package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/philips-software/go-hsdp-api/iron"
	"github.com/stretchr/testify/assert"
)

var (
	muxBackend    *http.ServeMux
	serverBackend *httptest.Server
	muxIRON       *http.ServeMux
	serverIRON    *httptest.Server
	client        *iron.Client
	projectID     = "48a0183d-a588-41c2-9979-737d15e9e860"
	token         = "YM7eZakYwqoui5znoH4g"
)

func setup(t *testing.T) func() {
	muxIRON = http.NewServeMux()
	serverIRON = httptest.NewServer(muxIRON)
	muxBackend = http.NewServeMux()
	serverBackend = httptest.NewServer(muxBackend)

	var err error

	client, err = iron.NewClient(&iron.Config{
		BaseURL:   serverIRON.URL,
		ProjectID: projectID,
		Token:     token,
	})
	assert.Nil(t, err)
	assert.NotNil(t, client)

	return func() {
		serverIRON.Close()
		serverBackend.Client()
	}
}

func TestAsyncHandler(t *testing.T) {
	var codeID = "20"
	teardown := setup(t)
	defer teardown()

	scheduleID := "8GSI27QGIZ5sSYRiMBIoASz8"
	muxIRON.HandleFunc(client.Path("projects", projectID, "schedules"), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "GET", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
  "schedules": [
    {
      "id": "`+scheduleID+`",
      "created_at": "2020-06-25T11:32:46.72Z",
      "updated_at": "2020-06-25T11:32:46.72Z",
      "project_id": "`+projectID+`",
      "status": "scheduled",
      "code_name": "testandy",
      "start_at": "2020-06-25T11:32:46.72Z",
      "end_at": "0001-01-01T00:00:00Z",
      "next_start": "2020-06-25T11:32:46.72Z",
      "last_run_time": "0001-01-01T00:00:00Z",
      "timeout": 7200,
      "run_times": 3,
      "run_every": 3600,
      "cluster": "DRxYM4SCFZBiJrsWytWju38C",
      "payload": "{\"type\":\"sync\"}"
    },
    {
      "id": "C8OvMrpP2f226nIMJQV5VNZz",
      "created_at": "2020-06-25T11:31:21.167Z",
      "updated_at": "2020-06-25T11:31:27.379Z",
      "project_id": "`+projectID+`",
      "status": "scheduled",
      "code_name": "testrichard",
      "start_at": "2020-06-25T11:31:21.167Z",
      "end_at": "0001-01-01T00:00:00Z",
      "next_start": "2020-06-25T12:31:21.167Z",
      "last_run_time": "2020-06-25T11:31:27.334Z",
      "timeout": 7200,
      "run_times": 3,
      "run_count": 1,
      "run_every": 3600,
      "cluster": "DRxYM4SCFZBiJrsWytWju38C",
      "payload": "{\"type\":\"sync\"}"
    }
  ]
}`)
	})
	muxIRON.HandleFunc(client.Path("projects", projectID, "schedules", scheduleID), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "GET", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
      "id": "`+scheduleID+`",
      "created_at": "2020-06-25T11:32:46.72Z",
      "updated_at": "2020-06-25T11:32:46.72Z",
      "project_id": "`+projectID+`",
      "status": "scheduled",
      "code_name": "testandy",
      "start_at": "2020-06-25T11:32:46.72Z",
      "end_at": "0001-01-01T00:00:00Z",
      "next_start": "2020-06-25T11:32:46.72Z",
      "last_run_time": "0001-01-01T00:00:00Z",
      "timeout": 7200,
      "run_times": 3,
      "run_every": 3600,
      "cluster": "XKaaLazEd1sAUAyZZN8IG6Tg",
      "payload": "{\"type\":\"sync\"}"
    }`)
	})

	muxIRON.HandleFunc(client.Path("projects", projectID, "codes", codeID), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "GET", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
  "id": "`+codeID+`",
  "created_at": "2020-06-23T23:13:43.949Z",
  "project_id": "5e20da41d748ad000ace7654",
  "name": "testandy",
  "image": "loafoe/siderite:0.99.20",
  "rev": 2,
  "latest_history_id": "5ef3a3c96f3bb20009ba9952",
  "latest_chanwge": "2020-06-24T19:04:41.782Z",
  "archived_at": "0001-01-01T00:00:00Z"
}`)
	})

	taskID := "bFp7OMpXdVsvRHp4sVtqb3gV"

	muxIRON.HandleFunc(client.Path("projects", projectID, "tasks"), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "POST", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"tasks":[{"id":"`+taskID+`"}],"msg":"Queued up"}`)
	})
	muxIRON.HandleFunc(client.Path("projects", projectID, "tasks", taskID), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "GET", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
      "id": "`+taskID+`",
      "created_at": "2020-06-23T09:47:07.967Z",
      "updated_at": "2020-06-23T10:19:58.119Z",
      "project_id": "Bny3gFLzLlMrFFDrujopyocu",
      "code_id": "5e6640a5fbce220009c0385e",
      "code_history_id": "5e6640a5fbce220009c0385f",
      "status": "cancelled",
      "msg": "Cancelled via API.",
      "code_name": "loafoe/siderite",
      "code_rev": "1",
      "start_time": "2020-06-23T09:47:11.85Z",
      "end_time": "0001-01-01T00:00:00Z",
      "timeout": 3600,
      "payload": "mu4xSCwztB79NcmrJvFEdRnw0priIxMDxLPencrypted",
      "schedule_id": "5eebb5113de052000a93b1f5",
      "message_id": "6841477577898197071",
      "cluster": "9PbpheKmd0bSHIelR7O6ChcH"
    }`)
	})

	// Reverse proxy
	origin, _ := url.Parse(serverBackend.URL) // Upstream
	targets := []*middleware.ProxyTarget{
		{
			URL: origin,
		},
	}
	balancer := middleware.NewRoundRobinBalancer(targets)
	transport := NewIronBackendRoundTripper(http.DefaultTransport, client, origin.Host)
	proxyMiddleware := middleware.ProxyWithConfig(middleware.ProxyConfig{
		Balancer:  balancer,
		Transport: transport,
	})
	e := echo.New()
	e.Group("/function", proxyMiddleware)

	req := httptest.NewRequest(http.MethodPost, "/function/"+codeID+"/20", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	handler := proxyMiddleware(func(ctx echo.Context) error {
		return nil
	})
	c := e.NewContext(req, rec)
	err := handler(c)
	assert.Nil(t, err)
}
