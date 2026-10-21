package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"marketplace/internal/api"
)

// AppError — ожидаемая ошибка (бизнес-правило, валидация, доступ).
// Хендлеры возвращают её как обычный error, а writeError отдаёт клиенту
// JSON в едином формате {error_code, message, details} — это схема Error из OpenAPI.
type AppError struct {
	Status  int            // HTTP-код
	Code    api.ErrorCode  // error_code
	Message string         // message
	Details map[string]any // details (может быть nil)
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

// --- Готовые ошибки, которые встречаются в нескольких местах ---

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

func errNotOwner() *AppError {
	return newError(http.StatusForbidden, api.ORDEROWNERSHIPVIOLATION, "order belongs to another user")
}

func errInvalidTransition(current api.OrderStatus) *AppError {
	return newError(http.StatusConflict, api.INVALIDSTATETRANSITION, "operation is not allowed in status "+string(current))
}

func errPromoInvalid() *AppError {
	return newError(http.StatusUnprocessableEntity, api.PROMOCODEINVALID, "promo code not found, expired, exhausted or inactive")
}

func errPromoMinAmount() *AppError {
	return newError(http.StatusUnprocessableEntity, api.PROMOCODEMINAMOUNT, "order total is below promo code minimum")
}

// errValidation — 400 VALIDATION_ERROR, в details.errors список вида [{field, message}].
func errValidation(violations []map[string]string) *AppError {
	return newError(http.StatusBadRequest, api.VALIDATIONERROR, "request validation failed").
		withDetails(map[string]any{"errors": violations})
}

// writeError пишет ошибку клиенту.
// Если это не AppError, значит что-то сломалось (например, БД):
// подробности пишем только в лог, клиенту — 500 INTERNAL_ERROR.
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(appErr.Status)
	json.NewEncoder(w).Encode(body)
}
