package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"marketplace/internal/api"
)

const (
	opCreateOrder = "CREATE_ORDER"
	opUpdateOrder = "UPDATE_ORDER"
)

// Разрешённые переходы статусов заказа:
//
//	CREATED → PAYMENT_PENDING → PAID → SHIPPED → COMPLETED
//	CREATED, PAYMENT_PENDING → CANCELED
var orderTransitions = map[api.OrderStatus][]api.OrderStatus{
	api.CREATED:        {api.PAYMENTPENDING, api.CANCELED},
	api.PAYMENTPENDING: {api.PAID, api.CANCELED},
	api.PAID:           {api.SHIPPED},
	api.SHIPPED:        {api.COMPLETED},
}

func canTransition(from, to api.OrderStatus) bool {
	return slices.Contains(orderTransitions[from], to)
}

func errInvalidTransition(from api.OrderStatus, action string) *AppError {
	return newError(http.StatusConflict, api.INVALIDSTATETRANSITION, "cannot "+action+" order in status "+string(from)).
		withDetails(map[string]any{"current_status": from})
}

func errPromoInvalid() *AppError {
	return newError(http.StatusUnprocessableEntity, api.PROMOCODEINVALID, "promo code not found, expired, exhausted or inactive")
}

// ---------------------------------------------------------------------------
// POST /orders
// ---------------------------------------------------------------------------

func (s *Server) CreateOrder(ctx context.Context, req api.CreateOrderRequestObject) (api.CreateOrderResponseObject, error) {
	user := currentUser(ctx)
	if user.Role == api.RoleSELLER {
		return nil, errAccessDenied()
	}

	var orderID uuid.UUID
	// Всё внутри BeginFunc — одна транзакция: при любой ошибке будет ROLLBACK, иначе COMMIT.
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, user.ID); err != nil {
			return err
		}

		// 1. Ограничение частоты создания.
		if err := s.checkRateLimit(ctx, tx, user.ID, opCreateOrder); err != nil {
			return err
		}

		// 2. Нет активного заказа (CREATED / PAYMENT_PENDING).
		var hasActive bool
		err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM orders WHERE user_id = $1 AND status IN ('CREATED', 'PAYMENT_PENDING'))`,
			user.ID,
		).Scan(&hasActive)
		if err != nil {
			return err
		}
		if hasActive {
			return newError(http.StatusConflict, api.ORDERHASACTIVE, "user already has an active order")
		}

		// 3–6. Проверка каталога и остатков, резервирование, снапшот цен.
		lines, total, err := reserveItems(ctx, tx, req.Body.Items)
		if err != nil {
			return err
		}

		// 7. Промокод и скидка.
		discount := decimal.Zero
		var promoID *uuid.UUID
		if req.Body.PromoCode != nil {
			promo, err := lockPromoByCode(ctx, tx, *req.Body.PromoCode)
			if errors.Is(err, pgx.ErrNoRows) {
				return errPromoInvalid()
			}
			if err != nil {
				return err
			}
			if !promoActiveNow(promo) || promo.CurrentUses >= promo.MaxUses {
				return errPromoInvalid()
			}
			if total.LessThan(promo.MinOrderAmount) {
				return newError(http.StatusUnprocessableEntity, api.PROMOCODEMINAMOUNT, "order total is below promo code minimum").
					withDetails(map[string]any{"min_order_amount": promo.MinOrderAmount, "order_amount": total})
			}
			discount = calcDiscount(promo, total)
			if _, err := tx.Exec(ctx, `UPDATE promo_codes SET current_uses = current_uses + 1 WHERE id = $1`, promo.Id); err != nil {
				return err
			}
			promoID = &promo.Id
		}

		err = tx.QueryRow(ctx,
			`INSERT INTO orders (user_id, status, promo_code_id, total_amount, discount_amount)
			 VALUES ($1, 'CREATED', $2, $3, $4)
			 RETURNING id`,
			user.ID, promoID, total.Sub(discount), discount,
		).Scan(&orderID)
		if err != nil {
			return err
		}
		if err := insertItems(ctx, tx, orderID, lines); err != nil {
			return err
		}

		// 8. Фиксация операции.
		return recordOperation(ctx, tx, user.ID, opCreateOrder)
	})
	if err != nil {
		return nil, err
	}

	order, err := s.loadOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return api.CreateOrder201JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// GET /orders/{id}
// ---------------------------------------------------------------------------

func (s *Server) GetOrder(ctx context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	user := currentUser(ctx)
	if user.Role == api.RoleSELLER {
		return nil, errAccessDenied()
	}

	order, err := s.loadOrder(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if err := checkOrderOwner(user, order.UserId); err != nil {
		return nil, err
	}
	return api.GetOrder200JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// PUT /orders/{id}
// ---------------------------------------------------------------------------

func (s *Server) UpdateOrder(ctx context.Context, req api.UpdateOrderRequestObject) (api.UpdateOrderResponseObject, error) {
	user := currentUser(ctx)
	if user.Role == api.RoleSELLER {
		return nil, errAccessDenied()
	}

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockUser(ctx, tx, user.ID); err != nil {
			return err
		}
		order, err := lockOrder(ctx, tx, req.Id)
		if err != nil {
			return err
		}

		// 1. Проверка владельца.
		if err := checkOrderOwner(user, order.userID); err != nil {
			return err
		}

		// 2. Обновлять можно только заказ в статусе CREATED.
		if order.status != api.CREATED {
			return errInvalidTransition(order.status, "update")
		}

		// 3. Ограничение частоты обновления.
		if err := s.checkRateLimit(ctx, tx, user.ID, opUpdateOrder); err != nil {
			return err
		}

		// 4. Возвращаем остатки по старым позициям и удаляем их.
		if err := releaseStock(ctx, tx, req.Id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM order_items WHERE order_id = $1`, req.Id); err != nil {
			return err
		}

		// 5. Проверяем и резервируем новые позиции (как при создании).
		lines, total, err := reserveItems(ctx, tx, req.Body.Items)
		if err != nil {
			return err
		}

		// 6. Пересчёт стоимости. Если промокод больше не применим — снимаем его и возвращаем использование.
		discount := decimal.Zero
		promoID := order.promoCodeID
		if promoID != nil {
			promo, err := lockPromoByID(ctx, tx, *promoID)
			if err != nil {
				return err
			}
			if promoActiveNow(promo) && total.GreaterThanOrEqual(promo.MinOrderAmount) {
				discount = calcDiscount(promo, total)
			} else {
				if _, err := tx.Exec(ctx, `UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = $1`, promo.Id); err != nil {
					return err
				}
				promoID = nil
			}
		}

		_, err = tx.Exec(ctx,
			`UPDATE orders SET promo_code_id = $2, total_amount = $3, discount_amount = $4 WHERE id = $1`,
			req.Id, promoID, total.Sub(discount), discount,
		)
		if err != nil {
			return err
		}
		if err := insertItems(ctx, tx, req.Id, lines); err != nil {
			return err
		}

		// 7. Фиксация операции.
		return recordOperation(ctx, tx, user.ID, opUpdateOrder)
	})
	if err != nil {
		return nil, err
	}

	order, err := s.loadOrder(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return api.UpdateOrder200JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/cancel
// ---------------------------------------------------------------------------

func (s *Server) CancelOrder(ctx context.Context, req api.CancelOrderRequestObject) (api.CancelOrderResponseObject, error) {
	user := currentUser(ctx)
	if user.Role == api.RoleSELLER {
		return nil, errAccessDenied()
	}

	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		order, err := lockOrder(ctx, tx, req.Id)
		if err != nil {
			return err
		}

		// 1. Проверка владельца.
		if err := checkOrderOwner(user, order.userID); err != nil {
			return err
		}

		// 2. Отмена разрешена только из CREATED / PAYMENT_PENDING.
		if !canTransition(order.status, api.CANCELED) {
			return errInvalidTransition(order.status, "cancel")
		}

		// 3. Возврат остатков.
		if err := releaseStock(ctx, tx, req.Id); err != nil {
			return err
		}

		// 4. Возврат использования промокода.
		if order.promoCodeID != nil {
			if _, err := tx.Exec(ctx, `UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = $1`, *order.promoCodeID); err != nil {
				return err
			}
		}

		// 5. Статус CANCELED.
		_, err = tx.Exec(ctx, `UPDATE orders SET status = 'CANCELED' WHERE id = $1`, req.Id)
		return err
	})
	if err != nil {
		return nil, err
	}

	order, err := s.loadOrder(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return api.CancelOrder200JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// Вспомогательные функции
// ---------------------------------------------------------------------------

// orderLine — позиция заказа после проверки: цена зафиксирована на момент заказа.
type orderLine struct {
	productID uuid.UUID
	quantity  int32
	price     decimal.Decimal
	available int32
}

// reserveItems выполняет шаги 3–6 создания заказа: товар существует и ACTIVE,
// хватает остатка, списывает stock, фиксирует цену. Возвращает позиции и сумму без скидки.
func reserveItems(ctx context.Context, tx pgx.Tx, items []api.OrderItemRequest) ([]orderLine, decimal.Decimal, error) {
	// Одинаковые product_id объединяем в одну позицию, чтобы остаток проверялся по суммарному количеству.
	quantities := map[uuid.UUID]int32{}
	for _, item := range items {
		quantities[item.ProductId] += item.Quantity
	}
	// Блокируем строки товаров всегда в одном порядке (по id), чтобы параллельные заказы не словили deadlock.
	ids := slices.SortedFunc(maps.Keys(quantities), func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	// 3. Каталог: товар существует и ACTIVE. FOR UPDATE держит блокировку строки до конца транзакции.
	lines := make([]orderLine, 0, len(ids))
	for _, id := range ids {
		line := orderLine{productID: id, quantity: quantities[id]}
		var status api.ProductStatus
		err := tx.QueryRow(ctx, `SELECT status, price, stock FROM products WHERE id = $1 FOR UPDATE`, id).
			Scan(&status, &line.price, &line.available)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, decimal.Zero, errProductNotFound(id)
		}
		if err != nil {
			return nil, decimal.Zero, err
		}
		if status != api.ACTIVE {
			return nil, decimal.Zero, newError(http.StatusConflict, api.PRODUCTINACTIVE, "product is not active").
				withDetails(map[string]any{"product_id": id})
		}
		lines = append(lines, line)
	}

	// 4. Остатки: собираем все товары, которых не хватает.
	var insufficient []map[string]any
	for _, line := range lines {
		if line.available < line.quantity {
			insufficient = append(insufficient, map[string]any{
				"product_id": line.productID,
				"requested":  line.quantity,
				"available":  line.available,
			})
		}
	}
	if len(insufficient) > 0 {
		return nil, decimal.Zero, newError(http.StatusConflict, api.INSUFFICIENTSTOCK, "insufficient stock").
			withDetails(map[string]any{"items": insufficient})
	}

	// 5. Резервирование. 6. Цена берётся из products и сохраняется в позиции (снапшот), по ней считаем сумму.
	total := decimal.Zero
	for _, line := range lines {
		if _, err := tx.Exec(ctx, `UPDATE products SET stock = stock - $2 WHERE id = $1`, line.productID, line.quantity); err != nil {
			return nil, decimal.Zero, err
		}
		total = total.Add(line.price.Mul(decimal.NewFromInt32(line.quantity)))
	}
	return lines, total, nil
}

func insertItems(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, lines []orderLine) error {
	for _, line := range lines {
		_, err := tx.Exec(ctx,
			`INSERT INTO order_items (order_id, product_id, quantity, price_at_order) VALUES ($1, $2, $3, $4)`,
			orderID, line.productID, line.quantity, line.price,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// releaseStock возвращает на склад количество по всем позициям заказа.
func releaseStock(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		`UPDATE products p SET stock = p.stock + oi.quantity
		 FROM order_items oi
		 WHERE oi.order_id = $1 AND oi.product_id = p.id`,
		orderID,
	)
	return err
}

// lockUser блокирует строку пользователя: его операции с заказами выполняются строго по очереди,
// поэтому два параллельных запроса не обойдут rate limit и проверку активного заказа.
func lockUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, userID)
	return err
}

// checkRateLimit: если последняя операция этого типа была меньше N минут назад — ORDER_LIMIT_EXCEEDED.
func (s *Server) checkRateLimit(ctx context.Context, tx pgx.Tx, userID uuid.UUID, operation string) error {
	var last time.Time
	err := tx.QueryRow(ctx,
		`SELECT created_at FROM user_operations
		 WHERE user_id = $1 AND operation_type = $2
		 ORDER BY created_at DESC LIMIT 1`,
		userID, operation,
	).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	if wait := s.orderRateLimit - time.Since(last); wait > 0 {
		return newError(http.StatusTooManyRequests, api.ORDERLIMITEXCEEDED, "too many order operations, try again later").
			withDetails(map[string]any{"retry_after_seconds": int(wait.Seconds()) + 1})
	}
	return nil
}

func recordOperation(ctx context.Context, tx pgx.Tx, userID uuid.UUID, operation string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_operations (user_id, operation_type) VALUES ($1, $2)`, userID, operation)
	return err
}

// checkOrderOwner: ADMIN видит любые заказы, остальные — только свои.
func checkOrderOwner(user authUser, ownerID uuid.UUID) error {
	if user.Role != api.RoleADMIN && ownerID != user.ID {
		return newError(http.StatusForbidden, api.ORDEROWNERSHIPVIOLATION, "order belongs to another user")
	}
	return nil
}

// orderState — то, что нужно знать о заказе для проверок перед изменением.
type orderState struct {
	userID      uuid.UUID
	status      api.OrderStatus
	promoCodeID *uuid.UUID
}

// lockOrder читает заказ с блокировкой строки до конца транзакции.
func lockOrder(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) (orderState, error) {
	var o orderState
	err := tx.QueryRow(ctx, `SELECT user_id, status, promo_code_id FROM orders WHERE id = $1 FOR UPDATE`, orderID).
		Scan(&o.userID, &o.status, &o.promoCodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, errOrderNotFound()
	}
	return o, err
}

func (s *Server) loadOrder(ctx context.Context, orderID uuid.UUID) (api.OrderResponse, error) {
	var o api.OrderResponse
	err := s.db.QueryRow(ctx,
		`SELECT id, user_id, status, promo_code_id, total_amount, discount_amount, created_at, updated_at
		 FROM orders WHERE id = $1`,
		orderID,
	).Scan(&o.Id, &o.UserId, &o.Status, &o.PromoCodeId, &o.TotalAmount, &o.DiscountAmount, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, errOrderNotFound()
	}
	if err != nil {
		return o, err
	}

	rows, err := s.db.Query(ctx,
		`SELECT id, product_id, quantity, price_at_order FROM order_items WHERE order_id = $1 ORDER BY product_id`,
		orderID,
	)
	if err != nil {
		return o, err
	}
	o.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (api.OrderItemResponse, error) {
		var item api.OrderItemResponse
		err := row.Scan(&item.Id, &item.ProductId, &item.Quantity, &item.PriceAtOrder)
		return item, err
	})
	return o, err
}
