package handlers

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func Payload(rt *IronBackendRoundTripper) echo.HandlerFunc {
	return func(ctx echo.Context) error {
		taskID := ctx.Param("taskID")
		data, err := rt.getPayload(taskID)
		if err != nil {
			return err
		}
		return ctx.JSONBlob(http.StatusOK, data)
	}
}
