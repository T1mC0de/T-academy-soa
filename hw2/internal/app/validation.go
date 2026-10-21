package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	middleware "github.com/oapi-codegen/nethttp-middleware"

	"marketplace/internal/api"
)

// newValidator создаёт middleware, который проверяет каждый запрос по api/openapi.yaml
// (minLength, maximum, pattern, enum, required, format и т.д.).
// Невалидный запрос получает VALIDATION_ERROR и до хендлеров не доходит.
func newValidator() (func(http.Handler) http.Handler, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil // не проверяем Host

	// kin-openapi по умолчанию не проверяет format: email и uuid — включаем явно.
	openapi3.DefineStringFormatValidator("email", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForEmail))
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`))

	return middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		Options: openapi3filter.Options{
			MultiError: true, // собрать все нарушения, а не только первое
			// JWT проверяет authMiddleware, здесь security-схему пропускаем.
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ middleware.ErrorHandlerOpts) {
			if errors.Is(err, routers.ErrPathNotFound) || errors.Is(err, routers.ErrMethodNotAllowed) {
				writeError(w, newError(http.StatusNotFound, api.NOTFOUND, "route not found"))
				return
			}
			writeError(w, errValidation("request validation failed", collectViolations(err, "")))
		},
	}), nil
}

// collectViolations разворачивает ошибки kin-openapi в плоский список {field, message}.
// field — имя параметра (query/path) или путь до поля в теле запроса (например items.0.quantity).
func collectViolations(err error, field string) []map[string]string {
	switch e := err.(type) {
	case openapi3.MultiError: // несколько ошибок сразу
		var out []map[string]string
		for _, inner := range e {
			out = append(out, collectViolations(inner, field)...)
		}
		return out

	case *openapi3filter.RequestError: // ошибка в параметре или теле запроса
		if e.Parameter != nil {
			field = e.Parameter.Name
		}
		if e.Err == nil {
			return []map[string]string{{"field": orBody(field), "message": e.Reason}}
		}
		return collectViolations(e.Err, field)

	case *openapi3.SchemaError: // нарушено конкретное ограничение схемы
		if path := e.JSONPointer(); len(path) > 0 {
			field = strings.Join(path, ".")
		}
		return []map[string]string{{"field": orBody(field), "message": e.Reason}}

	default:
		return []map[string]string{{"field": orBody(field), "message": err.Error()}}
	}
}

func orBody(field string) string {
	if field == "" {
		return "body"
	}
	return field
}
