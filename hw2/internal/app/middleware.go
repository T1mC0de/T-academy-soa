package app

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// statusRecorder — обёртка над ResponseWriter, которая запоминает HTTP-код ответа.
// Без неё middleware не узнает, какой код вернул хендлер.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// loggingMiddleware пишет одну JSON-строку лога на каждый запрос.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Уникальный id запроса. Отдаём его клиенту в заголовке,
		// чтобы по нему можно было найти запрос в логах.
		requestID := uuid.NewString()
		w.Header().Set("X-Request-Id", requestID)

		// Для POST/PUT/DELETE логируем тело запроса.
		// Тело можно прочитать только один раз, поэтому после чтения кладём его обратно.
		var body string
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			body = string(raw)
		}

		// Выполняем запрос дальше по цепочке.
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// user_id берём из заголовка X-User-Id. Если его нет или это не UUID — null.
		var userID any
		if id, err := uuid.Parse(r.Header.Get("X-User-Id")); err == nil {
			userID = id.String()
		}

		slog.Info("request",
			"request_id", requestID,
			"method", r.Method,
			"endpoint", r.URL.Path,
			"status_code", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"user_id", userID,
			"request_body", body,
		)
	})
}
