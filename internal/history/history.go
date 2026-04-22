// Package history provides an in-memory store of API request history.
package history

import (
	"sync"
	"time"
)

// EntryState describes the lifecycle state of a request.
type EntryState string

const (
	StateActive    EntryState = "active"
	StateComplete  EntryState = "complete"
	StateFailed    EntryState = "failed"
	StateStreaming EntryState = "streaming"
)

// Entry is a single recorded request.
type Entry struct {
	ID         string     `json:"id"`
	Endpoint   string     `json:"endpoint"`
	Model      string     `json:"model"`
	Stream     bool       `json:"stream"`
	State      EntryState `json:"state"`
	StartTime  time.Time  `json:"startTime"`
	EndTime    *time.Time `json:"endTime,omitempty"`
	DurationMs *int64     `json:"durationMs,omitempty"`
	Error      string     `json:"error,omitempty"`

	// Token usage
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`

	// Raw request/response for inspection
	RequestPayload  interface{} `json:"requestPayload,omitempty"`
	ResponsePayload interface{} `json:"responsePayload,omitempty"`
}

// Store holds the in-memory list of entries.
type Store struct {
	mu      sync.RWMutex
	entries []*Entry
	maxSize int
}

var globalStore = &Store{maxSize: 200}

// Global returns the singleton history store.
func Global() *Store {
	return globalStore
}

// SetMaxSize updates the maximum number of entries retained.
func (s *Store) SetMaxSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSize = n
}

// Add inserts a new entry and evicts old entries if over limit.
func (s *Store) Add(e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	if s.maxSize > 0 && len(s.entries) > s.maxSize {
		s.entries = s.entries[len(s.entries)-s.maxSize:]
	}
}

// Update applies fn to the entry with the given ID.
func (s *Store) Update(id string, fn func(*Entry)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			fn(e)
			return
		}
	}
}

// GetAll returns a copy of all entries (newest last).
func (s *Store) GetAll() []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// GetByID returns the entry with the given ID, or nil.
func (s *Store) GetByID(id string) *Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// DeleteAll removes all entries.
func (s *Store) DeleteAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
}

// Count returns the number of stored entries.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Sessions returns a deduplicated list of session IDs.
func (s *Store) Sessions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var sessions []string
	for _, e := range s.entries {
		if e.Endpoint != "" && !seen[e.Endpoint] {
			seen[e.Endpoint] = true
			sessions = append(sessions, e.Endpoint)
		}
	}
	return sessions
}

// Stats returns aggregate token statistics.
type Stats struct {
	TotalRequests int `json:"totalRequests"`
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
}

// GetStats computes aggregate stats from all entries.
func (s *Store) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var stats Stats
	for _, e := range s.entries {
		stats.TotalRequests++
		stats.InputTokens += e.InputTokens
		stats.OutputTokens += e.OutputTokens
	}
	return stats
}
