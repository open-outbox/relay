docker exec -it $(docker ps -qf "name=postgres") psql -U postgres -d postgres -c "
-- 1. Wipe the slate clean
DROP TABLE IF EXISTS outbox_events CASCADE;

-- Re-running the full CREATE for your reference:
CREATE TABLE outbox_events (
    event_id      UUID PRIMARY KEY,
    event_type    TEXT NOT NULL,
    partition_key TEXT,
    payload       BYTEA NOT NULL,
    headers       JSONB NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'PENDING',
    attempts      INT NOT NULL DEFAULT 0,
    last_error    TEXT,
    available_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_by     TEXT,
    locked_at     TIMESTAMPTZ,
    delivered_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    CONSTRAINT check_valid_status CHECK (
        status IN ('PENDING', 'DELIVERING', 'DELIVERED', 'DEAD')
    )
);

CREATE INDEX idx_outbox_processing_queue 
ON outbox_events (available_at ASC, created_at ASC)
WHERE status = 'PENDING';

CREATE INDEX idx_outbox_stuck_leases 
ON outbox_events (locked_at) 
WHERE status = 'DELIVERING';

CREATE INDEX idx_outbox_archive_lookup 
ON outbox_events (delivered_at) 
WHERE status = 'DELIVERED';
"