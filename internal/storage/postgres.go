package storage

import (
	"context"
	"fmt"
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
	leaseMinutes int,
) ([]relay.Event, error) {
	query := `
        WITH target_events AS (
            SELECT event_id 
            FROM outbox_events
            WHERE 
                -- Standard pickup: status pending and available
                (status = $1 AND available_at <= NOW())
                OR 
                -- Rescue stuck leases: leased but stuck
                (status = $2 AND locked_at < NOW() - MAKE_INTERVAL(mins => $3))
            ORDER BY created_at ASC
            LIMIT $4
            FOR UPDATE SKIP LOCKED
        ) 
        UPDATE outbox_events as e
        SET 
            status = $2,
            locked_by = $5,  
            locked_at = NOW(),
            updated_at = NOW()
        FROM target_events as t
        WHERE e.event_id = t.event_id
        RETURNING 
            e.event_id, 
            e.event_type, 
            e.payload, 
            e.partition_key,
            e.attempts;
    `

	rows, err := p.pool.Query(ctx, query,
		relay.StatusPending,
		relay.StatusDelivering,
		leaseMinutes,
		batchSize,
		relayID,
	)
	if err != nil {
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
		)
		if err != nil {
			return nil, fmt.Errorf("event scan error: %w", err)
		}
		events = append(events, e)
	}

	if err = rows.Err(); err != nil {
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

	if err != nil {
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

	if err != nil {
		return fmt.Errorf("failed to mark batch failures: %w", err)
	}

	if res.RowsAffected() < int64(n) {
		// "We lost the lease on some rows while we were processing them."
		log.Warn("Lease expired during processing for some events")
	}

	return nil
}

// MarkDone updates the status to 'completed'.
func (p *Postgres) MarkDone(ctx context.Context, id string) error {
	query := `UPDATE outbox_events SET status = $2, updated_at = NOW() WHERE event_id = $1`
	_, err := p.pool.Exec(ctx, query, id, relay.StatusDelivered)
	return err
}

func (p *Postgres) MarkFailed(ctx context.Context, id string, reason string) error {
	query := `
		UPDATE outbox_events 
		SET attempts = attempts + 1, 
		    last_error = $2,
		    updated_at = NOW() 
		WHERE event_id = $1`
	_, err := p.pool.Exec(ctx, query, id, reason)
	return err
}

func (p *Postgres) GetStats(ctx context.Context) (relay.Stats, error) {
	var stats relay.Stats
	query := `
        SELECT 
            COUNT(*) FILTER (WHERE status = 'PENDING' AND attempts = 0) as pending,
            COUNT(*) FILTER (WHERE status = 'PENDING' AND attempts > 0) as retrying,
            COUNT(*) FILTER (WHERE status = 'DELIVERING') as in_flight,
            COUNT(*) FILTER (WHERE status = 'DEAD') as dead
        FROM outbox_events`

	// Note: You'll need to update your relay.Stats struct to include InFlight and Dead!
	err := p.pool.QueryRow(ctx, query).Scan(
		&stats.Pending,
		&stats.Retrying,
		&stats.InFlight,
		&stats.Failed,
	)
	return stats, err
}
