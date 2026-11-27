package controller

import (
	"errors"
	"remote-codex/internal/protocol"
	"sync"
)

type Manager struct {
	mu         sync.Mutex
	generation uint64
	owner      string
	close      func(int)
	reset      func(uint64) error
}

func New(initialGeneration uint64, reset func(uint64) error) *Manager {
	return &Manager{generation: initialGeneration, reset: reset}
}
func (m *Manager) Acquire(id string, takeover bool, close func(int)) (uint64, error) {
	return m.acquire(id, takeover, "", close)
}
func (m *Manager) Resume(expectedOwner, id string, close func(int)) (uint64, error) {
	return m.acquire(id, true, expectedOwner, close)
}
func (m *Manager) acquire(id string, takeover bool, expectedOwner string, close func(int)) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if expectedOwner != "" && m.owner != "" && m.owner != expectedOwner {
		return 0, errors.New("CONTROLLER_OCCUPIED")
	}
	if m.owner != "" && !takeover {
		return 0, errors.New("CONTROLLER_OCCUPIED")
	}
	previous := m.close
	m.owner = ""
	m.close = nil
	m.generation++
	if previous != nil {
		previous(protocol.TakenOver)
	}
	if err := m.reset(m.generation); err != nil {
		return 0, err
	}
	m.owner = id
	m.close = close
	return m.generation, nil
}
func (m *Manager) Release(id string, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.owner || generation != m.generation {
		return nil
	}
	m.owner = ""
	m.close = nil
	m.generation++
	return m.reset(m.generation)
}
func (m *Manager) With(id string, generation uint64, fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owner != id || m.generation != generation {
		return errors.New("STALE_GENERATION")
	}
	return fn()
}
func (m *Manager) Status() (string, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner, m.generation
}
func (m *Manager) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	close := m.close
	m.owner = ""
	m.close = nil
	m.generation++
	if close != nil {
		close(protocol.TakenOver)
	}
	return m.reset(m.generation)
}
