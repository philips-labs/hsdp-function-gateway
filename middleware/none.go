package middleware

import "github.com/labstack/echo/v4"

// NoneAuth implements no auth
func NoneAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
