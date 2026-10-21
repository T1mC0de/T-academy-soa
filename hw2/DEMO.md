# Сценарий демонстрации

Нужно два терминала:

- **Терминал 1** — приложение и его логи.
- **Терминал 2** — запросы `curl`. SQL выполняется в `make psql` (можно в третьем терминале).

Ожидаемые числа (остатки, суммы) посчитаны для прохождения шагов строго по порядку.

---

## 0. Подготовка

**Терминал 1** — поднимаем систему с чистой БД:

```bash
cd hw2
make down        # удалить старые контейнеры и данные
make up          # docker compose up --build
```

Что показать в логах: сборка образа (в ней выполняется `go generate`, то есть кодогенерация из OpenAPI), строки goose о применении миграции, `server started`.

**Терминал 2** — один раз задаём переменные и короткую команду `c`:

```bash
API=localhost:8080
USER_ID=11111111-1111-1111-1111-111111111111
USER2_ID=22222222-2222-2222-2222-222222222222
SELLER_ID=33333333-3333-3333-3333-333333333333
SELLER2_ID=44444444-4444-4444-4444-444444444444
ADMIN_ID=55555555-5555-5555-5555-555555555555

alias c='curl -s -w "\n→ HTTP %{http_code}\n\n" -H "Content-Type: application/json"'
```

`c` — это обычный `curl`, который дописывает HTTP-код ответа.

Регистрации нет: кто делает запрос, передаётся заголовками `X-User-Id` и `X-User-Role`.

**SQL** — открыть консоль БД:

```bash
make psql
```

```sql
\dt                                   -- таблицы
\d products                           -- видно индекс idx_products_status и триггер updated_at
SELECT * FROM goose_db_version;       -- применённые миграции
```

---

## 1. Создание товаров (SELLER)

```bash
c -X POST $API/products -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Phone","description":"Smartphone","price":100.00,"stock":10,"category":"electronics","status":"ACTIVE"}'
```

→ `201`. Скопировать `id` из ответа:

```bash
P1=<id из ответа>
```

Аналогично создать ещё два товара:

```bash
c -X POST $API/products -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Case","price":50,"stock":5,"category":"accessories","status":"ACTIVE"}'
P2=<id из ответа>

c -X POST $API/products -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Old phone","price":10,"stock":100,"category":"electronics","status":"INACTIVE"}'
P3=<id из ответа>
```

```sql
SELECT id, name, price, stock, status, seller_id, created_at, updated_at FROM products;
```

`created_at` и `updated_at` заполнились сами, `seller_id` = id продавца из заголовка.

---

## 2. Валидация (400 VALIDATION_ERROR)

Все поля неверные — в `details.errors` перечислены все нарушения сразу:

```bash
c -X POST $API/products -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"","price":0,"stock":-1,"category":"","status":"DELETED"}'
```

Нет заголовков пользователя:

```bash
c -X POST $API/products -d '{"name":"X","price":1,"stock":1,"category":"x","status":"ACTIVE"}'
```

`X-User-Id` не UUID, роли нет в enum:

```bash
c -X POST $API/products -H "X-User-Id: abc" -H "X-User-Role: HACKER" \
  -d '{"name":"X","price":1,"stock":1,"category":"x","status":"ACTIVE"}'
```

Неверная пагинация:

```bash
c "$API/products?page=-1&size=1000"
```

---

## 3. Чтение товаров

Читать товары можно без заголовков пользователя.

```bash
c $API/products/$P1                                             # 200
c $API/products/00000000-0000-0000-0000-000000000000            # 404 PRODUCT_NOT_FOUND
c "$API/products?page=0&size=2"                                 # totalElements = 3, в items 2 товара
c "$API/products?category=electronics&status=ACTIVE"            # только Phone
```

---

## 4. Роли для товаров

USER не может создавать товары:

```bash
c -X POST $API/products -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d '{"name":"X","price":1,"stock":1,"category":"x","status":"ACTIVE"}'
```

→ `403 ACCESS_DENIED`

Другой продавец не может менять чужой товар:

```bash
c -X PUT $API/products/$P1 -H "X-User-Id: $SELLER2_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Stolen","price":1,"stock":1,"category":"x","status":"ACTIVE"}'
```

→ `403 ACCESS_DENIED`

ADMIN может менять любой товар. PUT — это полная замена, поэтому передаются все поля:

```bash
c -X PUT $API/products/$P1 -H "X-User-Id: $ADMIN_ID" -H "X-User-Role: ADMIN" \
  -d '{"name":"Phone","description":"Smartphone v2","price":100,"stock":10,"category":"electronics","status":"ACTIVE"}'
```

→ `200`

```sql
SELECT name, description, created_at, updated_at FROM products WHERE name = 'Phone';
```

`updated_at` стал больше `created_at` — сработал триггер.

---

## 5. Промокоды

SELLER создаёт два промокода:

```bash
# 10%, минимальная сумма заказа 100
c -X POST $API/promo-codes -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"code":"SAVE10","discount_type":"PERCENTAGE","discount_value":10,"min_order_amount":100,"max_uses":5,"valid_from":"2020-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"}'

# срок действия истёк
c -X POST $API/promo-codes -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"code":"OLD_CODE","discount_type":"FIXED_AMOUNT","discount_value":10,"min_order_amount":0,"max_uses":5,"valid_from":"2020-01-01T00:00:00Z","valid_until":"2021-01-01T00:00:00Z"}'
```

Ошибки:

```bash
# USER → 403 ACCESS_DENIED
c -X POST $API/promo-codes -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d '{"code":"NOPE","discount_type":"FIXED_AMOUNT","discount_value":10,"min_order_amount":0,"max_uses":5,"valid_from":"2020-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"}'

# Повторный код → 409 PROMO_CODE_ALREADY_EXISTS
c -X POST $API/promo-codes -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"code":"SAVE10","discount_type":"PERCENTAGE","discount_value":5,"min_order_amount":0,"max_uses":1,"valid_from":"2020-01-01T00:00:00Z","valid_until":"2099-01-01T00:00:00Z"}'

# valid_until раньше valid_from, процент > 100 → 400 (эти проверки сделаны в коде, в OpenAPI их не выразить)
c -X POST $API/promo-codes -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"code":"BAD_DATES","discount_type":"PERCENTAGE","discount_value":150,"min_order_amount":0,"max_uses":1,"valid_from":"2030-01-01T00:00:00Z","valid_until":"2020-01-01T00:00:00Z"}'
```

---

## 6. Создание заказа: ошибки

Ни один из этих запросов не создаёт заказ.

```bash
# SELLER не может заказывать → 403 ACCESS_DENIED
c -X POST $API/orders -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"

# Товара нет → 404 PRODUCT_NOT_FOUND
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d '{"items":[{"product_id":"00000000-0000-0000-0000-000000000000","quantity":1}]}'

# Товар INACTIVE → 409 PRODUCT_INACTIVE
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P3\",\"quantity\":1}]}"

# Не хватает остатков → 409 INSUFFICIENT_STOCK, в details оба товара: requested и available
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":500},{\"product_id\":\"$P2\",\"quantity\":6}]}"

# Промокода нет → 422 PROMO_CODE_INVALID
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}],\"promo_code\":\"NO_SUCH_CODE\"}"

# Промокод истёк → 422 PROMO_CODE_INVALID
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}],\"promo_code\":\"OLD_CODE\"}"

# Сумма 50 < 100 → 422 PROMO_CODE_MIN_AMOUNT
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}],\"promo_code\":\"SAVE10\"}"

# Пустой список и неверный формат промокода → 400 VALIDATION_ERROR
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d '{"items":[],"promo_code":"bad code"}'
```

```sql
SELECT count(*) FROM orders;            -- 0
SELECT count(*) FROM user_operations;   -- 0: при ошибке транзакция откатилась целиком
SELECT name, stock FROM products;       -- остатки не изменились
```

---

## 7. Успешный заказ с промокодом

Заказ: 2 × Phone (100) + 1 × Case (50) = 250. Скидка 10% = 25. Итого 225.

```bash
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":2},{\"product_id\":\"$P2\",\"quantity\":1}],\"promo_code\":\"SAVE10\"}"
ORDER=<id из ответа>
```

→ `201`, `status: CREATED`, `total_amount: 225`, `discount_amount: 25`.

```sql
SELECT name, stock FROM products WHERE status = 'ACTIVE';          -- Phone 8, Case 4 (зарезервировано)
SELECT code, current_uses FROM promo_codes;                        -- SAVE10: 1
SELECT * FROM orders;
SELECT * FROM order_items;                                         -- price_at_order = цена на момент заказа
SELECT * FROM user_operations;                                     -- CREATE_ORDER
```

---

## 8. Rate limit и активный заказ

Сразу повторяем создание заказа:

```bash
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
```

→ `429 ORDER_LIMIT_EXCEEDED` (с прошлого заказа прошло меньше N = 1 минуты).

Подождать минуту или «состарить» операции в БД:

```sql
UPDATE user_operations SET created_at = created_at - interval '1 hour';
```

Тот же запрос ещё раз → `409 ORDER_HAS_ACTIVE`: у пользователя уже есть заказ в статусе `CREATED`.

---

## 9. Доступ к заказу

```bash
c $API/orders/$ORDER -H "X-User-Id: $USER2_ID"  -H "X-User-Role: USER"     # 403 ORDER_OWNERSHIP_VIOLATION
c $API/orders/$ORDER -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER"   # 403 ACCESS_DENIED
c $API/orders/00000000-0000-0000-0000-000000000000 -H "X-User-Id: $ADMIN_ID" -H "X-User-Role: ADMIN"   # 404 ORDER_NOT_FOUND
c $API/orders/$ORDER -H "X-User-Id: $ADMIN_ID"  -H "X-User-Role: ADMIN"    # 200: админ видит любой заказ
c $API/orders/$ORDER -H "X-User-Id: $USER_ID"   -H "X-User-Role: USER"     # 200: свой заказ
```

---

## 10. Снапшот цены

Меняем цену Phone на 999.99. Остаток сейчас 8, его тоже нужно передать, потому что PUT заменяет все поля:

```bash
c -X PUT $API/products/$P1 -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Phone","price":999.99,"stock":8,"category":"electronics","status":"ACTIVE"}'

c $API/orders/$ORDER -H "X-User-Id: $USER_ID" -H "X-User-Role: USER"
```

В заказе `price_at_order` по-прежнему 100, `total_amount` по-прежнему 225.

Вернуть цену обратно:

```bash
c -X PUT $API/products/$P1 -H "X-User-Id: $SELLER_ID" -H "X-User-Role: SELLER" \
  -d '{"name":"Phone","price":100,"stock":8,"category":"electronics","status":"ACTIVE"}'
```

---

## 11. Обновление заказа

Чужой заказ → `403 ORDER_OWNERSHIP_VIOLATION`:

```bash
c -X PUT $API/orders/$ORDER -H "X-User-Id: $USER2_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
```

**Новый состав: 1 × Phone = 100.** Промокод ещё применим: 100 ≥ 100, скидка 10, итого 90.

```bash
c -X PUT $API/orders/$ORDER -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
```

```sql
SELECT name, stock FROM products WHERE status = 'ACTIVE';   -- Phone 9, Case 5: старые позиции вернулись, новая зарезервирована
SELECT * FROM order_items;
```

Сразу ещё одно обновление → `429 ORDER_LIMIT_EXCEEDED` (лимит на `UPDATE_ORDER`):

```bash
c -X PUT $API/orders/$ORDER -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}]}"
```

Сбросить лимит:

```sql
UPDATE user_operations SET created_at = created_at - interval '1 hour';
```

**Новый состав: 1 × Case = 50.** Это меньше минимума 100, поэтому промокод снимается.

```bash
c -X PUT $API/orders/$ORDER -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}]}"
```

→ `promo_code_id: null`, `discount_amount: 0`, `total_amount: 50`.

```sql
SELECT code, current_uses FROM promo_codes;   -- SAVE10: 0 (использование вернулось)
```

---

## 12. Отмена заказа

```bash
# Чужой → 403 ORDER_OWNERSHIP_VIOLATION
c -X POST $API/orders/$ORDER/cancel -H "X-User-Id: $USER2_ID" -H "X-User-Role: USER"

# Свой → 200, status CANCELED
c -X POST $API/orders/$ORDER/cancel -H "X-User-Id: $USER_ID" -H "X-User-Role: USER"
```

```sql
SELECT name, stock FROM products WHERE status = 'ACTIVE';   -- Phone 10, Case 5: остатки вернулись
```

Из `CANCELED` переходов нет:

```bash
c -X POST $API/orders/$ORDER/cancel -H "X-User-Id: $USER_ID" -H "X-User-Role: USER"   # 409 INVALID_STATE_TRANSITION
c -X PUT $API/orders/$ORDER -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"                           # 409 INVALID_STATE_TRANSITION
```

---

## 13. Заказ в статусе PAID

API оплаты нет, поэтому статус ставим через SQL.

```sql
UPDATE user_operations SET created_at = created_at - interval '1 hour';
```

```bash
c -X POST $API/orders -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
ORDER2=<id из ответа>
```

```sql
UPDATE orders SET status = 'PAID' WHERE id = '<ORDER2>';
```

```bash
c -X POST $API/orders/$ORDER2/cancel -H "X-User-Id: $ADMIN_ID" -H "X-User-Role: ADMIN"   # 409: из PAID отменить нельзя
c -X PUT $API/orders/$ORDER2 -H "X-User-Id: $USER_ID" -H "X-User-Role: USER" \
  -d "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":2}]}"                              # 409: менять можно только CREATED
```

---

## 14. Мягкое удаление товара

```bash
c -X DELETE $API/products/$P2 -H "X-User-Id: $SELLER2_ID" -H "X-User-Role: SELLER"   # 403: чужой товар
c -X DELETE $API/products/$P2 -H "X-User-Id: $SELLER_ID"  -H "X-User-Role: SELLER"   # 200, status ARCHIVED
```

```sql
SELECT id, name, status, updated_at FROM products;   -- запись осталась, статус ARCHIVED
```

---

## 15. Логи

`X-Request-Id` приходит в заголовке ответа:

```bash
curl -i $API/products/$P1
```

Найти этот запрос в логах:

```bash
docker compose logs app | grep <X-Request-Id из ответа>
```

Посмотреть последние логи:

```bash
docker compose logs app --tail 5
```

Каждая строка — JSON с полями `timestamp`, `request_id`, `method`, `endpoint`, `status_code`, `duration_ms`, `user_id`, `request_body`.

- Для запросов без заголовков `user_id` = `null`.
- У POST, PUT и DELETE в `request_body` — тело запроса. Паролей в API нет, поэтому маскировать нечего.

---

## 16. Итоговое состояние БД

```sql
SELECT id, name, price, stock, category, status, seller_id FROM products;
SELECT id, user_id, status, promo_code_id, total_amount, discount_amount, created_at, updated_at FROM orders;
SELECT * FROM order_items;
SELECT * FROM promo_codes;
SELECT * FROM user_operations ORDER BY created_at;
```
