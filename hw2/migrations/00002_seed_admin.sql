-- Администратор по умолчанию: admin@example.com / admin123.
-- Через /auth/register роль ADMIN получить нельзя, поэтому создаём его миграцией.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;

INSERT INTO users (email, password_hash, role)
VALUES ('admin@example.com', crypt('admin123', gen_salt('bf', 10)), 'ADMIN');

-- +goose Down
DELETE FROM users WHERE email = 'admin@example.com';
