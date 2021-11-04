package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestTokenAuth(t *testing.T) {
	e := echo.New()
	e.Group("/foo", TokenAuth("xxx"))

	req := httptest.NewRequest(http.MethodPost, "/foo", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Token xxx")
	rec := httptest.NewRecorder()

	handler := TokenAuth("xxx")
	c := e.NewContext(req, rec)
	f := handler(func(context echo.Context) error {
		return c.String(http.StatusOK, "test")
	})
	assert.NoError(t, f(c))
}
