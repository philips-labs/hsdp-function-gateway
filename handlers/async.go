package handlers

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/patrickmn/go-cache"
	siderite "github.com/philips-labs/siderite/models"
	"github.com/philips-software/go-hsdp-api/iron"
)

func Async(rt *IronBackendRoundTripper) echo.HandlerFunc {
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
			return fmt.Errorf("error JSON encoding data: %w", err)
		}
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
			return fmt.Errorf("failed to spawn task: %w", err)
		}
		rt.Cache.Set(task.ID, jsonData, cache.DefaultExpiration)

		return ctx.JSONBlob(http.StatusAccepted, []byte(fmt.Sprintf("{\"taskID\":\"%s\"}\n", task.ID)))
	}
}
