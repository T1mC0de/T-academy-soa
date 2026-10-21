-- Начальная схема маркетплейса.

-- +goose Up
-- Общая функция для автоматического обновления updated_at.
-- +goose StatementBegin
CREATE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TYPE user_role       AS ENUM ('USER', 'SELLER', 'ADMIN');
CREATE TYPE product_status  AS ENUM ('ACTIVE', 'INACTIVE', 'ARCHIVED');
CREATE TYPE order_status    AS ENUM ('CREATED', 'PAYMENT_PENDING', 'PAID', 'SHIPPED', 'COMPLETED', 'CANCELED');
CREATE TYPE discount_type   AS ENUM ('PERCENTAGE', 'FIXED_AMOUNT');
CREATE TYPE operation_type  AS ENUM ('CREATE_ORDER', 'UPDATE_ORDER');

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role          user_role    NOT NULL DEFAULT 'USER',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE products (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        VARCHAR(255)   NOT NULL,
    description VARCHAR(4000),
    price       DECIMAL(12, 2) NOT NULL CHECK (price > 0),
    stock       INTEGER        NOT NULL CHECK (stock >= 0),
    category    VARCHAR(100)   NOT NULL,
    status      product_status NOT NULL,
    seller_id   UUID           NOT NULL REFERENCES users (id),
    created_at  TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE INDEX idx_products_status ON products (status);
CREATE INDEX idx_products_category ON products (category);

CREATE TABLE promo_codes (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code             VARCHAR(20)    NOT NULL UNIQUE,
    discount_type    discount_type  NOT NULL,
    discount_value   DECIMAL(12, 2) NOT NULL CHECK (discount_value > 0),
    min_order_amount DECIMAL(12, 2) NOT NULL DEFAULT 0,
    max_uses         INTEGER        NOT NULL CHECK (max_uses > 0),
    current_uses     INTEGER        NOT NULL DEFAULT 0 CHECK (current_uses >= 0),
    valid_from       TIMESTAMPTZ    NOT NULL,
    valid_until      TIMESTAMPTZ    NOT NULL,
    active           BOOLEAN        NOT NULL DEFAULT true
);

CREATE TABLE orders (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID           NOT NULL REFERENCES users (id),
    status          order_status   NOT NULL DEFAULT 'CREATED',
    promo_code_id   UUID           REFERENCES promo_codes (id),
    total_amount    DECIMAL(12, 2) NOT NULL,
    discount_amount DECIMAL(12, 2) NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ    NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_user_status ON orders (user_id, status);

CREATE TABLE order_items (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id       UUID           NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id     UUID           NOT NULL REFERENCES products (id),
    quantity       INTEGER        NOT NULL CHECK (quantity > 0),
    price_at_order DECIMAL(12, 2) NOT NULL
);

CREATE INDEX idx_order_items_order ON order_items (order_id);

CREATE TABLE user_operations (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID           NOT NULL REFERENCES users (id),
    operation_type operation_type NOT NULL,
    created_at     TIMESTAMPTZ    NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_operations_lookup ON user_operations (user_id, operation_type, created_at DESC);

CREATE TRIGGER users_set_updated_at    BEFORE UPDATE ON users    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER products_set_updated_at BEFORE UPDATE ON products FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER orders_set_updated_at   BEFORE UPDATE ON orders   FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE user_operations;
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE promo_codes;
DROP TABLE products;
DROP TABLE users;
DROP TYPE operation_type;
DROP TYPE discount_type;
DROP TYPE order_status;
DROP TYPE product_status;
DROP TYPE user_role;
DROP FUNCTION set_updated_at();
