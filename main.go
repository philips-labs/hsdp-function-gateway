package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/cloudfoundry-community/gautocloud"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/philips-software/gautocloud-connectors/hsdp"
	"github.com/philips-software/go-hsdp-api/iron"
)

type ironBackendRoundTripper struct {
	client  *hsdp.IronClient
	next    http.RoundTripper
	spawned bool
	task    *iron.Task
}

func newIronBackendRoundTripper(next http.RoundTripper, client *hsdp.IronClient) *ironBackendRoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &ironBackendRoundTripper{
		next:   next,
		client: client,
	}
}

func (rt *ironBackendRoundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	if !rt.spawned {
		// TODO: spawn iron backend if needed before performing request
		var codeName string
		fmt.Printf("Checking: %s\n", req.RequestURI)
		fmt.Sscanf(req.RequestURI, "/function/%s", &codeName)
		if codeName != "" {
			schedules, _, err := rt.client.Schedules.GetSchedules()
			if err != nil {
				fmt.Printf("error retrieving schedules: %v\n", err)
				return rt.next.RoundTrip(req)
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
				return rt.next.RoundTrip(req)
			}
			task, _, err := rt.client.Tasks.QueueTask(iron.Task{
				CodeName: schedule.CodeName,
				Payload:  schedule.Payload,
				Timeout:  3600,
			})
			if err != nil {
				fmt.Printf("failed to spawn task: %v\n", err)
				return rt.next.RoundTrip(req)
			}
			rt.task = task
			rt.spawned = true
		}
	}
	if rt.task != nil {
		fmt.Printf("Using task as upstream: %s\n", rt.task.ID)
	} else {
		fmt.Printf("No upstream running..\n")
	}
	return rt.next.RoundTrip(req)
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
	r := e.Group("/")
	r.Use(middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  middleware.NewRandomBalancer(targets),
		Transport: newIronBackendRoundTripper(http.DefaultTransport, client),
	}))
	_ = e.Start(":8079")
}
