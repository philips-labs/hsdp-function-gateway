package main

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cloudfoundry-community/gautocloud"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/philips-software/gautocloud-connectors/hsdp"
	"github.com/philips-software/go-hsdp-api/iam"
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
		req.URL.Path = upstreamRequestURI
	}
	fmt.Printf("sending request upstream: %s\n", req.RequestURI)
	resp, err = rt.next.RoundTrip(req)
	// Kill task after single handling. In the future we might keep this around for a while longer
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
	_ = e.Start(":8079")
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
			fmt.Sscanf(authHeader, "Bearer %s", &token)
			introspect, _, err := iamClient.WithToken(token).Introspect()
			if err != nil {
				c.String(http.StatusUnauthorized, err.Error())
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
				c.String(http.StatusUnauthorized, "access denied")
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
			fmt.Sscanf(authHeader, "Token %s", &token)
			if authToken != token {
				c.String(http.StatusUnauthorized, "invalid token")
				return fmt.Errorf("invalid token")
			}
			return next(c)
		}
	}
}

func permanentError(err error) func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.String(http.StatusInternalServerError, err.Error())
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
