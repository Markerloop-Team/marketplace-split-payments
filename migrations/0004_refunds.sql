CREATE TABLE refunds (
    id              text PRIMARY KEY,
    order_id        text        NOT NULL REFERENCES orders (id),
    idempotency_key text        NOT NULL,
    amount          bigint      NOT NULL CHECK (amount > 0),
    parts           jsonb       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, idempotency_key)
);
