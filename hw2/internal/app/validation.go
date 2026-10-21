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

// newValidator создаёт middleware, который проверяет каждый запрос по api/openapi.yaml:
// обязательные поля, minLength, minimum, pattern, enum, format и т.д.
// Невалидный запрос получает 400 VALIDATION_ERROR и до хендлеров не доходит.
func newValidator() (func(http.Handler) http.Handler, error) {
	// Спецификация вшита в сгенерированный код (embedded-spec).
	spec, err := api.GetSpec()
	if err != nil {
		return nil, err
	}

	// kin-openapi по умолчанию не проверяет format: uuid — включаем явно.
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`))

	return middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		Options: openapi3filter.Options{
			MultiError: true, // собрать все нарушения, а не только первое
		},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ middleware.ErrorHandlerOpts) {
			// Такого пути или метода нет в спецификации.
			if errors.Is(err, routers.ErrPathNotFound) || errors.Is(err, routers.ErrMethodNotAllowed) {
				writeError(w, newError(http.StatusNotFound, api.NOTFOUND, "route not found"))
				return
			}
			writeError(w, errValidation(collectViolations(err, "body")))
		},
	}), nil
}

// collectViolations превращает ошибку валидатора в плоский список {field, message}.
// Ошибка валидатора — это «дерево»: список ошибок, внутри которых могут быть ещё ошибки.
// Поэтому функция вызывает сама себя, пока не дойдёт до конкретного нарушения.
// field — имя заголовка/параметра или путь до поля в теле (например, items.0.quantity).
func collectViolations(err error, field string) []map[string]string {
	switch e := err.(type) {
	case openapi3.MultiError: // несколько ошибок — разбираем каждую
		var result []map[string]string
		for _, inner := range e {
			result = append(result, collectViolations(inner, field)...)
		}
		return result

	case *openapi3filter.RequestError: // ошибка в параметре (query, path, header) или в теле
		if e.Parameter != nil {
			field = e.Parameter.Name
		}
		if e.Err != nil {
			return collectViolations(e.Err, field)
		}
		return []map[string]string{{"field": field, "message": e.Reason}}

	case *openapi3.SchemaError: // нарушено конкретное ограничение схемы
		if path := e.JSONPointer(); len(path) > 0 {
			field = strings.Join(path, ".")
		}
		return []map[string]string{{"field": field, "message": e.Reason}}

	default:
		return []map[string]string{{"field": field, "message": err.Error()}}
	}
}
