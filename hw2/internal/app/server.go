package app

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"marketplace/internal/api"
)

// Server реализует сгенерированный интерфейс api.StrictServerInterface.
// Методы разложены по файлам: auth.go, products.go, orders.go, promo_codes.go.
type Server struct {
	db             *pgxpool.Pool
	jwtSecret      []byte
	orderRateLimit time.Duration // N минут между операциями CREATE_ORDER / UPDATE_ORDER
}

// Проверка на этапе компиляции, что Server реализует все эндпоинты из OpenAPI.
var _ api.StrictServerInterface = (*Server)(nil)

func NewServer(db *pgxpool.Pool, jwtSecret []byte, orderRateLimit time.Duration) *Server {
	return &Server{db: db, jwtSecret: jwtSecret, orderRateLimit: orderRateLimit}
}

// Handler собирает цепочку обработки запроса:
//
//	logging → auth (JWT) → валидация по OpenAPI → сгенерированный роутер → наш хендлер
func (s *Server) Handler() (http.Handler, error) {
	validator, err := newValidator()
	if err != nil {
		return nil, err
	}

	// Ошибки разбора запроса, которые всё-таки дошли до сгенерированного кода, — тоже VALIDATION_ERROR.
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, errValidation("request validation failed", collectViolations(err, "")))
	}

	strict := api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		// Хендлер вернул error → пишем его в формате api.Error.
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, err)
		},
	})
	router := api.HandlerWithOptions(strict, api.StdHTTPServerOptions{
		BaseRouter:       http.NewServeMux(),
		ErrorHandlerFunc: badRequest,
	})

	return loggingMiddleware(s.authMiddleware(validator(router))), nil
}
