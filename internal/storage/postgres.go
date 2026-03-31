package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
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
func (p *Postgres) Fetch(ctx context.Context, batchSize int) ([]relay.Event, error) {
	query := `
		SELECT event_id, event_type, payload, created_at 
		FROM outbox_events 
		WHERE status = $2 
		AND attempts < 5
		ORDER BY created_at ASC 
		LIMIT $1
		FOR UPDATE SKIP LOCKED;`

	rows, err := p.pool.Query(ctx, query, batchSize, relay.StatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []relay.Event
	for rows.Next() {
		var e relay.Event
		err := rows.Scan(&e.ID, &e.Type, &e.Payload, &e.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}

	return events, nil
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
