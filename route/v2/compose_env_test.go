package v2_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/IceWhaleTech/CasaOS-AppManagement/codegen"
	v2 "github.com/IceWhaleTech/CasaOS-AppManagement/route/v2"
	"github.com/IceWhaleTech/CasaOS-Common/utils/logger"
	"github.com/deepmap/oapi-codegen/pkg/middleware"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/labstack/echo/v4"
	"gotest.tools/v3/assert"
)

// the OpenAPI validator of route/v2.go in front of the real handler, no docker: `dry_run` returns
// before the app is resolved.
func putEnv(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	logger.LogInitConsoleOnly()

	swagger, err := codegen.GetSwagger()
	assert.NilError(t, err)
	base, err := url.Parse(swagger.Servers[0].URL)
	assert.NilError(t, err)

	e := echo.New()
	e.Use(middleware.OapiRequestValidatorWithOptions(swagger, &middleware.Options{
		Options: openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}))
	codegen.RegisterHandlersWithBaseURL(e, v2.NewAppManagement(), strings.TrimRight(base.Path, "/"))

	req := httptest.NewRequest(http.MethodPut, strings.TrimRight(base.Path, "/")+"/compose/wg-easy/env?dry_run=true", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMETextPlain)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// the contract says an empty body removes the file: the validator must let it through
func TestApplyComposeAppEnvAcceptsEmptyBody(t *testing.T) {
	for _, body := range []string{"", " \n", "KEY=value\n"} {
		rec := putEnv(t, body)
		assert.Equal(t, rec.Code, http.StatusOK, "body %q: %s", body, rec.Body.String())
	}

	rec := putEnv(t, "KEY VALUE\n")
	assert.Equal(t, rec.Code, http.StatusBadRequest, rec.Body.String())
	assert.Assert(t, strings.Contains(rec.Body.String(), "line 1"), rec.Body.String())
}
