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

type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres creates a new storage backed by a connection pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

// Fetch pulls pending events from the DB.
func (p *Postgres) ClaimBatch(
	ctx context.Context,
	relayID string,
	batchSize int,
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
            e.payload, 
            e.partition_key,
            e.attempts,
			e.created_at;
    `

	rows, err := p.pool.Query(ctx, query,
		relay.StatusPending,
		batchSize,
		relay.StatusDelivering,
		relayID,
	)
	if err != nil && err != context.Canceled {
		return nil, fmt.Errorf("failed to claim batch: %w", err)
	}
	defer rows.Close()

	var events []relay.Event
	for rows.Next() {
		var e relay.Event
		err := rows.Scan(
			&e.ID,
			&e.Type,
			&e.Payload,
			&e.PartitionKey,
			&e.Attempts,
			&e.CreatedAt,
		)
		if err != nil && err != context.Canceled {
			return nil, fmt.Errorf("event scan error: %w", err)
		}
		events = append(events, e)
	}

	if err = rows.Err(); err != nil && err != context.Canceled {
		return nil, fmt.Errorf("rows stream error: %w", err)
	}

	return events, nil
}

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

	if err != nil && err != context.Canceled {
		return fmt.Errorf("failed to mark batch delivered: %w", err)
	}

	return nil
}

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

	// 2. The Atomic Update with UNNEST
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

	if err != nil && err != context.Canceled {
		return fmt.Errorf("failed to mark batch failures: %w", err)
	}

	if res.RowsAffected() < int64(n) {
		// "We lost the lease on some rows while we were processing them."
		log.Warn("Lease expired during processing for some events")
	}

	return nil
}

func (p *Postgres) ReapExpiredLeases(ctx context.Context, leaseTimeout time.Duration, limit int) (int64, error) {
	// We use a CTE (WITH block) to:
	// 1. Find the oldest stuck leases (ORDER BY locked_at)
	// 2. Limit the impact
	// 3. Prevent multiple reapers from fighting (SKIP LOCKED)

	query := `
        WITH stuck_events AS (
            SELECT event_id 
            FROM outbox_events
            WHERE status = 'DELIVERING'
              AND locked_at < (now() - $1::interval)
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

func (p *Postgres) Close() error { p.pool.Close(); return nil }

func durationToInterval(d time.Duration) string {
	// simplest portable interval literal
	// e.g. "1.5s" -> "1500 milliseconds"
	ms := d.Milliseconds()
	return fmtIntervalMillis(ms)
}

func fmtIntervalMillis(ms int64) string {
	// Postgres interval accepts "123 milliseconds"
	return strconv.FormatInt(ms, 10) + " milliseconds"
}
