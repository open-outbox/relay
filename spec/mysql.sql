CREATE TABLE outbox_events (
    id         BINARY(16) PRIMARY KEY,  
    topic      VARCHAR(255) NOT NULL,
    payload    LONGBLOB NOT NULL,       
    status     VARCHAR(20) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_outbox_pending ON outbox_events (status, created_at);