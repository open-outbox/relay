docker exec -it $(docker ps -qf "name=postgres") psql -U postgres -d postgres -c "
CREATE TABLE outbox_events (
    id         UUID PRIMARY KEY,
    topic      TEXT NOT NULL,
    payload    BYTEA NOT NULL,          -- Binary data (JWT, JSON, Protobuf)
    status     VARCHAR(20) NOT NULL DEFAULT 'pending',
    retry_count INT DEFAULT 0,
    last_error TEXT;
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_outbox_pending ON outbox_events (status, created_at) 
WHERE status = 'pending';

INSERT INTO outbox_events (id, topic, payload, status) VALUES 
(gen_random_uuid(), 'user.created', '{\"id\": 1, \"email\": \"test@example.com\"}', 'pending'),
(gen_random_uuid(), 'order.placed', '{\"id\": 99, \"total\": 45.00}', 'pending'),
(gen_random_uuid(), 'payment.success', '{\"id\": 555, \"method\": \"stripe\"}', 'pending');
"