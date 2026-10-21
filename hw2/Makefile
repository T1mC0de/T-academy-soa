generate: ## Сгенерировать код из api/openapi.yaml
	go generate ./...

up: ## Поднять всё: postgres → app (миграции применяются при старте)
	docker compose up --build

down: ## Остановить и удалить данные
	docker compose down -v

psql: ## Консоль БД
	docker compose exec postgres psql -U marketplace -d marketplace

migration: ## Создать новую миграцию: make migration name=add_something
	go run github.com/pressly/goose/v3/cmd/goose@v3.29.0 -dir migrations create $(name) sql
