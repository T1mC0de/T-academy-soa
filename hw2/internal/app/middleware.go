package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"marketplace/internal/api"
)

// requestInfo живёт в контексте запроса. Логирующий middleware создаёт его,
// а auth-middleware (который работает «глубже») записывает сюда user_id,
// чтобы он попал в лог.
type requestInfo struct {
	ID     string
	UserID string
}

type requestInfoKey struct{}

// Поля, которые нельзя писать в лог как есть.
var sensitiveFields = map[string]bool{"password": true, "refresh_token": true, "access_token": true}

// statusRecorder запоминает HTTP-код, который записал хендлер.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// loggingMiddleware логирует каждый запрос одной JSON-строкой.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{ID: uuid.NewString()}
		w.Header().Set("X-Request-Id", info.ID)

		// Для мутирующих запросов читаем тело, чтобы залогировать, и возвращаем его обратно в r.Body.
		var body any
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(raw))
			body = maskBody(raw)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info)))

		var userID any // nil → в JSON будет null
		if info.UserID != "" {
			userID = info.UserID
		}
		attrs := []any{
			"request_id", info.ID,
			"method", r.Method,
			"endpoint", r.URL.Path,
			"status_code", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"user_id", userID,
		}
		if body != nil {
			attrs = append(attrs, "request_body", body)
		}
		slog.Info("request", attrs...)
	})
}

// maskBody разбирает JSON-тело и заменяет чувствительные поля на "***".
func maskBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return string(raw)
	}
	if obj, ok := data.(map[string]any); ok {
		for key := range obj {
			if sensitiveFields[key] {
				obj[key] = "***"
			}
		}
	}
	return data
}

// authUser — текущий пользователь, достаётся из access token.
type authUser struct {
	ID   uuid.UUID
	Role api.Role
}

type authUserKey struct{}

func currentUser(ctx context.Context) authUser {
	user, _ := ctx.Value(authUserKey{}).(authUser)
	return user
}

// authMiddleware проверяет access token у всех запросов, кроме /auth/*.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			next.ServeHTTP(w, r)
			return
		}

		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, newError(http.StatusUnauthorized, api.TOKENINVALID, "missing bearer token"))
			return
		}

		user, err := s.parseToken(token, tokenTypeAccess)
		if errors.Is(err, jwt.ErrTokenExpired) {
			writeError(w, newError(http.StatusUnauthorized, api.TOKENEXPIRED, "access token expired"))
			return
		}
		if err != nil {
			writeError(w, newError(http.StatusUnauthorized, api.TOKENINVALID, "invalid access token"))
			return
		}

		if info, ok := r.Context().Value(requestInfoKey{}).(*requestInfo); ok {
			info.UserID = user.ID.String()
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authUserKey{}, user)))
	})
}
