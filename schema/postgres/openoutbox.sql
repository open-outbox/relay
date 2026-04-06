-- Wipe the slate clean for development
DROP TABLE IF EXISTS outbox_events CASCADE;

-- Create the core Outbox table
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

-- Optimization Indexes
CREATE INDEX IF NOT EXISTS idx_outbox_processing_queue
    ON public.outbox_events (available_at ASC, created_at ASC)
    WHERE status = 'PENDING';

CREATE INDEX IF NOT EXISTS idx_outbox_stuck_leases
    ON public.outbox_events (locked_at ASC)
    WHERE status = 'DELIVERING';

-- Metrics-specific partial indexes
CREATE INDEX IF NOT EXISTS idx_outbox_metrics_count
    ON public.outbox_events (status)
    WHERE status = 'PENDING';


CREATE INDEX IF NOT EXISTS idx_outbox_metrics_lag
    ON public.outbox_events (status, created_at ASC)
    WHERE status = 'PENDING'::text;
