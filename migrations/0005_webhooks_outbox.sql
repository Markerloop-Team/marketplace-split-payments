CREATE TABLE processed_webhooks (
    provider    text        NOT NULL,
    event_id    text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, event_id)
);

CREATE TABLE outbox (
    id           bigserial PRIMARY KEY,
    topic        text        NOT NULL,
    key          text        NOT NULL,
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

CREATE INDEX outbox_pending_idx ON outbox (id) WHERE published_at IS NULL;
