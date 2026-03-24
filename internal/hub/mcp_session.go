package hub

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const mcpSessionTTL = 30 * time.Minute

// mcpSessionStore tracks Streamable HTTP MCP sessions (Mcp-Session-Id) after initialize.
type mcpSessionStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newMCPSessionStore() *mcpSessionStore {
	return &mcpSessionStore{m: make(map[string]time.Time)}
}

func (s *mcpSessionStore) pruneLocked() {
	cutoff := time.Now().Add(-mcpSessionTTL)
	for id, t := range s.m {
		if t.Before(cutoff) {
			delete(s.m, id)
		}
	}
}

func randomSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create allocates a new session id.
func (s *mcpSessionStore) Create() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	id := randomSessionID()
	s.m[id] = time.Now()
	return id
}

// Valid reports whether the session id is active.
func (s *mcpSessionStore) Valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	t, ok := s.m[id]
	if !ok {
		return false
	}
	if time.Since(t) > mcpSessionTTL {
		delete(s.m, id)
		return false
	}
	s.m[id] = time.Now() // slide expiry
	return true
}
