package middleware

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/philips-software/go-hsdp-api/iam"
)

type IAMConfig struct {
	Client        *http.Client
	ClientID      string
	ClientSecret  string
	Region        string
	Environment   string
	Organizations []string
	Roles         []string
}

// IAMAuth implements IAM authorization
func IAMAuth(config IAMConfig) echo.MiddlewareFunc {
	httpClient := http.DefaultClient
	if config.Client != nil {
		httpClient = config.Client
	}
	iamClient, err := iam.NewClient(httpClient, &iam.Config{
		Region:         config.Region,
		Environment:    config.Environment,
		OAuth2ClientID: config.ClientID,
		OAuth2Secret:   config.ClientSecret,
	})
	if err != nil {
		return permanentError(err)
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			var token string
			_, _ = fmt.Sscanf(authHeader, "Bearer %s", &token)
			introspect, _, err := iamClient.WithToken(token).Introspect()
			if err != nil {
				_ = c.String(http.StatusUnauthorized, err.Error())
				return err
			}
			allowed := false
			for _, org := range introspect.Organizations.OrganizationList {
				if allowed {
					break
				}
				if !contains(config.Organizations, org.OrganizationID) {
					continue
				}
				for _, role := range org.Roles {
					if contains(config.Roles, role) {
						allowed = true
						continue
					}
				}
			}
			if !allowed {
				_ = c.String(http.StatusUnauthorized, "access denied")
				return fmt.Errorf("access denied")
			}
			return next(c)
		}
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
