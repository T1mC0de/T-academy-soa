package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"marketplace/internal/api"
)

// AppError — бизнес-ошибка. Хендлеры возвращают её как обычный error,
// а writeError превращает её в JSON единого формата (схема Error из OpenAPI).
type AppError struct {
	Status  int
	Code    api.ErrorCode
	Message string
	Details map[string]any
}

func (e *AppError) Error() string {
	return string(e.Code) + ": " + e.Message
}

func newError(status int, code api.ErrorCode, message string) *AppError {
	return &AppError{Status: status, Code: code, Message: message}
}

func (e *AppError) withDetails(details map[string]any) *AppError {
	e.Details = details
	return e
}

func errAccessDenied() *AppError {
	return newError(http.StatusForbidden, api.ACCESSDENIED, "insufficient permissions")
}

func errProductNotFound(id any) *AppError {
	return newError(http.StatusNotFound, api.PRODUCTNOTFOUND, "product not found").
		withDetails(map[string]any{"product_id": id})
}

func errOrderNotFound() *AppError {
	return newError(http.StatusNotFound, api.ORDERNOTFOUND, "order not found")
}

func errValidation(message string, violations []map[string]string) *AppError {
	return newError(http.StatusBadRequest, api.VALIDATIONERROR, message).
		withDetails(map[string]any{"errors": violations})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError пишет любую ошибку в формате api.Error.
// Всё, что не AppError, — неожиданная ошибка: логируем и отдаём 500 без подробностей.
func writeError(w http.ResponseWriter, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		slog.Error("internal error", "error", err.Error())
		appErr = newError(http.StatusInternalServerError, api.INTERNALERROR, "internal server error")
	}

	body := api.Error{ErrorCode: appErr.Code, Message: appErr.Message}
	if appErr.Details != nil {
		body.Details = &appErr.Details
	}
	writeJSON(w, appErr.Status, body)
}
