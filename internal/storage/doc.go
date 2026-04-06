// Package storage provides the persistence layer implementations for the Outbox Relay.
//
// It defines the logic for interacting with various databases to manage the lifecycle
// of outbox events. The core responsibilities of this package include:
//   - Claiming batches of pending events using row-level locking (e.g., FOR UPDATE SKIP LOCKED).
//   - Updating event status after successful or failed delivery.
//   - Reaping "stuck" events whose processing leases have expired.
//   - Providing metrics and health statistics about the outbox table.
//
// The primary implementation is PostgreSQL, which leverages pgx for high-performance
// connection pooling and advanced SQL features like UNNEST for batch updates.
//
// All storage implementations must satisfy the relay.Storage interface to ensure
// pluggability within the relay engine.
package storage
