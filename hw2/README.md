# ДЗ-2. Marketplace: OpenAPI + CRUD

Go 1.26 · net/http · oapi-codegen · kin-openapi · PostgreSQL 17 · goose · database/sql (драйвер pgx)

## Запуск

```bash
docker compose up --build          # postgres → app на :8080 (миграции применяются при старте)
make psql                          # консоль БД для SELECT
```

Если порт 8080 занят: `APP_PORT=18080 docker compose up --build`.

Аутентификации (JWT, логина, регистрации) и ролей в этом ДЗ нет. Операциям с заказами нужно знать пользователя — он передаётся заголовком:

```
X-User-Id: 3f1c...-uuid      # любой UUID
```

Заголовок описан в OpenAPI как обязательный header-параметр у `/orders*`. Без него или не UUID — `400 VALIDATION_ERROR`. Товары и промокоды доступны без заголовка.

Новая миграция: `make migration name=add_something`.

Rate limit (N минут) задаётся через `ORDER_RATE_LIMIT_MINUTES` (по умолчанию 1).

## Структура

```
api/openapi.yaml              # контракт — единственный источник правды
internal/api/generate.go      # //go:generate → api.gen.go (в .gitignore)
internal/api/oapi-codegen.yaml
internal/app/
  server.go                   # сборка цепочки middleware
  middleware.go               # JSON-логи
  validation.go               # валидация запросов по OpenAPI → VALIDATION_ERROR
  errors.go                   # AppError → JSON единого формата
  products.go                 # CRUD товаров
  orders.go                   # заказы: создание, обновление, отмена
  promo_codes.go              # промокоды и расчёт скидки
migrations/                   # goose: 00001_init_schema.sql (+ embed.go)
cmd/marketplace/main.go       # конфиг из env, подключение к БД, запуск HTTP
```

## Путь запроса

```
HTTP → loggingMiddleware → OpenAPI-валидатор → сгенерированный роутер → метод Server
          request_id,         400 VALIDATION_ERROR   разбирает JSON и заголовки   бизнес-логика,
          JSON-лог            до бизнес-логики       в сгенерированные DTO        возвращает DTO или AppError
```

## Соответствие пунктам задания

| # | Пункт | Где |
|---|-------|-----|
| 1 | CRUD-эндпоинты, пагинация `page/size`, фильтры `status/category`, `totalElements` | `api/openapi.yaml`, `products.go` |
| 2 | `ProductCreate` / `ProductUpdate` / `ProductResponse`, `format: decimal/date-time/uuid`, `enum`, `nullable`, `required` | `api/openapi.yaml` |
| 3 | Генерация одной командой `go generate ./...`; `api.gen.go` в `.gitignore`; генерируется в `Dockerfile`; используются сгенерированные хендлеры (`StrictServerInterface`) и модели | `internal/api/`, `Dockerfile` |
| 4 | PostgreSQL, миграции goose (Up/Down), индекс `idx_products_status`, `created_at` default + trigger `updated_at`, soft delete | `migrations/`, `cmd/marketplace/main.go` |
| 5 | Единый формат `{error_code, message, details}`, все коды в `ErrorCode` enum | `errors.go`, `api/openapi.yaml` |
| 6 | Ограничения в спецификации, проверка middleware'ом kin-openapi, в `details.errors` — поле и нарушение | `validation.go` |
| 7 | Заказы: проверки по порядку, транзакции, снапшот цены, промокоды, state machine | `orders.go`, `promo_codes.go` |
| 8 | JSON-логи с `request_id, method, endpoint, status_code, duration_ms, user_id, timestamp`, тело POST/PUT/DELETE, `X-Request-Id` | `middleware.go` |

## Принятые решения

- **Strict-сервер oapi-codegen.** Хендлер получает уже разобранный DTO и возвращает типизированный ответ (`CreateProduct201JSONResponse`). Ручных DTO нет. Строка `var _ api.StrictServerInterface = (*Server)(nil)` не даст собрать проект, если в спецификации появится эндпоинт без реализации.
- **Ошибки как `error`.** Хендлер возвращает `*AppError` с HTTP-статусом и кодом. `ResponseErrorHandlerFunc` пишет его в формате схемы `Error`. Любая другая ошибка превращается в 500 `INTERNAL_ERROR`, детали попадают только в лог.
- **Валидация из спецификации, без ручных `if`.** Middleware `nethttp-middleware` (kin-openapi) проверяет запрос по `openapi.yaml`. При `MultiError: true` собираются все нарушения сразу. Вручную проверяется только то, что нельзя описать в OpenAPI: `valid_until > valid_from` и процент не больше 100.
- **Деньги — `decimal.Decimal`, а не float.** `0.1 + 0.2 != 0.3` во float. В БД `DECIMAL(12,2)`, в Go `shopspring/decimal`, в спецификации `x-go-type`.
- **Транзакции.** Каждая операция с заказом: `tx := db.Begin()`, `defer tx.Rollback()`, в конце `tx.Commit()`. Если функция вышла с ошибкой до Commit, срабатывает Rollback и в БД ничего не меняется. Одновременные запросы к одним и тем же данным не обрабатываются специально — в задании этого нет.
- **Одинаковые `product_id` в заказе суммируются** в одну позицию, чтобы остаток проверялся по общему количеству.
- **Скидка PERCENTAGE.** В задании написано: «не более 70% от total_amount, в ином случае цена 100%». Реализовано буквально: если скидка больше 70%, она не применяется. Это одна строка в `calcDiscount` (`promo_codes.go`).
- **Обновление заказа и промокод.** Если промокод перестал подходить (сумма меньше минимальной, истёк или деактивирован), он снимается с заказа, а `current_uses` уменьшается на 1. `current_uses < max_uses` повторно не проверяется: этот заказ уже занимает одно использование.
- **Пользователь из заголовка.** Аутентификация и роли вне рамок ДЗ. `X-User-Id` описан в спецификации, поэтому его проверяет тот же валидатор, а сгенерированный код кладёт его в `req.Params`. Таблицы `users` нет: `user_id` — просто UUID.
- **Миграции через goose** — стандартный для Go инструмент (аналог alembic в Python). SQL-файлы встроены в бинарник через `//go:embed`, приложение при старте вызывает `goose.Up`: применяет новые миграции и пропускает уже применённые. Отдельный контейнер не нужен. У каждой миграции есть секция `Down` для отката.

## Шпаргалка к теоретическим вопросам

- **Contract-first.** Сначала пишется спецификация, код генерируется из неё. Контракт и реализация не расходятся, клиенты могут сгенерировать SDK.
- **PUT и PATCH.** PUT полностью заменяет ресурс и идемпотентен. PATCH меняет ресурс частично.
- **Soft delete.** Запись остаётся, меняется статус. Нужно для истории и FK: `order_items` ссылаются на товар.
- **Индекс.** B-tree по `status` ускоряет `WHERE status = ...`. Цена — более медленная запись и место на диске.
- **Миграции (goose).** Файлы `00001_name.sql` с секциями `-- +goose Up` и `-- +goose Down`. Применённые версии хранятся в таблице `goose_db_version`. Уже применённую миграцию не редактируют, пишут новую (`make migration name=...`). `StatementBegin/End` нужен, когда внутри одной команды есть `;` (тело plpgsql-функции).
- **Транзакция, ACID.** Резерв остатков и создание заказа атомарны: либо всё, либо ничего.
- **403.** «Знаю, кто ты, но тебе нельзя»: заказ чужой (`ORDER_OWNERSHIP_VIOLATION`).
- **429.** Too Many Requests — превышен лимит частоты.
- **409 и 422.** 409 — конфликт с текущим состоянием ресурса (статус, остатки). 422 — запрос синтаксически верен, но не проходит бизнес-правило (промокод).
- **`X-Request-Id`.** Сквозной идентификатор, по которому запрос можно найти в логах.
