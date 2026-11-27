package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"remote-codex/internal/config"
	"time"
)

const PermanentCookieMaxAge = 34560000

type savedSession struct {
	Hash      string    `json:"hash"`
	CSRF      string    `json:"csrf"`
	CreatedAt time.Time `json:"createdAt"`
	Permanent bool      `json:"permanent"`
}
type savedTicket struct {
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	Permanent bool      `json:"permanent"`
}
type savedState struct {
	Version  int            `json:"version"`
	Sessions []savedSession `json:"sessions"`
	Tickets  []savedTicket  `json:"tickets"`
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func validHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}
func copySession(item *Session) *Session {
	return &Session{ID: item.ID, CSRF: item.CSRF, Expires: item.Expires, CreatedAt: item.CreatedAt, Permanent: item.Permanent}
}
func (s *Store) SetStatePath(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.name != "" || len(s.sessions) != 0 || len(s.permanentTickets) != 0 {
		return errors.New("STATE_PATH_REQUIRES_EMPTY_PUBLIC_STORE")
	}
	s.statePath = path
	return nil
}
func (s *Store) LoadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statePath == "" {
		return nil
	}
	if len(s.sessions) != 0 || len(s.permanentTickets) != 0 {
		return errors.New("STATE_ALREADY_LOADED")
	}
	file, err := os.Open(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("AUTH_STATE_TOO_LARGE")
	}
	var state savedState
	if err = json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != 1 || len(state.Sessions) > 1024 || len(state.Tickets) > 16 {
		return errors.New("AUTH_STATE_INVALID")
	}
	sessions := map[string]*Session{}
	tickets := map[string]time.Time{}
	for _, item := range state.Sessions {
		if !validHash(item.Hash) || !validHash(item.CSRF) || !item.Permanent || item.CreatedAt.IsZero() || sessions[item.Hash] != nil {
			return errors.New("AUTH_SESSION_INVALID")
		}
		sessions[item.Hash] = &Session{ID: item.Hash, CSRF: item.CSRF, CreatedAt: item.CreatedAt, Permanent: true, closers: map[string]func(){}}
	}
	for _, item := range state.Tickets {
		if !validHash(item.Hash) || !item.Permanent || item.CreatedAt.IsZero() {
			return errors.New("AUTH_TICKET_INVALID")
		}
		if _, exists := tickets[item.Hash]; exists {
			return errors.New("AUTH_TICKET_DUPLICATE")
		}
		tickets[item.Hash] = item.CreatedAt
	}
	if s.password != nil {
		s.sessions, s.permanentTickets = sessions, tickets
	}
	return os.Chmod(s.statePath, 0600)
}
func (s *Store) persistLocked() error {
	if s.statePath == "" {
		return nil
	}
	state := savedState{Version: 1, Sessions: []savedSession{}, Tickets: []savedTicket{}}
	for hash, item := range s.sessions {
		if item.Permanent {
			state.Sessions = append(state.Sessions, savedSession{Hash: hash, CSRF: item.CSRF, CreatedAt: item.CreatedAt, Permanent: true})
		}
	}
	for hash, created := range s.permanentTickets {
		state.Tickets = append(state.Tickets, savedTicket{Hash: hash, CreatedAt: created, Permanent: true})
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(s.statePath, data, 0600)
}
func (s *Store) createPermanentLocked() (*Session, error) {
	if len(s.sessions) >= 1024 {
		return nil, errors.New("SESSION_LIMIT")
	}
	raw := Random()
	id := tokenHash(raw)
	item := &Session{ID: id, CSRF: Random(), CreatedAt: time.Now(), Permanent: true, closers: map[string]func(){}}
	s.sessions[id] = item
	if err := s.persistLocked(); err != nil {
		delete(s.sessions, id)
		return nil, err
	}
	result := copySession(item)
	result.RawToken = raw
	return result, nil
}
func (s *Store) IssuePermanentTicket() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.password == nil {
		return "", errors.New("PASSWORD_NOT_CONFIGURED")
	}
	var oldest string
	var created time.Time
	if len(s.permanentTickets) >= 16 {
		for hash, at := range s.permanentTickets {
			if oldest == "" || at.Before(created) {
				oldest, created = hash, at
			}
		}
		delete(s.permanentTickets, oldest)
	}
	raw := Random()
	id := tokenHash(raw)
	s.permanentTickets[id] = time.Now()
	if err := s.persistLocked(); err != nil {
		delete(s.permanentTickets, id)
		if oldest != "" {
			s.permanentTickets[oldest] = created
		}
		return "", err
	}
	return raw, nil
}
func stopSessions(items map[string]*Session) []func() {
	var closers []func()
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.timer != nil {
			item.timer.Stop()
		}
		for _, close := range item.closers {
			closers = append(closers, close)
		}
	}
	return closers
}

// Close 关闭进程内连接，持久化认证状态供下次启动读取。
func (s *Store) Close() {
	s.mu.Lock()
	closers := stopSessions(s.sessions)
	s.sessions = map[string]*Session{}
	s.mu.Unlock()
	for _, close := range closers {
		close()
	}
}
