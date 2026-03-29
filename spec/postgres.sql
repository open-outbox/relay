CREATE TABLE outbox_events (
    id         UUID PRIMARY KEY,
    topic      TEXT NOT NULL,
    payload    BYTEA NOT NULL,          -- Binary data (JWT, JSON, Protobuf)
    status     VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_outbox_pending ON outbox_events (status, created_at) 
WHERE status = 'pending';