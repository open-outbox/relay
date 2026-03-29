package storage

import (
	"context"
	"sync"

	"github.com/open-outbox/relay/internal/relay"
)

type Memory struct {
	mu     sync.RWMutex
	events map[string]relay.Event
}

func NewMemory() *Memory {
	return &Memory{
		events: make(map[string]relay.Event),
	}
}

// Add is a helper for our simulator to "insert" events into memory.
func (m *Memory) Add(e relay.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[e.ID.String()] = e
}

func (m *Memory) Fetch(ctx context.Context, batchSize int) ([]relay.Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []relay.Event
	for _, e := range m.events {
		if e.Retries > 5 {
			continue
		}
		results = append(results, e)
		if len(results) >= batchSize {
			break
		}
	}
	return results, nil
}

func (m *Memory) MarkDone(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	event, ok := m.events[id]
	if ok{
		event.Status = "completed"
		m.events[id] = event
	}
	delete(m.events, id) 
	return nil
}

func (m *Memory) MarkFailed(ctx context.Context, id string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	event, ok := m.events[id]
	if ok{
		event.Retries = event.Retries + 1
		event.LastError = reason
		m.events[id] = event
	}
	delete(m.events, id)
	return nil
}

func (m *Memory) GetStats(ctx context.Context) (relay.Stats, error) {

	var stats relay.Stats
    
    for _, event := range m.events {
        switch {
        case event.Retries == 0:
            stats.Pending++
        case event.Retries > 0 && event.Retries < 5:
            stats.Retrying++
		case event.Retries >= 5:
			stats.Failed++
        }
    }    
    return stats, nil
}