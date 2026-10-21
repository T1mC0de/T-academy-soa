#!/usr/bin/env bash
# E2E-демонстрация всех сценариев. Перед запуском: docker compose up -d --build
# Использование: ./demo.sh            (по умолчанию http://localhost:8080)
#                BASE=http://localhost:18080 ./demo.sh
set -u
BASE=${BASE:-http://localhost:8080}
RUN=$RANDOM # чтобы можно было запускать несколько раз подряд (уникальные email и промокоды)

step() { printf '\n\033[1;34m=== %s\033[0m\n' "$*"; }
# req METHOD PATH TOKEN [BODY] — печатает код ответа и тело, тело сохраняет в $OUT
req() {
  local method=$1 path=$2 token=$3 body=${4:-}
  local args=(-s -w '\n%{http_code}' -X "$method" "$BASE$path" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  local resp; resp=$(curl "${args[@]}")
  OUT=$(echo "$resp" | sed '$d')
  echo "-> $method $path  [HTTP $(echo "$resp" | tail -n1)]"
  echo "$OUT" | jq -c . 2>/dev/null || echo "$OUT"
}
sql() { docker compose exec -T postgres psql -U marketplace -d marketplace -c "$1"; }
# Чтобы не ждать N минут rate limit'а, «состариваем» операции пользователя в БД.
skip_rate_limit() { sql "UPDATE user_operations SET created_at = created_at - interval '1 hour';" >/dev/null; echo "(rate limit сброшен: операции сдвинуты на час назад)"; }
login() { curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$1\",\"password\":\"$2\"}" | jq -r .access_token; }

step "1. Регистрация и логин (USER, SELLER, второй SELLER, ADMIN из миграции)"
req POST /auth/register "" "{\"email\":\"user$RUN@test.com\",\"password\":\"secret123\"}"
req POST /auth/register "" "{\"email\":\"user2_$RUN@test.com\",\"password\":\"secret123\"}"
req POST /auth/register "" "{\"email\":\"seller$RUN@test.com\",\"password\":\"secret123\",\"role\":\"SELLER\"}"
req POST /auth/register "" "{\"email\":\"seller2_$RUN@test.com\",\"password\":\"secret123\",\"role\":\"SELLER\"}"
req POST /auth/register "" "{\"email\":\"user$RUN@test.com\",\"password\":\"secret123\"}"   # повтор → 409
req POST /auth/login "" "{\"email\":\"user$RUN@test.com\",\"password\":\"secret123\"}"
USER_REFRESH=$(echo "$OUT" | jq -r .refresh_token)
USER=$(echo "$OUT" | jq -r .access_token)
USER2=$(login "user2_$RUN@test.com" secret123)
SELLER=$(login "seller$RUN@test.com" secret123)
SELLER2=$(login "seller2_$RUN@test.com" secret123)
ADMIN=$(login admin@example.com admin123)
req POST /auth/login "" "{\"email\":\"user$RUN@test.com\",\"password\":\"wrong\"}"        # → 401 INVALID_CREDENTIALS

step "2. JWT: без токена, мусорный токен, просроченный токен, refresh"
req GET /products ""
req GET /products "garbage.token.value"
EXPIRED=$(python3 - <<'EOF'
import base64, hmac, hashlib, json, time
b = lambda d: base64.urlsafe_b64encode(json.dumps(d).encode()).rstrip(b'=').decode()
msg = b({"alg": "HS256", "typ": "JWT"}) + "." + b({"sub": "00000000-0000-0000-0000-000000000000", "role": "USER", "type": "access", "exp": int(time.time()) - 60})
sig = base64.urlsafe_b64encode(hmac.new(b"super-secret-key-for-homework", msg.encode(), hashlib.sha256).digest()).rstrip(b'=').decode()
print(msg + "." + sig)
EOF
)
req GET /products "$EXPIRED"                                                       # → 401 TOKEN_EXPIRED
req POST /auth/refresh "" "{\"refresh_token\":\"$USER\"}"                          # access вместо refresh → 401
req POST /auth/refresh "" "{\"refresh_token\":\"$USER_REFRESH\"}"                  # → 200

step "3. Товары: создание (SELLER), запрет для USER, валидация"
req POST /products "$SELLER" '{"name":"Phone","description":"Smartphone","price":100.00,"stock":10,"category":"electronics","status":"ACTIVE"}'
P1=$(echo "$OUT" | jq -r .id)
req POST /products "$SELLER" '{"name":"Case","price":50,"stock":5,"category":"accessories","status":"ACTIVE"}'
P2=$(echo "$OUT" | jq -r .id)
req POST /products "$SELLER" '{"name":"Old phone","price":10,"stock":100,"category":"electronics","status":"INACTIVE"}'
P3=$(echo "$OUT" | jq -r .id)
req POST /products "$USER" '{"name":"Hack","price":1,"stock":1,"category":"x","status":"ACTIVE"}'                 # → 403 ACCESS_DENIED
req POST /products "$SELLER" '{"name":"","price":0,"stock":-1,"category":"","status":"DELETED"}'                # → 400 со списком полей

step "4. Товары: чтение, список с фильтрами и пагинацией"
req GET "/products/$P1" "$USER"
req GET "/products/00000000-0000-0000-0000-000000000000" "$USER"                     # → 404 PRODUCT_NOT_FOUND
req GET "/products?page=0&size=2&category=electronics&status=ACTIVE" "$USER"
req GET "/products?page=-1&size=1000" "$USER"                                        # → 400

step "5. Товары: изменение чужого товара (SELLER2) запрещено, ADMIN может"
req PUT "/products/$P1" "$SELLER2" '{"name":"Stolen","price":1,"stock":1,"category":"x","status":"ACTIVE"}'        # → 403
req PUT "/products/$P1" "$ADMIN" '{"name":"Phone","description":"Smartphone v2","price":100.00,"stock":10,"category":"electronics","status":"ACTIVE"}'

step "6. Промокоды: SELLER создаёт, USER — нет"
req POST /promo-codes "$SELLER" "{\"code\":\"SAVE10_$RUN\",\"discount_type\":\"PERCENTAGE\",\"discount_value\":10,\"min_order_amount\":100,\"max_uses\":5,\"valid_from\":\"2020-01-01T00:00:00Z\",\"valid_until\":\"2099-01-01T00:00:00Z\"}"
req POST /promo-codes "$SELLER" "{\"code\":\"EXPIRED_$RUN\",\"discount_type\":\"FIXED_AMOUNT\",\"discount_value\":10,\"min_order_amount\":0,\"max_uses\":5,\"valid_from\":\"2020-01-01T00:00:00Z\",\"valid_until\":\"2021-01-01T00:00:00Z\"}"
req POST /promo-codes "$USER" "{\"code\":\"NOPE_$RUN\",\"discount_type\":\"FIXED_AMOUNT\",\"discount_value\":10,\"min_order_amount\":0,\"max_uses\":5,\"valid_from\":\"2020-01-01T00:00:00Z\",\"valid_until\":\"2099-01-01T00:00:00Z\"}"  # → 403

step "7. Заказ: ошибки каталога, остатков и промокодов (заказ не создаётся)"
req POST /orders "$SELLER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"                          # → 403 ACCESS_DENIED
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"00000000-0000-0000-0000-000000000000\",\"quantity\":1}]}"  # → 404
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"$P3\",\"quantity\":1}]}"                            # → 409 PRODUCT_INACTIVE
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":500},{\"product_id\":\"$P2\",\"quantity\":6}]}"  # → 409 INSUFFICIENT_STOCK
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}],\"promo_code\":\"NOSUCHCODE\"}"   # → 422 PROMO_CODE_INVALID
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}],\"promo_code\":\"EXPIRED_$RUN\"}" # → 422 PROMO_CODE_INVALID
req POST /orders "$USER2" "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}],\"promo_code\":\"SAVE10_$RUN\"}"  # 50 < 100 → 422 PROMO_CODE_MIN_AMOUNT
req POST /orders "$USER2" "{\"items\":[],\"promo_code\":\"bad code\"}"                                          # → 400

step "8. Заказ: успешное создание с промокодом (2×100 + 1×50 = 250, скидка 10% = 25, итог 225)"
req POST /orders "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":2},{\"product_id\":\"$P2\",\"quantity\":1}],\"promo_code\":\"SAVE10_$RUN\"}"
ORDER=$(echo "$OUT" | jq -r .id)
sql "SELECT name, stock FROM products WHERE id IN ('$P1','$P2');"
sql "SELECT code, current_uses FROM promo_codes WHERE code = 'SAVE10_$RUN';"

step "9. Rate limit и активный заказ"
req POST /orders "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"      # → 429 ORDER_LIMIT_EXCEEDED
skip_rate_limit
req POST /orders "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"      # → 409 ORDER_HAS_ACTIVE

step "10. Доступ к заказу: чужой USER, SELLER, ADMIN"
req GET "/orders/$ORDER" "$USER2"                                                     # → 403 ORDER_OWNERSHIP_VIOLATION
req GET "/orders/$ORDER" "$SELLER"                                                    # → 403 ACCESS_DENIED
req GET "/orders/00000000-0000-0000-0000-000000000000" "$ADMIN"                       # → 404 ORDER_NOT_FOUND
req GET "/orders/$ORDER" "$ADMIN"

step "11. Снапшот цены: меняем цену товара — в заказе price_at_order не меняется"
req PUT "/products/$P1" "$SELLER" '{"name":"Phone","price":999.99,"stock":8,"category":"electronics","status":"ACTIVE"}'
req GET "/orders/$ORDER" "$USER"
req PUT "/products/$P1" "$SELLER" '{"name":"Phone","price":100,"stock":8,"category":"electronics","status":"ACTIVE"}'

step "12. Обновление заказа: 1×Phone(100) — промокод ещё применим (итог 90), остатки пересчитаны"
req PUT "/orders/$ORDER" "$USER2" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"   # → 403 ORDER_OWNERSHIP_VIOLATION
req PUT "/orders/$ORDER" "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
sql "SELECT name, stock FROM products WHERE id IN ('$P1','$P2');"
req PUT "/orders/$ORDER" "$USER" "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}]}"    # → 429 ORDER_LIMIT_EXCEEDED
skip_rate_limit

step "13. Обновление заказа: 1×Case(50) < min 100 — промокод снимается, current_uses уменьшается"
req PUT "/orders/$ORDER" "$USER" "{\"items\":[{\"product_id\":\"$P2\",\"quantity\":1}]}"
sql "SELECT code, current_uses FROM promo_codes WHERE code = 'SAVE10_$RUN';"

step "14. Отмена заказа: остатки возвращаются, повторная отмена запрещена"
req POST "/orders/$ORDER/cancel" "$USER2" ""                                          # → 403
req POST "/orders/$ORDER/cancel" "$USER" ""
sql "SELECT name, stock FROM products WHERE id IN ('$P1','$P2');"
req POST "/orders/$ORDER/cancel" "$USER" ""                                           # → 409 INVALID_STATE_TRANSITION
req PUT "/orders/$ORDER" "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"    # → 409 INVALID_STATE_TRANSITION

step "15. Заказ в статусе PAID нельзя ни изменить, ни отменить (статус ставим через SQL — API оплаты нет)"
skip_rate_limit
req POST /orders "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":1}]}"
ORDER2=$(echo "$OUT" | jq -r .id)
sql "UPDATE orders SET status = 'PAID' WHERE id = '$ORDER2';"
req POST "/orders/$ORDER2/cancel" "$ADMIN" ""                                          # → 409 INVALID_STATE_TRANSITION
skip_rate_limit
req PUT "/orders/$ORDER2" "$USER" "{\"items\":[{\"product_id\":\"$P1\",\"quantity\":2}]}"   # → 409 INVALID_STATE_TRANSITION

step "16. Мягкое удаление товара"
req DELETE "/products/$P2" "$SELLER2"                                                 # → 403
req DELETE "/products/$P2" "$SELLER"
sql "SELECT id, name, status, created_at, updated_at FROM products WHERE id = '$P2';"

step "17. Таблицы в БД"
sql "SELECT id, email, role FROM users ORDER BY email;"
sql "SELECT id, user_id, status, promo_code_id, total_amount, discount_amount, created_at, updated_at FROM orders ORDER BY created_at;"
sql "SELECT * FROM order_items;"
sql "SELECT * FROM promo_codes;"
sql "SELECT * FROM user_operations ORDER BY created_at;"
sql "SELECT version_id, is_applied, tstamp FROM goose_db_version ORDER BY id;"
