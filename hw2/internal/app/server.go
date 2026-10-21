package app

import (
	"database/sql"
	"net/http"
	"time"

	"marketplace/internal/api"
)

// Server реализует сгенерированный интерфейс api.StrictServerInterface:
// по одному методу на каждую операцию из OpenAPI.
// Методы разложены по файлам: products.go, orders.go, promo_codes.go.
type Server struct {
	db             *sql.DB
	orderRateLimit time.Duration // N минут между операциями CREATE_ORDER / UPDATE_ORDER
}

// Проверка при компиляции: если в спецификации есть эндпоинт,
// а метода для него нет, проект не соберётся.
var _ api.StrictServerInterface = (*Server)(nil)

func NewServer(db *sql.DB, orderRateLimit time.Duration) *Server {
	return &Server{db: db, orderRateLimit: orderRateLimit}
}

// Handler собирает цепочку обработки запроса:
//
//	логирование → валидация по OpenAPI → сгенерированный роутер → метод Server
func (s *Server) Handler() (http.Handler, error) {
	validator, err := newValidator()
	if err != nil {
		return nil, err
	}

	// Сгенерированный код не смог разобрать запрос (на практике это ловит валидатор раньше).
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, errValidation([]map[string]string{{"field": "request", "message": err.Error()}}))
	}
	// Метод Server вернул ошибку → пишем её в едином формате.
	handlerError := func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, err)
	}

	// strict-обёртка: разбирает запрос в DTO, вызывает метод Server, пишет ответ в JSON.
	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  badRequest,
		ResponseErrorHandlerFunc: handlerError,
	})
	// Роутер: сопоставляет метод и путь с нужной операцией.
	router := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       http.NewServeMux(),
		ErrorHandlerFunc: badRequest,
	})

	return loggingMiddleware(validator(router)), nil
}
