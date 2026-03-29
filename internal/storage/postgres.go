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
		SELECT id, topic, payload, created_at 
		FROM outbox_events 
		WHERE status = 'pending' 
		AND retries < 5 -- New safety check
		ORDER BY created_at ASC 
		LIMIT $1
		FOR UPDATE SKIP LOCKED;`

	rows, err := p.pool.Query(ctx, query, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []relay.Event
	for rows.Next() {
		var e relay.Event
		err := rows.Scan(&e.ID, &e.Topic, &e.Payload, &e.CreatedAt)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}

	return events, nil
}

// MarkDone updates the status to 'completed'.
func (p *Postgres) MarkDone(ctx context.Context, id string) error {
	query := `UPDATE outbox_events SET status = 'completed', updated_at = NOW() WHERE id = $1`
	_, err := p.pool.Exec(ctx, query, id)
	return err
}

func (p *Postgres) MarkFailed(ctx context.Context, id string, reason string) error {
	query := `
		UPDATE outbox_events 
		SET retries = retries + 1, 
		    last_error = $2,
		    updated_at = NOW() 
		WHERE id = $1`
	_, err := p.pool.Exec(ctx, query, id, reason)
	return err
}

func (p *Postgres) GetStats(ctx context.Context) (relay.Stats, error) {
	var stats relay.Stats
	query := `
		SELECT 
			COUNT(*) FILTER (WHERE retries = 0) as pending,
			COUNT(*) FILTER (WHERE retries > 0 AND retries < 5) as retrying,
			COUNT(*) FILTER (WHERE retries >= 5) as failed
		FROM outbox_events 
		WHERE status = 'pending'`
	
	err := p.pool.QueryRow(ctx, query).Scan(&stats.Pending, &stats.Retrying, &stats.Failed)
	return stats, err
}