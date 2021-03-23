package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudfoundry-community/gautocloud"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/philips-software/gautocloud-connectors/hsdp"
	"github.com/philips-software/go-hsdp-api/iron"
)

const (
	backendKeepRunning = 180
)

type ironBackendRoundTripper struct {
	mu     sync.Mutex
	client *hsdp.IronClient
	next   http.RoundTripper
	host   string
}

func newIronBackendRoundTripper(next http.RoundTripper, client *hsdp.IronClient, host string) *ironBackendRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &ironBackendRoundTripper{
		next:   next,
		client: client,
		host:   host,
	}
}

func waitForPort(timeout time.Duration, host string) (bool, error) {
	if timeout == 0 {
		timeout = time.Duration(1) * time.Minute
	}
	until := time.Now().Add(timeout)
	for {
		var conn net.Conn
		conn, _ = net.DialTimeout("tcp", host, timeout)
		if conn != nil {
			err := conn.Close()
			return true, err
		}
		time.Sleep(100 * time.Millisecond)
		if time.Now().After(until) {
			return false, fmt.Errorf("timed out waiting for %s", host)
		}
	}
}

func (rt *ironBackendRoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	var codeName string
	var upstreamRequestURI string
	parts := strings.Split(req.RequestURI, "/")
	if len(parts) < 3 || parts[1] != "function" {
		fmt.Printf("expected /function/{codeName}/..., got %s\n", req.RequestURI)
		return resp, fmt.Errorf("invalid request: %s", req.RequestURI)
	}
	if len(parts) > 3 {
		upstreamRequestURI = "/" + strings.Join(parts[3:], "/")
	} else {
		upstreamRequestURI = "/"
	}
	codeName = "hsdp-function-" + parts[2]
	schedules, _, err := rt.client.Schedules.GetSchedules()
	if err != nil {
		fmt.Printf("error retrieving schedules: %v\n", err)
		return resp, err
	}
	var schedule *iron.Schedule
	for _, s := range *schedules {
		if s.CodeName == codeName {
			var sch iron.Schedule
			sch = s
			schedule = &sch
		}
	}
	if schedule == nil {
		fmt.Printf("cannot locate code: %s\n", codeName)
		return resp, fmt.Errorf("cannot locate code: %s", codeName)
	}
	fmt.Printf("creating task from code %s\n", schedule.CodeName)
	task, _, err := rt.client.Tasks.QueueTask(iron.Task{
		CodeName: schedule.CodeName,
		Payload:  schedule.Payload,
		Cluster:  schedule.Cluster,
		Timeout:  backendKeepRunning,
	})
	if err != nil {
		fmt.Printf("failed to spawn task: %v\n", err)
		return resp, err
	}
	fmt.Printf("waiting for iron worker to connect..\n")
	connected, err := waitForPort(time.Duration(1)*time.Minute, rt.host)
	if err != nil {
		fmt.Printf("waitForPort %s failed: %v\n", rt.host, err)
		return resp, fmt.Errorf("waitForPort %s failed: %w", rt.host, err)
	}
	if !connected {
		fmt.Printf("upstream failed to connect in time\n")
		return resp, fmt.Errorf("upstream failed to connect in time")
	}
	if upstreamRequestURI != "" {
		req.RequestURI = upstreamRequestURI
	}
	fmt.Printf("sending request upstream: %s\n", req.RequestURI)
	resp, err = rt.next.RoundTrip(req)
	// Kill tasks after single handling
	if resp != nil {
		fmt.Printf("response code: %d\n", resp.StatusCode)
	}
	fmt.Printf("cancelling task %s..\n", task.ID)
	rt.client.Tasks.CancelTask(task.ID)
	return resp, err
}

func main() {
	clients, err := gautocloud.GetAll("hsdp:iron-client")
	if err != nil {
		fmt.Printf("no iron services found: %v\n", err)
	}
	fmt.Printf("found %d client(s)\n", len(clients))

	var client *hsdp.IronClient
	for _, c := range clients {
		var ok bool
		client, ok = c.(*hsdp.IronClient)
		if !ok {
			fmt.Printf("invalid client: %q\n", c)
			return
		}
		codes, _, err := client.Codes.GetCodes()
		if err != nil {
			fmt.Printf("error getting codes: %v\n", err)
			continue
		}
		for _, c := range *codes {
			fmt.Printf("code: %s:%s -> %s\n", c.ID, c.Name, c.Image)
		}
	}

	e := echo.New()
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	// Reverse proxy
	origin, _ := url.Parse("http://localhost:8081/") // Upstream
	targets := []*middleware.ProxyTarget{
		{
			URL: origin,
		},
	}
	e.Use(middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  middleware.NewRoundRobinBalancer(targets),
		Transport: newIronBackendRoundTripper(http.DefaultTransport, client, "localhost:8081"),
	}))
	_ = e.Start(":8079")
}
