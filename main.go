package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/patrickmn/go-cache"
	"github.com/philips-labs/ferrite/server"
	"github.com/philips-labs/hsdp-funcion-gateway/crontab"
	"github.com/philips-software/go-hsdp-api/iam"
	"github.com/philips-software/go-hsdp-api/iron"
)

const (
	backendKeepRunning = 180
)

type ironBackendRoundTripper struct {
	mu           sync.Mutex
	client       *iron.Client
	next         http.RoundTripper
	host         string
	requestCache *cache.Cache
}

func newIronBackendRoundTripper(next http.RoundTripper, client *iron.Client, host string) *ironBackendRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}

	return &ironBackendRoundTripper{
		next:         next,
		client:       client,
		host:         host,
		requestCache: cache.New(20*time.Minute, 40*time.Minute),
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
	var upstreamRequestURI string
	parts := strings.Split(req.RequestURI, "/")
	if len(parts) < 3 || !(parts[1] == "function" || parts[1] == "async-function" || parts[1] == "payload") {
		fmt.Printf("expected /{method}/{id}/..., got %s\n", req.RequestURI)
		return resp, fmt.Errorf("invalid request: %s", req.RequestURI)
	}
	if len(parts) > 3 {
		upstreamRequestURI = "/" + strings.Join(parts[3:], "/")
	} else {
		upstreamRequestURI = "/"
	}
	scheduleID := parts[2]
	path := parts[1]
	fmt.Printf("task from task/schedule [%s] calling handler for [%s] with requestURI [%s]\n", scheduleID, path, upstreamRequestURI)
	switch path {
	case "payload":
		return rt.handlePayload(scheduleID, req)
	case "function":
		return rt.handleRequest(scheduleID, upstreamRequestURI, req)
	default: // Async
		return rt.handleRequestAsync(scheduleID, upstreamRequestURI, req)
	}
}

func (rt *ironBackendRoundTripper) handleRequest(scheduleID, upstreamRequestURI string, req *http.Request) (resp *http.Response, err error) {
	schedule, _, err := rt.client.Schedules.GetSchedule(scheduleID)
	if err != nil {
		fmt.Printf("error retrieving schedule: %v\n", err)
		return resp, err
	}
	if schedule == nil {
		fmt.Printf("cannot locate schedule: %s\n", scheduleID)
		return resp, fmt.Errorf("cannot locate schedule: %s", scheduleID)
	}
	fmt.Printf("creating task from schedule %s\n", schedule.CodeName)
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
		req.URL.Path = upstreamRequestURI
	}
	fmt.Printf("sending request upstream: %s\n", req.RequestURI)
	resp, err = rt.next.RoundTrip(req)
	// Kill task after single handling. In the future we might keep this around for a while longer
	if resp != nil {
		fmt.Printf("response code: %d\n", resp.StatusCode)
	}
	fmt.Printf("cancelling task %s..\n", task.ID)
	_, _, _ = rt.client.Tasks.CancelTask(task.ID)
	return resp, err
}

func (rt *ironBackendRoundTripper) handleRequestAsync(scheduleID string, path string, req *http.Request) (resp *http.Response, err error) {
	// Validate if request is suitable for async handling
	if req.Method != http.MethodPost {
		return resp, fmt.Errorf("only the POST method is supported for async-function invocations")
	}
	callbackURL := req.Header.Get("X-Callback-URL")
	if callbackURL == "" {
		return resp, fmt.Errorf("missing X-Callback-URL header")
	}
	schedule, _, err := rt.client.Schedules.GetSchedule(scheduleID)
	if err != nil {
		fmt.Printf("error retrieving schedule: %v\n", err)
		return resp, err
	}
	if schedule == nil {
		fmt.Printf("cannot locate schedule: %s\n", scheduleID)
		return resp, fmt.Errorf("cannot locate schedule: %s", scheduleID)
	}
	fmt.Printf("creating async task from schedule %s\n", schedule.ID)
	cacheRequest := request{
		Callback: callbackURL,
		Path:     path,
	}
	headers := make(map[string]string)
	for k, v := range req.Header {
		headers[k] = v[0]
	}
	if req.Body != nil {
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			fmt.Printf("error reading body: %v\n", err)
			return resp, err
		}
		cacheRequest.Body = string(data)
	}
	jsonData, err := json.Marshal(&cacheRequest)
	if err != nil {
		fmt.Printf("error JSON enocding data: %v\n", err)
		return resp, err
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
	rt.requestCache.Set(task.ID, jsonData, cache.DefaultExpiration)
	return &http.Response{
		Status:     "202 Accepted",
		StatusCode: http.StatusAccepted,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Request:    req,
		Header:     make(http.Header, 0),
		Body:       ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"taskID\":\"%s\"}\n", task.ID))),
	}, nil
}

type request struct {
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Callback string            `json:"callback"`
	Path     string            `json:"path"`
}

func (rt *ironBackendRoundTripper) handlePayload(taskID string, req *http.Request) (*http.Response, error) {
	fmt.Printf("searching cache for task: %s\n", taskID)
	data, ok := rt.requestCache.Get(taskID)
	if !ok {
		fmt.Printf("request data for taskID not found: %s\n", taskID)
		return nil, fmt.Errorf("request data not found")
	}
	requestData, ok := data.([]byte)
	if !ok {
		fmt.Printf("cache item was not a byte array\n")
		return nil, fmt.Errorf("cache item was not a byte array")
	}
	headers := make(http.Header, 0)
	headers.Set("Content-Type", "application/json")

	fmt.Printf("returning payload: %s\n", string(requestData))
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Request:    req,
		Header:     headers,
		Body:       ioutil.NopCloser(bytes.NewBuffer(requestData)),
	}, nil
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
	authType := os.Getenv("GATEWAY_AUTH_TYPE")
	switch authType {
	case "token":
		e.Use(middlewareTokenAuth())
	case "iam":
		e.Use(middlewareIAMAuth())
	}

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

	done, err := crontab.Start(client) // Start crontab
	if err != nil {
		fmt.Printf("failed to start cronjob: %v\n", err)
		return
	}

	_ = e.Start(":8079")
	done <- true
}

func middlewareIAMAuth() echo.MiddlewareFunc {
	clientID := os.Getenv("AUTH_IAM_CLIENT_ID")
	clientSecret := os.Getenv("AUTH_IAM_CLIENT_SECRET")
	region := os.Getenv("AUTH_IAM_REGION")
	environment := os.Getenv("AUTH_IAM_ENVIRONMENT")
	orgIDs := strings.Split(os.Getenv("AUTH_IAM_ORGS"), ",")
	roles := strings.Split(os.Getenv("AUTH_IAM_ROLES"), ",")
	iamClient, err := iam.NewClient(http.DefaultClient, &iam.Config{
		Region:         region,
		Environment:    environment,
		OAuth2ClientID: clientID,
		OAuth2Secret:   clientSecret,
	})
	if err != nil {
		return permanentError(err)
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			var token string
			_, _ = fmt.Sscanf(authHeader, "Bearer %s", &token)
			introspect, _, err := iamClient.WithToken(token).Introspect()
			if err != nil {
				_ = c.String(http.StatusUnauthorized, err.Error())
				return err
			}
			allowed := false
			for _, org := range introspect.Organizations.OrganizationList {
				if allowed {
					break
				}
				if !contains(orgIDs, org.OrganizationID) {
					continue
				}
				for _, role := range org.Roles {
					if contains(roles, role) {
						allowed = true
						continue
					}
				}
			}
			if !allowed {
				_ = c.String(http.StatusUnauthorized, "access denied")
				return fmt.Errorf("access denied")
			}
			return next(c)
		}
	}
}

func middlewareTokenAuth() echo.MiddlewareFunc {
	authToken := os.Getenv("AUTH_TOKEN_TOKEN")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			var token string
			_, _ = fmt.Sscanf(authHeader, "Token %s", &token)
			if authToken != token {
				_ = c.String(http.StatusUnauthorized, "invalid token")
				return fmt.Errorf("invalid token")
			}
			return next(c)
		}
	}
}

func permanentError(err error) func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			_ = c.String(http.StatusInternalServerError, err.Error())
			return err
		}
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
