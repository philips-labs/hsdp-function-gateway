package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/patrickmn/go-cache"
	"github.com/philips-labs/ferrite/server"
	"github.com/philips-labs/hsdp-funcion-gateway/crontab"
	mw "github.com/philips-labs/hsdp-funcion-gateway/middleware"
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
	if len(parts) < 3 || !(parts[1] == "function") {
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
	task, _, err := rt.Client.Tasks.QueueTask(iron.Task{
		CodeName: schedule.CodeName,
		Payload:  cfg.EncryptedPayload,
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

func main() {
	var config iron.Config

	err := json.Unmarshal([]byte(os.Getenv("IRON_CONFIG")), &config)
	if err != nil {
		fmt.Printf("no iron services found: %v\n", err)
		return
	}
	if os.Getenv("BACKEND_TYPE") == "ferrite" { // Need bootstrap
		bootstrap, err := server.Bootstrap(config.BaseURL, config.Token)
		if err != nil {
			fmt.Printf("Error bootstrapping: %v\n", err)
			return
		}
		config.ProjectID = bootstrap.ProjectID
		config.Project = bootstrap.ProjectID
		config.ClusterInfo[0].ClusterID = bootstrap.ClusterID
		config.ClusterInfo[0].Pubkey = bootstrap.PublicKey
		fmt.Printf("Bootstrapped ferrite config\n")
	}

	client, err := iron.NewClient(&config)
	if err != nil {
		fmt.Printf("invalid client: %v\n", err)
		return
	}
	codes, _, _ := client.Codes.GetCodes()
	if codes != nil {
		for _, c := range *codes {
			fmt.Printf("code: %s:%s -> %s\n", c.ID, c.Name, c.Image)
		}
	}

	e := echo.New()
	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	// Authentication
	authToken := os.Getenv("AUTH_TOKEN_TOKEN")
	authType := os.Getenv("GATEWAY_AUTH_TYPE")
	var authMiddleware echo.MiddlewareFunc
	switch authType {
	case "none":
		authMiddleware = mw.NoneAuth()
	case "iam":
		cfg := mw.IAMConfig{
			ClientID:      os.Getenv("AUTH_IAM_CLIENT_ID"),
			ClientSecret:  os.Getenv("AUTH_IAM_CLIENT_SECRET"),
			Region:        os.Getenv("AUTH_IAM_REGION"),
			Environment:   os.Getenv("AUTH_IAM_ENVIRONMENT"),
			Organizations: strings.Split(os.Getenv("AUTH_IAM_ORGS"), ","),
			Roles:         strings.Split(os.Getenv("AUTH_IAM_ROLES"), ","),
		}
		authMiddleware = mw.IAMAuth(cfg)
	case "token":
		authMiddleware = mw.TokenAuth(authToken)
	default:
		fmt.Printf("invalid authType: %s, falling back to 'token'\n", authType)
		authMiddleware = mw.TokenAuth(authToken)
	}

	// Reverse proxy
	origin, _ := url.Parse("http://localhost:8081/") // Upstream
	targets := []*middleware.ProxyTarget{
		{
			URL: origin,
		},
	}
	balancer := middleware.NewRoundRobinBalancer(targets)
	transport := NewIronBackendRoundTripper(http.DefaultTransport, client, "localhost:8081")
	proxyMiddleware := middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  balancer,
		Transport: transport,
	})

	as := e.Group("/async-function", authMiddleware)
	as.POST("/:codeID/*", asyncHandler(transport))
	as.POST("/:codeID", asyncHandler(transport))

	e.Group("/function", authMiddleware, proxyMiddleware)

	e.Group("/payload", mw.TokenAuth(authToken)).GET("/:taskID", payloadHandler(transport))

	done, err := crontab.Start(client) // Start crontab
	if err != nil {
		fmt.Printf("failed to start cronjob: %v\n", err)
		return
	}

	_ = e.Start(":8079")
	done <- true
}

func asyncHandler(rt *IronBackendRoundTripper) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		callbackURL := ctx.Request().Header.Get("X-Callback-URL")
		if callbackURL == "" {
			return fmt.Errorf("missing X-Callback-URL header")
		}
		codeID := ctx.Param("codeID")
		path := ctx.Param("*")
		code, _, err := rt.Client.Codes.GetCode(codeID)
		if err != nil {
			return fmt.Errorf("error retrieving code: %w", err)
		}
		schedules, _, err := rt.Client.Schedules.GetSchedulesWithCode(code.Name)
		if err != nil {
			return fmt.Errorf("error retrieving schedule: %w", err)
		}
		fmt.Printf("found %d matching schedule(s)\n", len(*schedules))

		var schedule *iron.Schedule
		var cfg siderite.CronPayload
		for _, s := range *schedules {
			_ = json.Unmarshal([]byte(s.Payload), &cfg)
			if cfg.Type == "async" {
				schedule = &s
				break
			}
		}
		if schedule == nil {
			return fmt.Errorf("cannot async locate schedule for codeID: %s", codeID)
		}
		fmt.Printf("creating async task from schedule %s\n", schedule.ID)
		cacheRequest := request{
			Callback: callbackURL,
			Path:     path,
		}
		headers := make(map[string]string)
		for k, v := range ctx.Request().Header {
			headers[k] = v[0]
		}
		if ctx.Request().Body != nil {
			data, err := ioutil.ReadAll(ctx.Request().Body)
			if err != nil {
				return fmt.Errorf("error reading body: %w", err)
			}
			cacheRequest.Body = string(data)
		}
		jsonData, err := json.Marshal(&cacheRequest)
		if err != nil {
			return fmt.Errorf("error JSON enocding data: %w", err)
		}
		task, _, err := rt.Client.Tasks.QueueTask(iron.Task{
			CodeName: schedule.CodeName,
			Payload:  cfg.EncryptedPayload,
			Cluster:  schedule.Cluster,
			Timeout:  backendKeepRunning,
		})
		if err != nil {
			return fmt.Errorf("failed to spawn task: %w", err)
		}
		rt.Cache.Set(task.ID, jsonData, cache.DefaultExpiration)

		return ctx.JSONBlob(http.StatusAccepted, []byte(fmt.Sprintf("{\"taskID\":\"%s\"}\n", task.ID)))
	}
}

func payloadHandler(rt *IronBackendRoundTripper) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		taskID := ctx.Param("taskID")
		data, err := rt.getPayload(taskID)
		if err != nil {
			return err
		}
		return ctx.JSONBlob(http.StatusOK, data)
	}
}
