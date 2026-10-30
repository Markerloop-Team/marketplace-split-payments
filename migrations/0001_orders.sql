CREATE TABLE orders (
    id          text PRIMARY KEY,
    customer_id text        NOT NULL,
    currency    char(3)     NOT NULL,
    discount    bigint      NOT NULL CHECK (discount >= 0),
    total       bigint      NOT NULL CHECK (total >= 0),
    status      text        NOT NULL,
    payment_id  text UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sub_orders (
    id                  text PRIMARY KEY,
    order_id            text   NOT NULL REFERENCES orders (id),
    seller_id           text   NOT NULL,
    rate_bps            bigint NOT NULL CHECK (rate_bps BETWEEN 0 AND 10000),
    gross               bigint NOT NULL CHECK (gross >= 0),
    commission          bigint NOT NULL CHECK (commission BETWEEN 0 AND gross),
    net                 bigint NOT NULL,
    refunded            bigint NOT NULL DEFAULT 0 CHECK (refunded BETWEEN 0 AND gross),
    commission_refunded bigint NOT NULL DEFAULT 0 CHECK (commission_refunded BETWEEN 0 AND commission),
    UNIQUE (order_id, seller_id),
    CHECK (net = gross - commission)
);

CREATE INDEX sub_orders_seller_idx ON sub_orders (seller_id);

CREATE TABLE order_lines (
    id           text PRIMARY KEY,
    sub_order_id text   NOT NULL REFERENCES sub_orders (id),
    position     int    NOT NULL,
    sku          text   NOT NULL,
    qty          bigint NOT NULL CHECK (qty > 0),
    unit_price   bigint NOT NULL CHECK (unit_price >= 0),
    paid         bigint NOT NULL CHECK (paid >= 0),
    refunded_qty bigint NOT NULL DEFAULT 0 CHECK (refunded_qty BETWEEN 0 AND qty)
);

CREATE INDEX order_lines_sub_order_idx ON order_lines (sub_order_id);
