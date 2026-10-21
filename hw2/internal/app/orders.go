package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"marketplace/internal/api"
)

// ---------------------------------------------------------------------------
// POST /orders — создание заказа
// ---------------------------------------------------------------------------

func (s *Server) CreateOrder(ctx context.Context, req api.CreateOrderRequestObject) (api.CreateOrderResponseObject, error) {
	userID := req.Params.XUserId
	role := req.Params.XUserRole
	if role == api.SELLER {
		return nil, errAccessDenied()
	}

	// Все шаги — в одной транзакции. Если выйдем из функции без Commit,
	// defer сделает Rollback, и в БД ничего не изменится.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 1. Ограничение частоты создания.
	if err := s.checkRateLimit(tx, userID, "CREATE_ORDER"); err != nil {
		return nil, err
	}

	// 2. У пользователя не должно быть активного заказа.
	var hasActive bool
	err = tx.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM orders WHERE user_id = $1 AND status IN ('CREATED', 'PAYMENT_PENDING'))`,
		userID,
	).Scan(&hasActive)
	if err != nil {
		return nil, err
	}
	if hasActive {
		return nil, newError(http.StatusConflict, api.ORDERHASACTIVE, "user already has an active order")
	}

	// 3–6. Проверка товаров и остатков, резервирование, снапшот цен.
	lines, total, err := reserveItems(tx, req.Body.Items)
	if err != nil {
		return nil, err
	}

	// 7. Промокод.
	discount := decimal.Zero
	var promoID *uuid.UUID // nil — промокода нет
	if req.Body.PromoCode != nil {
		promo, err := lockPromoByCode(tx, *req.Body.PromoCode)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errPromoInvalid()
		}
		if err != nil {
			return nil, err
		}
		if !promoActiveNow(promo) || promo.CurrentUses >= promo.MaxUses {
			return nil, errPromoInvalid()
		}
		if total.LessThan(promo.MinOrderAmount) {
			return nil, errPromoMinAmount()
		}
		discount = calcDiscount(promo, total)
		_, err = tx.Exec(`UPDATE promo_codes SET current_uses = current_uses + 1 WHERE id = $1`, promo.Id)
		if err != nil {
			return nil, err
		}
		promoID = &promo.Id
	}

	// Сохраняем заказ и его позиции.
	var orderID uuid.UUID
	err = tx.QueryRow(
		`INSERT INTO orders (user_id, status, promo_code_id, total_amount, discount_amount)
		 VALUES ($1, 'CREATED', $2, $3, $4)
		 RETURNING id`,
		userID, promoID, total.Sub(discount), discount,
	).Scan(&orderID)
	if err != nil {
		return nil, err
	}
	if err := insertItems(tx, orderID, lines); err != nil {
		return nil, err
	}

	// 8. Фиксация операции.
	_, err = tx.Exec(`INSERT INTO user_operations (user_id, operation_type) VALUES ($1, 'CREATE_ORDER')`, userID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	order, err := s.loadOrder(orderID)
	if err != nil {
		return nil, err
	}
	return api.CreateOrder201JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// GET /orders/{id}
// ---------------------------------------------------------------------------

func (s *Server) GetOrder(ctx context.Context, req api.GetOrderRequestObject) (api.GetOrderResponseObject, error) {
	userID := req.Params.XUserId
	role := req.Params.XUserRole
	if role == api.SELLER {
		return nil, errAccessDenied()
	}

	order, err := s.loadOrder(req.Id)
	if err != nil {
		return nil, err
	}
	// ADMIN видит любой заказ, USER — только свой.
	if role != api.ADMIN && order.UserId != userID {
		return nil, errNotOwner()
	}
	return api.GetOrder200JSONResponse(order), nil
}

// ---------------------------------------------------------------------------
// PUT /orders/{id} — замена позиций заказа
// ---------------------------------------------------------------------------

func (s *Server) UpdateOrder(ctx context.Context, req api.UpdateOrderRequestObject) (api.UpdateOrderResponseObject, error) {
	userID := req.Params.XUserId
	role := req.Params.XUserRole
	if role == api.SELLER {
		return nil, errAccessDenied()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	order, err := lockOrder(tx, req.Id)
	if err != nil {
		return nil, err
	}

	// 1. Проверка владельца (ADMIN может менять любой заказ).
	if role != api.ADMIN && order.userID != userID {
		return nil, errNotOwner()
	}

	// 2. Менять можно только заказ в статусе CREATED.
	if order.status != api.CREATED {
		return nil, errInvalidTransition(order.status)
	}

	// 3. Ограничение частоты обновления.
	if err := s.checkRateLimit(tx, userID, "UPDATE_ORDER"); err != nil {
		return nil, err
	}

	// 4. Возвращаем на склад товары из старых позиций и удаляем эти позиции.
	if err := releaseStock(tx, req.Id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM order_items WHERE order_id = $1`, req.Id); err != nil {
		return nil, err
	}

	// 5. Проверяем и резервируем новые позиции — так же, как при создании.
	lines, total, err := reserveItems(tx, req.Body.Items)
	if err != nil {
		return nil, err
	}

	// 6. Пересчёт скидки. Если промокод больше не подходит — снимаем его с заказа.
	discount := decimal.Zero
	promoID := order.promoCodeID
	if promoID != nil {
		promo, err := lockPromoByID(tx, *promoID)
		if err != nil {
			return nil, err
		}
		if promoActiveNow(promo) && total.GreaterThanOrEqual(promo.MinOrderAmount) {
			discount = calcDiscount(promo, total)
		} else {
			_, err = tx.Exec(`UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = $1`, promo.Id)
			if err != nil {
				return nil, err
			}
			promoID = nil
		}
	}

	_, err = tx.Exec(
		`UPDATE orders SET promo_code_id = $2, total_amount = $3, discount_amount = $4 WHERE id = $1`,
		req.Id, promoID, total.Sub(discount), discount,
	)
	if err != nil {
		return nil, err
	}
	if err := insertItems(tx, req.Id, lines); err != nil {
		return nil, err
	}

	// 7. Фиксация операции.
	_, err = tx.Exec(`INSERT INTO user_operations (user_id, operation_type) VALUES ($1, 'UPDATE_ORDER')`, userID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	updated, err := s.loadOrder(req.Id)
	if err != nil {
		return nil, err
	}
	return api.UpdateOrder200JSONResponse(updated), nil
}

// ---------------------------------------------------------------------------
// POST /orders/{id}/cancel — отмена заказа
// ---------------------------------------------------------------------------

func (s *Server) CancelOrder(ctx context.Context, req api.CancelOrderRequestObject) (api.CancelOrderResponseObject, error) {
	userID := req.Params.XUserId
	role := req.Params.XUserRole
	if role == api.SELLER {
		return nil, errAccessDenied()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	order, err := lockOrder(tx, req.Id)
	if err != nil {
		return nil, err
	}

	// 1. Проверка владельца.
	if role != api.ADMIN && order.userID != userID {
		return nil, errNotOwner()
	}

	// 2. Отменить можно только из CREATED или PAYMENT_PENDING.
	if order.status != api.CREATED && order.status != api.PAYMENTPENDING {
		return nil, errInvalidTransition(order.status)
	}

	// 3. Возвращаем товары на склад.
	if err := releaseStock(tx, req.Id); err != nil {
		return nil, err
	}

	// 4. Возвращаем использование промокода.
	if order.promoCodeID != nil {
		_, err = tx.Exec(`UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = $1`, *order.promoCodeID)
		if err != nil {
			return nil, err
		}
	}

	// 5. Меняем статус.
	if _, err := tx.Exec(`UPDATE orders SET status = 'CANCELED' WHERE id = $1`, req.Id); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	canceled, err := s.loadOrder(req.Id)
	if err != nil {
		return nil, err
	}
	return api.CancelOrder200JSONResponse(canceled), nil
}

// ---------------------------------------------------------------------------
// Вспомогательные функции
// ---------------------------------------------------------------------------

// orderLine — позиция заказа. price — цена товара на момент заказа.
type orderLine struct {
	productID uuid.UUID
	quantity  int32
	price     decimal.Decimal
}

// reserveItems — шаги 3–6 создания заказа.
// Возвращает позиции с зафиксированными ценами и сумму заказа без скидки.
func reserveItems(tx *sql.Tx, items []api.OrderItemRequest) ([]orderLine, decimal.Decimal, error) {
	// Если один товар указан дважды, складываем количество в одну позицию.
	var lines []orderLine
	for _, item := range items {
		found := false
		for i := range lines {
			if lines[i].productID == item.ProductId {
				lines[i].quantity += item.Quantity
				found = true
			}
		}
		if !found {
			lines = append(lines, orderLine{productID: item.ProductId, quantity: item.Quantity})
		}
	}

	// 3. Каждый товар существует и в статусе ACTIVE.
	// FOR UPDATE блокирует строку товара до конца транзакции:
	// параллельный заказ будет ждать и не спишет тот же остаток.
	stocks := make([]int32, len(lines)) // текущий остаток каждого товара
	for i := range lines {
		var status api.ProductStatus
		err := tx.QueryRow(`SELECT status, price, stock FROM products WHERE id = $1 FOR UPDATE`, lines[i].productID).
			Scan(&status, &lines[i].price, &stocks[i])
		if errors.Is(err, sql.ErrNoRows) {
			return nil, decimal.Zero, errProductNotFound(lines[i].productID)
		}
		if err != nil {
			return nil, decimal.Zero, err
		}
		if status != api.ACTIVE {
			return nil, decimal.Zero, newError(http.StatusConflict, api.PRODUCTINACTIVE, "product is not active").
				withDetails(map[string]any{"product_id": lines[i].productID})
		}
	}

	// 4. Хватает ли остатков. Собираем все товары, которых не хватает.
	var notEnough []map[string]any
	for i := range lines {
		if stocks[i] < lines[i].quantity {
			notEnough = append(notEnough, map[string]any{
				"product_id": lines[i].productID,
				"requested":  lines[i].quantity,
				"available":  stocks[i],
			})
		}
	}
	if len(notEnough) > 0 {
		return nil, decimal.Zero, newError(http.StatusConflict, api.INSUFFICIENTSTOCK, "insufficient stock").
			withDetails(map[string]any{"items": notEnough})
	}

	// 5. Резервируем остатки.
	// 6. Сумму считаем по ценам, прочитанным выше, — это и есть снапшот цены.
	total := decimal.Zero
	for _, line := range lines {
		_, err := tx.Exec(`UPDATE products SET stock = stock - $2 WHERE id = $1`, line.productID, line.quantity)
		if err != nil {
			return nil, decimal.Zero, err
		}
		total = total.Add(line.price.Mul(decimal.NewFromInt32(line.quantity)))
	}
	return lines, total, nil
}

// insertItems сохраняет позиции заказа вместе с ценой на момент заказа (price_at_order).
func insertItems(tx *sql.Tx, orderID uuid.UUID, lines []orderLine) error {
	for _, line := range lines {
		_, err := tx.Exec(
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
func releaseStock(tx *sql.Tx, orderID uuid.UUID) error {
	// Сначала читаем позиции заказа целиком...
	rows, err := tx.Query(`SELECT product_id, quantity FROM order_items WHERE order_id = $1`, orderID)
	if err != nil {
		return err
	}
	var lines []orderLine
	for rows.Next() {
		var line orderLine
		if err := rows.Scan(&line.productID, &line.quantity); err != nil {
			rows.Close()
			return err
		}
		lines = append(lines, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// ...потом возвращаем остатки по каждой.
	for _, line := range lines {
		_, err := tx.Exec(`UPDATE products SET stock = stock + $2 WHERE id = $1`, line.productID, line.quantity)
		if err != nil {
			return err
		}
	}
	return nil
}

// checkRateLimit: если последняя операция этого типа была меньше N минут назад — ORDER_LIMIT_EXCEEDED.
func (s *Server) checkRateLimit(tx *sql.Tx, userID uuid.UUID, operation string) error {
	var last time.Time
	err := tx.QueryRow(
		`SELECT created_at FROM user_operations
		 WHERE user_id = $1 AND operation_type = $2
		 ORDER BY created_at DESC LIMIT 1`,
		userID, operation,
	).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // операций ещё не было
	}
	if err != nil {
		return err
	}
	if time.Since(last) < s.orderRateLimit {
		return newError(http.StatusTooManyRequests, api.ORDERLIMITEXCEEDED, "too many order operations, try again later")
	}
	return nil
}

// orderState — то, что нужно знать о заказе перед его изменением.
type orderState struct {
	userID      uuid.UUID
	status      api.OrderStatus
	promoCodeID *uuid.UUID // nil — промокода нет
}

// lockOrder читает заказ и блокирует его строку до конца транзакции (FOR UPDATE).
func lockOrder(tx *sql.Tx, orderID uuid.UUID) (orderState, error) {
	var o orderState
	err := tx.QueryRow(`SELECT user_id, status, promo_code_id FROM orders WHERE id = $1 FOR UPDATE`, orderID).
		Scan(&o.userID, &o.status, &o.promoCodeID)
	if errors.Is(err, sql.ErrNoRows) {
		return o, errOrderNotFound()
	}
	return o, err
}

// loadOrder читает заказ вместе с позициями — для ответа клиенту.
func (s *Server) loadOrder(orderID uuid.UUID) (api.OrderResponse, error) {
	var o api.OrderResponse
	err := s.db.QueryRow(
		`SELECT id, user_id, status, promo_code_id, total_amount, discount_amount, created_at, updated_at
		 FROM orders WHERE id = $1`,
		orderID,
	).Scan(&o.Id, &o.UserId, &o.Status, &o.PromoCodeId, &o.TotalAmount, &o.DiscountAmount, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return o, errOrderNotFound()
	}
	if err != nil {
		return o, err
	}

	rows, err := s.db.Query(
		`SELECT id, product_id, quantity, price_at_order FROM order_items WHERE order_id = $1`,
		orderID,
	)
	if err != nil {
		return o, err
	}
	defer rows.Close()

	o.Items = []api.OrderItemResponse{}
	for rows.Next() {
		var item api.OrderItemResponse
		if err := rows.Scan(&item.Id, &item.ProductId, &item.Quantity, &item.PriceAtOrder); err != nil {
			return o, err
		}
		o.Items = append(o.Items, item)
	}
	return o, rows.Err()
}
