CREATE TABLE payouts (
    id         text PRIMARY KEY,
    seller_id  text        NOT NULL,
    currency   char(3)     NOT NULL,
    amount     bigint      NOT NULL CHECK (amount > 0),
    status     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX payouts_seller_idx ON payouts (seller_id, created_at);
