// Package migrations содержит SQL-миграции БД. Файлы встраиваются в бинарник
// и применяются goose при старте приложения.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
