package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/gommon/log"
	"github.com/open-outbox/relay/internal/relay"
	"go.uber.org/zap"
)

// Postgres is a PostgreSQL-backed implementation of the relay.Storage interface.
// It uses the jackc/pgx/v5 library for efficient connection pooling and
// PostgreSQL-specific optimizations.
type Postgres struct {
	pool   *pgxpool.Pool
	logger *zap.Logger
}

// NewPostgres creates a new Postgres storage instance using the provided pgx connection pool.
func NewPostgres(pool *pgxpool.Pool, logger *zap.Logger) *Postgres {
	return &Postgres{pool: pool, logger: logger}
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
func (p *Postgres) ReapExpiredLeases(
	ctx context.Context,
	leaseTimeout time.Duration,
	limit int,
) (int64, error) {
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

// Prune removes historical event data from the outbox based on the provided age thresholds.
//
// It handles two types of cleanup:
//  1. DELIVERED: Successfully processed events that are no longer needed for auditing.
//  2. DEAD: Events that failed all retry attempts and have been quarantined.
//
// If opts.DryRun is true, it performs a non-destructive count of the records
// that meet the criteria. Otherwise, it executes the deletions within a transaction
// to ensure consistency and returns the total number of rows removed.
func (p *Postgres) Prune(ctx context.Context, opts relay.PruneOptions) (relay.PruneResult, error) {

	if err := validateInterval(opts.DeliveredAge); err != nil {
		return relay.PruneResult{}, fmt.Errorf("delivered_age validation: %w", err)
	}
	if err := validateInterval(opts.DeadAge); err != nil {
		return relay.PruneResult{}, fmt.Errorf("dead_age validation: %w", err)
	}

	deliveredInterval := formatInterval(opts.DeliveredAge)
	deadInterval := formatInterval(opts.DeadAge)

	if opts.DryRun {
		return p.countPotentialDeletes(ctx, deliveredInterval, deadInterval)
	}

	return p.executePrune(ctx, deliveredInterval, deadInterval)
}

// Close gracefully shuts down the underlying pgx connection pool.
func (p *Postgres) Close() error { p.pool.Close(); return nil }

func (p *Postgres) countPotentialDeletes(
	ctx context.Context,
	deliveredInterval, deadInterval string,
) (relay.PruneResult, error) {
	var res relay.PruneResult
	query := `
		SELECT
			COUNT(*) FILTER (
				WHERE $1::text IS NOT NULL
				AND status = 'DELIVERED'
				AND delivered_at < NOW() - $1::interval
			),
			COUNT(*) FILTER (
				WHERE $2::text IS NOT NULL
				AND status = 'DEAD'
				AND updated_at < NOW() - $2::interval
			)
		FROM outbox_events;
	`
	var dInv, fInv interface{}
	if deliveredInterval != "" {
		dInv = deliveredInterval
	}
	if deadInterval != "" {
		fInv = deadInterval
	}

	err := p.pool.QueryRow(ctx, query, dInv, fInv).Scan(
		&res.DeliveredDeleted,
		&res.DeadDeleted,
	)

	return res, err
}

func (p *Postgres) executePrune(
	ctx context.Context,
	deliveredInterval, deadInterval string,
) (relay.PruneResult, error) {
	var res relay.PruneResult
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			if !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				p.logger.Warn("failed to rollback transaction", zap.Error(rollbackErr))
			}
		}
	}()

	if deliveredInterval != "" {
		q := `DELETE FROM outbox_events WHERE status = $1 AND delivered_at < NOW() - $2::interval`
		tag, err := tx.Exec(ctx, q, relay.StatusDelivered, deliveredInterval)
		if err != nil {
			return res, fmt.Errorf("failed to prune delivered: %w", err)
		}
		res.DeliveredDeleted = tag.RowsAffected()
	}

	if deadInterval != "" {
		q := `DELETE FROM outbox_events WHERE status = $1 AND updated_at < NOW() - $2::interval`
		tag, err := tx.Exec(ctx, q, relay.StatusDead, deadInterval)
		if err != nil {
			return res, fmt.Errorf("failed to prune dead: %w", err)
		}
		res.DeadDeleted = tag.RowsAffected()
	}

	return res, tx.Commit(ctx)
}

func durationToInterval(d time.Duration) string {
	ms := d.Milliseconds()
	return fmtIntervalMillis(ms)
}

func fmtIntervalMillis(ms int64) string {
	return strconv.FormatInt(ms, 10) + " milliseconds"
}

func formatInterval(age string) string {
	age = strings.ToLower(strings.TrimSpace(age))
	if age == "" || age == "0" {
		return ""
	}
	if strings.HasSuffix(age, "d") {
		return strings.Replace(age, "d", " days", 1)
	}
	if strings.HasSuffix(age, "h") {
		return strings.Replace(age, "h", " hours", 1)
	}
	if strings.HasSuffix(age, "m") {
		return strings.Replace(age, "m", " minutes", 1)
	}
	return age + " hours"
}

var intervalRegex = regexp.MustCompile(`^\d+[dhm]$`)

func validateInterval(age string) error {
	if age == "" || age == "0" {
		return nil
	}
	if !intervalRegex.MatchString(age) {
		return fmt.Errorf("invalid retention format '%s': must match [number][d|h|m]", age)
	}
	return nil
}
