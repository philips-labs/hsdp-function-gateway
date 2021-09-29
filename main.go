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
	siderite "github.com/philips-labs/siderite/models"
	"github.com/philips-software/go-hsdp-api/iam"
	"github.com/philips-software/go-hsdp-api/iron"
)

const (
	backendKeepRunning = 7200
)

type ironBackendRoundTripper struct {
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

func (rt *ironBackendRoundTripper) handleRequest(codeID, upstreamRequestURI string, req *http.Request) (resp *http.Response, err error) {
	code, _, err := rt.client.Codes.GetCode(codeID)
	if err != nil {
		fmt.Printf("error retrieving code: %v\n", err)
		return resp, err
	}
	schedules, _, err := rt.client.Schedules.GetSchedulesWithCode(code.Name)
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
	task, _, err := rt.client.Tasks.QueueTask(iron.Task{
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
	_, _, _ = rt.client.Tasks.CancelTask(task.ID)
	return resp, err
}

type request struct {
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Callback string            `json:"callback"`
	Path     string            `json:"path"`
}

func (rt *ironBackendRoundTripper) getPayload(taskID string) ([]byte, error) {
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
	authType := os.Getenv("GATEWAY_AUTH_TYPE")
	var authMiddleware echo.MiddlewareFunc
	switch authType {
	case "none":
		authMiddleware = noneAuth()
	case "iam":
		authMiddleware = middlewareIAMAuth()
	case "token":
		authMiddleware = middlewareTokenAuth()
	default:
		fmt.Printf("invalid authType: %s, falling back to 'token'\n", authType)
		authMiddleware = noneAuth()
	}

	// Reverse proxy
	origin, _ := url.Parse("http://localhost:8081/") // Upstream
	targets := []*middleware.ProxyTarget{
		{
			URL: origin,
		},
	}
	balancer := middleware.NewRoundRobinBalancer(targets)
	transport := newIronBackendRoundTripper(http.DefaultTransport, client, "localhost:8081")
	proxyMiddleware := middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  balancer,
		Transport: transport,
	})

	as := e.Group("/async-function", authMiddleware)
	as.POST("/:codeID/*", asyncHandler(transport))
	as.POST("/:codeID", asyncHandler(transport))

	e.Group("/function", authMiddleware, proxyMiddleware)

	e.Group("/payload", middlewareTokenAuth()).GET("/:taskID", payloadHandler(transport))

	done, err := crontab.Start(client) // Start crontab
	if err != nil {
		fmt.Printf("failed to start cronjob: %v\n", err)
		return
	}

	_ = e.Start(":8079")
	done <- true
}

func asyncHandler(rt *ironBackendRoundTripper) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		callbackURL := ctx.Request().Header.Get("X-Callback-URL")
		if callbackURL == "" {
			return fmt.Errorf("missing X-Callback-URL header")
		}
		codeID := ctx.Param("codeID")
		path := ctx.Param("*")
		code, _, err := rt.client.Codes.GetCode(codeID)
		if err != nil {
			return fmt.Errorf("error retrieving code: %w", err)
		}
		schedules, _, err := rt.client.Schedules.GetSchedulesWithCode(code.Name)
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
		task, _, err := rt.client.Tasks.QueueTask(iron.Task{
			CodeName: schedule.CodeName,
			Payload:  cfg.EncryptedPayload,
			Cluster:  schedule.Cluster,
			Timeout:  backendKeepRunning,
		})
		if err != nil {
			return fmt.Errorf("failed to spawn task: %w", err)
		}
		rt.requestCache.Set(task.ID, jsonData, cache.DefaultExpiration)

		return ctx.JSONBlob(http.StatusAccepted, []byte(fmt.Sprintf("{\"taskID\":\"%s\"}\n", task.ID)))
	}
}
func payloadHandler(rt *ironBackendRoundTripper) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		taskID := ctx.Param("taskID")
		data, err := rt.getPayload(taskID)
		if err != nil {
			return err
		}
		return ctx.JSONBlob(http.StatusOK, data)
	}
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

func noneAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
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
