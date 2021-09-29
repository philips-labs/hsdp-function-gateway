package middleware

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// TokenAuth implements token auth
func TokenAuth(authToken string) echo.MiddlewareFunc {
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
