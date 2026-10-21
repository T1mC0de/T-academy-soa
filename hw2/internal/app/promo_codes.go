package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"marketplace/internal/api"
)

const promoColumns = `id, code, discount_type, discount_value, min_order_amount, max_uses, current_uses, valid_from, valid_until, active`

func scanPromo(row pgx.Row) (api.PromoCodeResponse, error) {
	var p api.PromoCodeResponse
	err := row.Scan(&p.Id, &p.Code, &p.DiscountType, &p.DiscountValue, &p.MinOrderAmount, &p.MaxUses, &p.CurrentUses, &p.ValidFrom, &p.ValidUntil, &p.Active)
	return p, err
}

func (s *Server) CreatePromoCode(ctx context.Context, req api.CreatePromoCodeRequestObject) (api.CreatePromoCodeResponseObject, error) {
	user := currentUser(ctx)
	if user.Role != api.RoleSELLER && user.Role != api.RoleADMIN {
		return nil, errAccessDenied()
	}

	// Ограничения, которые нельзя выразить в OpenAPI (зависят от нескольких полей).
	b := req.Body
	var violations []map[string]string
	if !b.ValidUntil.After(b.ValidFrom) {
		violations = append(violations, map[string]string{"field": "valid_until", "message": "must be after valid_from"})
	}
	if b.DiscountType == api.PERCENTAGE && b.DiscountValue.GreaterThan(decimal.NewFromInt(100)) {
		violations = append(violations, map[string]string{"field": "discount_value", "message": "percentage must be at most 100"})
	}
	if len(violations) > 0 {
		return nil, errValidation("request validation failed", violations)
	}

	active := true
	if b.Active != nil {
		active = *b.Active
	}

	promo, err := scanPromo(s.db.QueryRow(ctx,
		`INSERT INTO promo_codes (code, discount_type, discount_value, min_order_amount, max_uses, valid_from, valid_until, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+promoColumns,
		b.Code, b.DiscountType, b.DiscountValue, b.MinOrderAmount, b.MaxUses, b.ValidFrom, b.ValidUntil, active,
	))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return nil, newError(http.StatusConflict, api.PROMOCODEALREADYEXISTS, "promo code already exists")
	}
	if err != nil {
		return nil, err
	}
	return api.CreatePromoCode201JSONResponse(promo), nil
}

// lockPromoByCode / lockPromoByID читают промокод с блокировкой строки (FOR UPDATE),
// чтобы два параллельных заказа не «перерасходовали» max_uses.
func lockPromoByCode(ctx context.Context, tx pgx.Tx, code string) (api.PromoCodeResponse, error) {
	return scanPromo(tx.QueryRow(ctx, `SELECT `+promoColumns+` FROM promo_codes WHERE code = $1 FOR UPDATE`, code))
}

func lockPromoByID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (api.PromoCodeResponse, error) {
	return scanPromo(tx.QueryRow(ctx, `SELECT `+promoColumns+` FROM promo_codes WHERE id = $1 FOR UPDATE`, id))
}

// promoActiveNow: активен и текущее время в [valid_from, valid_until].
func promoActiveNow(p api.PromoCodeResponse) bool {
	now := time.Now()
	return p.Active && !now.Before(p.ValidFrom) && !now.After(p.ValidUntil)
}

// calcDiscount считает скидку по правилам задания.
func calcDiscount(p api.PromoCodeResponse, total decimal.Decimal) decimal.Decimal {
	if p.DiscountType == api.FIXEDAMOUNT {
		return decimal.Min(p.DiscountValue, total)
	}

	// PERCENTAGE: total * value / 100, округляем до копеек.
	discount := total.Mul(p.DiscountValue).Div(decimal.NewFromInt(100)).Round(2)
	// По заданию: скидка не более 70% от суммы, иначе скидка не применяется (цена остаётся 100%).
	if discount.GreaterThan(total.Mul(decimal.RequireFromString("0.7"))) {
		return decimal.Zero
	}
	return discount
}
