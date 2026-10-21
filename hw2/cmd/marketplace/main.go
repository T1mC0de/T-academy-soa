package main

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/shopspring/decimal"

	"marketplace/internal/app"
	"marketplace/migrations"
)

func main() {
	// Все логи пишем в формате JSON.
	// По заданию поле времени называется "timestamp", а slog по умолчанию пишет "time" — переименовываем.
	renameTime := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Key = "timestamp"
		}
		return a
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: renameTime})))

	// Деньги отдаём в JSON числом (12.5), а не строкой ("12.5"), — как в OpenAPI (type: number).
	decimal.MarshalJSONWithoutQuotes = true

	if err := run(); err != nil {
		slog.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	// 1. Настройки из переменных окружения.
	databaseURL := env("DATABASE_URL", "postgres://marketplace:marketplace@localhost:5432/marketplace")
	addr := env("HTTP_ADDR", ":8080")
	rateLimitMinutes, err := strconv.Atoi(env("ORDER_RATE_LIMIT_MINUTES", "1"))
	if err != nil {
		return err
	}

	// 2. Подключение к PostgreSQL через стандартный database/sql.
	// sql.DB сам держит несколько соединений и раздаёт их параллельным запросам.
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// 3. Миграции: goose применяет SQL-файлы из папки migrations (они вшиты в бинарник)
	// и запоминает применённые версии в таблице goose_db_version.
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(db, "."); err != nil {
		return err
	}

	// 4. HTTP-сервер.
	server := app.NewServer(db, time.Duration(rateLimitMinutes)*time.Minute)
	handler, err := server.Handler()
	if err != nil {
		return err
	}
	slog.Info("server started", "addr", addr, "order_rate_limit_minutes", rateLimitMinutes)
	return http.ListenAndServe(addr, handler)
}

// env возвращает переменную окружения или значение по умолчанию.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
