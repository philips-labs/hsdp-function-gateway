package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/philips-labs/ferrite/server"
	"github.com/philips-labs/hsdp-funcion-gateway/crontab"
	"github.com/philips-labs/hsdp-funcion-gateway/handlers"
	mw "github.com/philips-labs/hsdp-funcion-gateway/middleware"
	"github.com/philips-software/go-hsdp-api/iron"
)

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
	transport := handlers.NewIronBackendRoundTripper(http.DefaultTransport, client, "localhost:8081")
	proxyMiddleware := middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  balancer,
		Transport: transport,
	})

	af := e.Group("/async-function", authMiddleware)
	af.POST("/:codeID/*", handlers.Async(transport))
	af.POST("/:codeID", handlers.Async(transport))

	e.Group("/function", authMiddleware, proxyMiddleware)
	e.Group("/sync-function", authMiddleware, proxyMiddleware)

	e.Group("/payload", mw.TokenAuth(authToken)).GET("/:taskID", handlers.Payload(transport))

	done, err := crontab.Start(client) // Start crontab
	if err != nil {
		fmt.Printf("failed to start cronjob: %v\n", err)
		return
	}

	_ = e.Start(":8079")
	done <- true
}
