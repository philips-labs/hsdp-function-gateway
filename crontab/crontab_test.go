package crontab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/philips-software/go-hsdp-api/iron"
	"github.com/stretchr/testify/assert"
)

var (
	muxIRON    *http.ServeMux
	serverIRON *httptest.Server
	client     *iron.Client
	projectID  = "48a0183d-a588-41c2-9979-737d15e9e860"
	token      = "YM7eZakYwqoui5znoH4g"
)

func setup(t *testing.T) func() {
	muxIRON = http.NewServeMux()
	serverIRON = httptest.NewServer(muxIRON)

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
	}
}

func TestCrontabRefresh(t *testing.T) {
	var scheduleID = "xxx-xxx"

	teardown := setup(t)
	defer teardown()

	muxIRON.HandleFunc(client.Path("projects", projectID, "schedules"), func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "POST", r.Method) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"schedules":[{"id":"`+scheduleID+`"}]}`)
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
      "payload": "{}"
    }`)
	})

	done, err := Start(client)

	if !assert.Nil(t, err) {
		return
	}
	if !assert.NotNil(t, done) {
		return
	}
	done <- true
}
