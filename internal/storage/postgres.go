package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/gommon/log"
	"github.com/open-outbox/relay/internal/relay"
)

// Postgres is a PostgreSQL-backed implementation of the relay.Storage interface.
// It uses the jackc/pgx/v5 library for efficient connection pooling and
// PostgreSQL-specific optimizations.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres creates a new Postgres storage instance using the provided pgx connection pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// ClaimBatch atomically selects and locks a batch of pending events for the current relay instance.
//
// It uses a Common Table Expression (CTE) with 'FOR UPDATE SKIP LOCKED' to ensure that:
// 1. Multiple relay instances can process the table concurrently without colliding.
// 2. Only events that are PENDING and past their available_at time are selected.
// 3. Selected events are immediately marked as DELIVERING to "lease" them to this instance.
func (p *Postgres) ClaimBatch(
	ctx context.Context,
	relayID string,
	batchSize int,
	buf []relay.Event,
) ([]relay.Event, error) {
	query := `
        WITH target_events AS (
            SELECT event_id 
            FROM outbox_events
            WHERE 
                -- Standard pickup: status pending and available
                status = $1 AND available_at <= NOW()
            ORDER BY available_at ASC, created_at ASC
            LIMIT $2
            FOR UPDATE SKIP LOCKED
        ) 
        UPDATE outbox_events as e
        SET 
            status = $3,
            locked_by = $4,  
            locked_at = NOW(),
            updated_at = NOW()
        FROM target_events as t
        WHERE e.event_id = t.event_id
        RETURNING 
            e.event_id, 
            e.event_type,
			e.partition_key,
            e.payload,
			e.headers,
            e.attempts,
			e.created_at;
    `

	rows, err := p.pool.Query(ctx, query,
		relay.StatusPending,
		batchSize,
		relay.StatusDelivering,
		relayID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to claim batch: %w", err)
	}
	defer rows.Close()

	i := 0
	for rows.Next() {
		err := rows.Scan(
			&buf[i].ID,
			&buf[i].Type,
			&buf[i].PartitionKey,
			&buf[i].Payload,
			&buf[i].Headers,
			&buf[i].Attempts,
			&buf[i].CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("event scan error: %w", err)
		}
		i++
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("rows stream error: %w", err)
	}

	return buf[:i], nil
}

// MarkDeliveredBatch updates a set of events to the DELIVERED status.
// It requires the relayID to match the current lock to ensure that an instance
// doesn't accidentally mark an event as delivered if it has already been reaped.
func (p *Postgres) MarkDeliveredBatch(
	ctx context.Context,
	ids []uuid.UUID,
	relayID string,
) error {
	if len(ids) == 0 {
		return nil
	}

	query := `
        UPDATE outbox_events
        SET 
            status = $1,
			delivered_at = NOW(),
            locked_by = NULL,
            locked_at = NULL,
            updated_at = NOW()
        WHERE 
			event_id = ANY($2)
          	AND status = $3
          	AND locked_by = $4
    `

	_, err := p.pool.Exec(ctx, query,
		relay.StatusDelivered,
		ids,
		relay.StatusDelivering,
		relayID,
	)

	if err != nil {
		return fmt.Errorf("failed to mark batch delivered: %w", err)
	}

	return nil
}

// MarkFailedBatch updates multiple events that failed during publishing.
//
// It uses the PostgreSQL UNNEST function to perform a single atomic batch update,
// which is significantly more efficient than individual UPDATE statements.
// It updates the status, increases the attempt count, and sets the next available_at time.
func (p *Postgres) MarkFailedBatch(
	ctx context.Context,
	failures []relay.FailedEvent,
	relayID string,
) error {
	if len(failures) == 0 {
		return nil
	}

	n := len(failures)
	ids := make([]uuid.UUID, n)
	statuses := make([]relay.Status, n)
	avails := make([]time.Time, n)
	attempts := make([]int, n)
	errors := make([]string, n)

	for i, f := range failures {
		ids[i] = f.ID
		statuses[i] = f.NewStatus
		avails[i] = f.AvailableAt
		attempts[i] = f.Attempts
		errors[i] = f.LastError
	}

	query := `
        UPDATE outbox_events AS e
        SET 
            status       = v.status,
            available_at = v.avail,
            attempts     = v.att,
            last_error   = v.err,
            locked_by    = NULL,
            locked_at    = NULL,
            updated_at   = NOW()
        FROM (
            SELECT * FROM UNNEST(
                $1::uuid[],
                $2::text[],
                $3::timestamptz[],
                $4::int[],
                $5::text[]
            ) AS t(id, status, avail, att, err)
        ) AS v
        WHERE e.event_id = v.id
          AND e.status = $6     -- Safety: Ensure it hasn't been reaped
          AND e.locked_by = $7;  -- Ownership: Ensure this relay still owns it
    `

	res, err := p.pool.Exec(ctx, query,
		ids,
		statuses,
		avails,
		attempts,
		errors,
		relay.StatusDelivering,
		relayID,
	)

	if err != nil {
		return fmt.Errorf("failed to mark batch failures: %w", err)
	}

	if res.RowsAffected() < int64(n) {
		log.Warn("Lease expired during processing for some events")
	}

	return nil
}

// ReapExpiredLeases identifies and resets events that have been stuck in the DELIVERING
// state for longer than the specified leaseTimeout.
//
// This is a critical recovery mechanism. If a relay instance crashes while processing
// a batch, this function allows other instances to eventually pick up those "orphaned"
// events by moving them back to the PENDING state.
func (p *Postgres) ReapExpiredLeases(ctx context.Context, leaseTimeout time.Duration, limit int) (int64, error) {
	query := `
        WITH stuck_events AS (
            SELECT event_id 
            FROM outbox_events
            WHERE status = 'DELIVERING'
				AND (
					locked_at < (now() - $1::interval) 
					OR locked_at IS NULL
				)
            ORDER BY locked_at ASC
            LIMIT $2
            FOR UPDATE SKIP LOCKED
        )
        UPDATE outbox_events
        SET status = 'PENDING',
            locked_by = NULL,
            locked_at = NULL
        FROM stuck_events
        WHERE outbox_events.event_id = stuck_events.event_id
    `

	result, err := p.pool.Exec(ctx, query, durationToInterval(leaseTimeout), limit)
	if err != nil {
		return 0, fmt.Errorf("failed to reap expired leases: %w", err)
	}

	return result.RowsAffected(), nil
}

// GetStats retrieves high-level metrics from the outbox table, such as the total
// number of pending events and the age of the oldest message in the queue.
func (p *Postgres) GetStats(ctx context.Context) (relay.Stats, error) {
	var stats relay.Stats

	query := `
        SELECT
            COALESCE((SELECT count(*) FROM outbox_events WHERE status='PENDING'), 0)::bigint,
            COALESCE(EXTRACT(EPOCH FROM (now() - (SELECT min(created_at) 
				FROM outbox_events WHERE status='PENDING'))), 0)::bigint
	`

	err := p.pool.QueryRow(ctx, query).Scan(
		&stats.PendingCount,
		&stats.OldestAgeSec,
	)

	return stats, err
}

// Close gracefully shuts down the underlying pgx connection pool.
func (p *Postgres) Close() error { p.pool.Close(); return nil }

func durationToInterval(d time.Duration) string {
	ms := d.Milliseconds()
	return fmtIntervalMillis(ms)
}

func fmtIntervalMillis(ms int64) string {
	return strconv.FormatInt(ms, 10) + " milliseconds"
}
