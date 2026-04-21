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

const (
	sqlClaimBatch = `
		WITH target_events AS (
			SELECT event_id
			FROM {{TABLE}}
			WHERE
				-- Standard pickup: status pending and available
				status = $1 AND available_at <= NOW()
			ORDER BY available_at ASC, created_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE {{TABLE}} as e
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
	sqlMarkDeliveredBatch = `
        UPDATE {{TABLE}}
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
	sqlMarkFailedBatch = `
        UPDATE {{TABLE}} AS e
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
	sqlReapExpiredLeases = `
        WITH stuck_events AS (
            SELECT event_id
            FROM {{TABLE}}
            WHERE status = $1
				AND (
					locked_at < (now() - $2::interval)
					OR locked_at IS NULL
				)
            ORDER BY locked_at ASC
            LIMIT $3
            FOR UPDATE SKIP LOCKED
        )
        UPDATE {{TABLE}}
        SET status = $4,
            locked_by = NULL,
            locked_at = NULL
        FROM stuck_events
        WHERE {{TABLE}}.event_id = stuck_events.event_id
    `
	sqlStats = `
        SELECT
            COALESCE((SELECT count(*) FROM {{TABLE}} WHERE status=$1), 0)::bigint,
            COALESCE(EXTRACT(EPOCH FROM (now() - (SELECT min(created_at)
				FROM {{TABLE}} WHERE status=$1))), 0)::bigint
	`
	sqlPruneStats = `
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
		FROM {{TABLE}};
	`
	sqlPruneDelivered = `
		DELETE FROM {{TABLE}} WHERE status = $1 AND delivered_at < NOW() - $2::interval
	`
	sqlPruneDead = `
		DELETE FROM {{TABLE}} WHERE status = $1 AND updated_at < NOW() - $2::interval
	`
)

// Postgres is a PostgreSQL-backed implementation of the relay.Storage interface.
// It uses the jackc/pgx/v5 library for efficient connection pooling and
// PostgreSQL-specific optimizations.
type Postgres struct {
	pool                    *pgxpool.Pool
	logger                  *zap.Logger
	queryClaimBatch         string
	queryMarkDeliveredBatch string
	queryMarkFailedBatch    string
	queryReapExpiredLeases  string
	queryStats              string
	queryPruneStats         string
	queryPruneDelivered     string
	queryPruneDead          string
}

// NewPostgres creates a new Postgres storage instance using the provided pgx connection pool.
func NewPostgres(pool *pgxpool.Pool, tableName string, logger *zap.Logger) (*Postgres, error) {
	if err := ValidateTableName(tableName); err != nil {
		return nil, err
	}
	logger.Info("postgres storage initialized", zap.String("table", tableName))
	p := &Postgres{
		pool:   pool,
		logger: logger,
	}

	p.queryClaimBatch = strings.ReplaceAll(sqlClaimBatch, "{{TABLE}}", tableName)
	p.queryMarkDeliveredBatch = strings.ReplaceAll(sqlMarkDeliveredBatch, "{{TABLE}}", tableName)
	p.queryMarkFailedBatch = strings.ReplaceAll(sqlMarkFailedBatch, "{{TABLE}}", tableName)
	p.queryReapExpiredLeases = strings.ReplaceAll(sqlReapExpiredLeases, "{{TABLE}}", tableName)
	p.queryStats = strings.ReplaceAll(sqlStats, "{{TABLE}}", tableName)
	p.queryPruneStats = strings.ReplaceAll(sqlPruneStats, "{{TABLE}}", tableName)
	p.queryPruneDelivered = strings.ReplaceAll(sqlPruneDelivered, "{{TABLE}}", tableName)
	p.queryPruneDead = strings.ReplaceAll(sqlPruneDead, "{{TABLE}}", tableName)

	return p, nil
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

	if batchSize <= 0 {
		return nil, nil
	}

	rows, err := p.pool.Query(ctx, p.queryClaimBatch,
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

	_, err := p.pool.Exec(ctx, p.queryMarkDeliveredBatch,
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

	res, err := p.pool.Exec(ctx, p.queryMarkFailedBatch,
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

	result, err := p.pool.Exec(
		ctx,
		p.queryReapExpiredLeases,
		relay.StatusDelivering,
		durationToInterval(leaseTimeout),
		limit,
		relay.StatusPending,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to reap expired leases: %w", err)
	}

	return result.RowsAffected(), nil
}

// GetStats retrieves high-level metrics from the outbox table, such as the total
// number of pending events and the age of the oldest message in the queue.
func (p *Postgres) GetStats(ctx context.Context) (relay.Stats, error) {
	var stats relay.Stats

	err := p.pool.QueryRow(ctx, p.queryStats, relay.StatusPending).Scan(
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
func (p *Postgres) Close(_ context.Context) error { p.pool.Close(); return nil }

// Ping checks the health of the Postgres connection pool. It ensures
// that the database is reachable and accepting commands. This is
// used primarily by the health check endpoint for liveness probes.
func (p *Postgres) Ping(ctx context.Context) error {
	if p.pool == nil {
		return fmt.Errorf("database connection not initialized")
	}

	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping failed: %w", err)
	}

	return nil
}

func (p *Postgres) countPotentialDeletes(
	ctx context.Context,
	deliveredInterval, deadInterval string,
) (relay.PruneResult, error) {
	var res relay.PruneResult

	var dInv, fInv interface{}
	if deliveredInterval != "" {
		dInv = deliveredInterval
	}
	if deadInterval != "" {
		fInv = deadInterval
	}

	err := p.pool.QueryRow(ctx, p.queryPruneStats, dInv, fInv).Scan(
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
		tag, err := tx.Exec(ctx, p.queryPruneDelivered, relay.StatusDelivered, deliveredInterval)
		if err != nil {
			return res, fmt.Errorf("failed to prune delivered: %w", err)
		}
		res.DeliveredDeleted = tag.RowsAffected()
	}

	if deadInterval != "" {
		tag, err := tx.Exec(ctx, p.queryPruneDead, relay.StatusDead, deadInterval)
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
