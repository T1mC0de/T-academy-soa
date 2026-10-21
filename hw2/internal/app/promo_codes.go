package app

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"marketplace/internal/api"
)

// Колонки промокода в том порядке, в котором их читает scanPromo.
const promoColumns = `id, code, discount_type, discount_value, min_order_amount, max_uses, current_uses, valid_from, valid_until, active`

func scanPromo(row scanner) (api.PromoCodeResponse, error) {
	var p api.PromoCodeResponse
	err := row.Scan(&p.Id, &p.Code, &p.DiscountType, &p.DiscountValue, &p.MinOrderAmount, &p.MaxUses, &p.CurrentUses, &p.ValidFrom, &p.ValidUntil, &p.Active)
	return p, err
}

// POST /promo-codes
func (s *Server) CreatePromoCode(ctx context.Context, req api.CreatePromoCodeRequestObject) (api.CreatePromoCodeResponseObject, error) {
	role := req.Params.XUserRole
	if role != api.SELLER && role != api.ADMIN {
		return nil, errAccessDenied()
	}

	// Проверки, которые нельзя описать в OpenAPI: они зависят от двух полей сразу.
	b := req.Body
	var violations []map[string]string
	if !b.ValidUntil.After(b.ValidFrom) {
		violations = append(violations, map[string]string{"field": "valid_until", "message": "must be after valid_from"})
	}
	if b.DiscountType == api.PERCENTAGE && b.DiscountValue.GreaterThan(decimal.NewFromInt(100)) {
		violations = append(violations, map[string]string{"field": "discount_value", "message": "percentage must be at most 100"})
	}
	if len(violations) > 0 {
		return nil, errValidation(violations)
	}

	// Код промокода должен быть уникальным.
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM promo_codes WHERE code = $1)`, b.Code).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, newError(http.StatusConflict, api.PROMOCODEALREADYEXISTS, "promo code already exists")
	}

	// active необязательное, по умолчанию true.
	active := true
	if b.Active != nil {
		active = *b.Active
	}

	promo, err := scanPromo(s.db.QueryRow(
		`INSERT INTO promo_codes (code, discount_type, discount_value, min_order_amount, max_uses, valid_from, valid_until, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+promoColumns,
		b.Code, b.DiscountType, b.DiscountValue, b.MinOrderAmount, b.MaxUses, b.ValidFrom, b.ValidUntil, active,
	))
	if err != nil {
		return nil, err
	}
	return api.CreatePromoCode201JSONResponse(promo), nil
}

// lockPromoByCode и lockPromoByID читают промокод и блокируют его строку (FOR UPDATE),
// чтобы два параллельных заказа не превысили max_uses.
func lockPromoByCode(tx *sql.Tx, code string) (api.PromoCodeResponse, error) {
	return scanPromo(tx.QueryRow(`SELECT `+promoColumns+` FROM promo_codes WHERE code = $1 FOR UPDATE`, code))
}

func lockPromoByID(tx *sql.Tx, id uuid.UUID) (api.PromoCodeResponse, error) {
	return scanPromo(tx.QueryRow(`SELECT `+promoColumns+` FROM promo_codes WHERE id = $1 FOR UPDATE`, id))
}

// promoActiveNow: промокод включён и сейчас в интервале [valid_from, valid_until].
func promoActiveNow(p api.PromoCodeResponse) bool {
	now := time.Now()
	started := !now.Before(p.ValidFrom)  // now >= valid_from
	notEnded := !now.After(p.ValidUntil) // now <= valid_until
	return p.Active && started && notEnded
}

// calcDiscount считает скидку по правилам задания.
func calcDiscount(p api.PromoCodeResponse, total decimal.Decimal) decimal.Decimal {
	// FIXED_AMOUNT: скидка = discount_value, но не больше суммы заказа.
	if p.DiscountType == api.FIXEDAMOUNT {
		return decimal.Min(p.DiscountValue, total)
	}

	// PERCENTAGE: скидка = total * discount_value / 100, округляем до копеек.
	discount := total.Mul(p.DiscountValue).Div(decimal.NewFromInt(100)).Round(2)

	// По заданию скидка не больше 70% от суммы, иначе скидка не применяется (цена остаётся 100%).
	maxDiscount := total.Mul(decimal.NewFromInt(70)).Div(decimal.NewFromInt(100))
	if discount.GreaterThan(maxDiscount) {
		return decimal.Zero
	}
	return discount
}
