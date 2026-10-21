package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/shopspring/decimal"

	"marketplace/internal/app"
	"marketplace/migrations"
)

func main() {
	// Логи в JSON; стандартное поле времени называем timestamp.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				a.Key = "timestamp"
			}
			return a
		},
	})))

	// Деньги отдаём в JSON числом (12.5), а не строкой ("12.5") — как описано в OpenAPI (type: number).
	decimal.MarshalJSONWithoutQuotes = true

	if err := run(); err != nil {
		slog.Error("server stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	rateLimitMinutes, err := strconv.Atoi(env("ORDER_RATE_LIMIT_MINUTES", "1"))
	if err != nil {
		return err
	}

	cfg, err := pgxpool.ParseConfig(env("DATABASE_URL", "postgres://marketplace:marketplace@localhost:5432/marketplace"))
	if err != nil {
		return err
	}
	// Учим pgx читать/писать NUMERIC в decimal.Decimal.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxdecimal.Register(conn.TypeMap())
		return nil
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		return err
	}
	if err := migrate(db); err != nil {
		return err
	}

	server := app.NewServer(db, []byte(env("JWT_SECRET", "dev-secret-change-me")), time.Duration(rateLimitMinutes)*time.Minute)
	handler, err := server.Handler()
	if err != nil {
		return err
	}

	addr := env("HTTP_ADDR", ":8080")
	slog.Info("server started", "addr", addr, "order_rate_limit_minutes", rateLimitMinutes)
	return http.ListenAndServe(addr, handler)
}

// migrate применяет SQL-миграции из папки migrations (они встроены в бинарник).
// goose хранит применённые версии в таблице goose_db_version и повторно их не выполняет.
func migrate(pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool) // goose работает через database/sql
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{})
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// gooseLogger пишет логи goose через slog, чтобы все логи были в JSON.
type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) {
	slog.Info(strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (gooseLogger) Fatalf(format string, v ...any) {
	slog.Error(strings.TrimSpace(fmt.Sprintf(format, v...)))
	os.Exit(1)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
