-- -----------------------------------------------------------------------------
-- MAINTENANCE INDEXES (Optimizing for relay-cli prune)
-- -----------------------------------------------------------------------------

-- Pruning DELIVERED: Minimizes I/O when cleaning up successfully processed events.
-- We use a partial index because DELIVERED rows typically make up 90%+ of the table.
CREATE INDEX IF NOT EXISTS idx_outbox_prune_delivered
    ON outbox_events (delivered_at)
    WHERE status = 'DELIVERED';

-- Pruning DEAD: Allows for fast identification of abandoned events based on their last update.
CREATE INDEX IF NOT EXISTS idx_outbox_prune_dead
    ON outbox_events (updated_at)
    WHERE status = 'DEAD';
