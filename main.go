package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	mu           sync.Mutex
	backendStart time.Time
	client       *hsdp.IronClient
	next         http.RoundTripper
	running      bool
	host         string
	task         *iron.Task
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
	// TODO: support /async-function/{code} invocation. Should spawn dedicated task
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.running || time.Now().Sub(rt.backendStart) < time.Duration(60)*time.Second {
		// Cancel the task. TODO: investigate graceful shutdown
		if rt.running && rt.task != nil {
			rt.client.Tasks.CancelTask(rt.task.ID)
			rt.task = nil
			rt.running = false
		}
		var codeName string
		fmt.Printf("Checking: %s\n", req.RequestURI)
		fmt.Sscanf(req.RequestURI, "/function/%s", &codeName)
		if codeName != "" { // TODO: should we only allow the original function?
			codeName = "hsdp-function-" + codeName
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
			rt.backendStart = time.Now()
			rt.task = task
			rt.running = true
			// Wait for connection. TODO: poll port 8081 until it responds
			time.Sleep(time.Duration(5) * time.Second)
			connected, err := waitForPort(time.Duration(1)*time.Minute, rt.host)
			if err != nil {
				fmt.Printf("waitForPort %s failed: %v\n", rt.host, err)
				return resp, fmt.Errorf("waitForPort %s failed: %w", rt.host, err)
			}
			if !connected {
				fmt.Printf("upstream failed to connect in time\n")
				return resp, fmt.Errorf("upstream failed to connect in time")
			}
		}
	}
	// At this point we should have a backend
	if rt.task == nil {
		fmt.Printf("no upstream running..\n")
		return resp, fmt.Errorf("no upstream running")
	}
	fmt.Printf("sending request upstream..\n")
	resp, err = rt.next.RoundTrip(req)
	// Kill tasks after single handling
	fmt.Printf("cancelling task %s..\n", rt.task.ID)
	rt.client.Tasks.CancelTask(rt.task.ID)
	rt.task = nil
	rt.running = false
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
		Balancer:  middleware.NewRandomBalancer(targets),
		Transport: newIronBackendRoundTripper(http.DefaultTransport, client, "localhost:8081"),
	}))
	_ = e.Start(":8079")
}
