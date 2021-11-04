package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/patrickmn/go-cache"
	siderite "github.com/philips-labs/siderite/models"
	"github.com/philips-software/go-hsdp-api/iron"
)

const (
	backendKeepRunning = 7200
)

type IronBackendRoundTripper struct {
	*iron.Client
	*cache.Cache
	next http.RoundTripper
	host string
}

func NewIronBackendRoundTripper(next http.RoundTripper, client *iron.Client, host string) *IronBackendRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}

	return &IronBackendRoundTripper{
		next:   next,
		Client: client,
		host:   host,
		Cache:  cache.New(20*time.Minute, 40*time.Minute),
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

func (rt *IronBackendRoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	var upstreamRequestURI string
	parts := strings.Split(req.RequestURI, "/")
	if len(parts) < 3 || !(parts[1] == "function") { // TODO: remove prefix dependency
		fmt.Printf("expected /function/{id}/..., got %s\n", req.RequestURI)
		return resp, fmt.Errorf("invalid request: %s", req.RequestURI)
	}
	if len(parts) > 3 {
		upstreamRequestURI = "/" + strings.Join(parts[3:], "/")
	} else {
		upstreamRequestURI = "/"
	}
	codeID := parts[2]
	path := parts[1]
	fmt.Printf("task from codeID [%s] calling handler for [%s] with requestURI [%s]\n", codeID, path, upstreamRequestURI)
	return rt.handleRequest(codeID, upstreamRequestURI, req)
}

func (rt *IronBackendRoundTripper) handleRequest(codeID, upstreamRequestURI string, req *http.Request) (resp *http.Response, err error) {
	code, _, err := rt.Client.Codes.GetCode(codeID)
	if err != nil {
		fmt.Printf("error retrieving code: %v\n", err)
		return resp, err
	}
	schedules, _, err := rt.Client.Schedules.GetSchedulesWithCode(code.Name)
	if err != nil {
		fmt.Printf("error retrieving schedule: %v\n", err)
		return resp, err
	}
	fmt.Printf("found %d matching schedule(s)\n", len(*schedules))
	var schedule *iron.Schedule
	var cfg siderite.CronPayload
	for _, s := range *schedules {
		_ = json.Unmarshal([]byte(s.Payload), &cfg)
		if cfg.Type == "sync" {
			schedule = &s
			break
		}
	}
	if schedule == nil {
		fmt.Printf("cannot locate sync schedule for codeID: %s\n", codeID)
		return resp, fmt.Errorf("cannot locate schedule for codeID: %s", codeID)
	}
	fmt.Printf("creating task from schedule %s\n", schedule.CodeName)
	timeout := schedule.Timeout
	if timeout < 60 {
		timeout = backendKeepRunning
	}
	task, _, err := rt.Client.Tasks.QueueTask(iron.Task{
		CodeName: schedule.CodeName,
		Payload:  cfg.EncryptedPayload,
		Cluster:  schedule.Cluster,
		Timeout:  timeout,
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
		req.URL.Path = upstreamRequestURI
	}
	fmt.Printf("sending request upstream: %s\n", req.RequestURI)
	resp, err = rt.next.RoundTrip(req)
	// Kill task after single handling. In the future we might keep this around for a while longer
	if resp != nil {
		fmt.Printf("response code: %d\n", resp.StatusCode)
	}
	fmt.Printf("cancelling task %s..\n", task.ID)
	_, _, _ = rt.Client.Tasks.CancelTask(task.ID)
	return resp, err
}

type request struct {
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Callback string            `json:"callback"`
	Path     string            `json:"path"`
}

func (rt *IronBackendRoundTripper) getPayload(taskID string) ([]byte, error) {
	fmt.Printf("searching cache for task: %s\n", taskID)
	data, ok := rt.Cache.Get(taskID)
	if !ok {
		fmt.Printf("request data for taskID not found: %s\n", taskID)
		return nil, fmt.Errorf("request data not found")
	}
	requestData, ok := data.([]byte)
	if !ok {
		fmt.Printf("cache item was not a byte array\n")
		return nil, fmt.Errorf("cache item was not a byte array")
	}

	fmt.Printf("returning payload: %s\n", string(requestData))
	return requestData, nil
}
