package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func permanentError(err error) func(next echo.HandlerFunc) echo.HandlerFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			_ = c.String(http.StatusInternalServerError, err.Error())
			return err
		}
	}
}
