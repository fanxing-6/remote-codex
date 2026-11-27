package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"remote-codex/internal/config"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func loadStore(t *testing.T, path string, record *config.Password) *Store {
	t.Helper()
	s := New(false, record)
	if err := s.SetStatePath(path); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadState(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func stateOnDisk(t *testing.T, path string, secrets ...string) savedState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal("state permissions", err)
	}
	for _, raw := range secrets {
		if raw != "" && bytes.Contains(data, []byte(raw)) {
			t.Fatal("raw credential persisted")
		}
	}
	var state savedState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	for _, item := range state.Sessions {
		if !validHash(item.Hash) || !item.Permanent {
			t.Fatal("session state")
		}
	}
	for _, item := range state.Tickets {
		if !validHash(item.Hash) || !item.Permanent {
			t.Fatal("ticket state")
		}
	}
	return state
}
func requestSession(s *Store, raw string) *Session {
	r := httptest.NewRequest("GET", "https://example.test/api/session", nil)
	r.AddCookie(&http.Cookie{Name: s.CookieName(), Value: raw})
	return s.FromRequest(r)
}
func TestPermanentSessionAndReusableTicketReload(t *testing.T) {
	password := Random()
	record, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	s := loadStore(t, path, record)
	login, err := s.Login("127.0.0.1", password)
	if err != nil {
		t.Fatal(err)
	}
	if !login.Permanent || !login.Expires.IsZero() || login.ID != tokenHash(login.RawToken) || s.sessions[login.ID].timer != nil || s.sessions[login.ID].RawToken != "" {
		t.Fatal("permanent session identity")
	}
	if s.Get(login.RawToken) != nil || requestSession(s, login.ID) != nil {
		t.Fatal("digest accepted as raw credential")
	}
	ticket, err := s.IssuePermanentTicket()
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := s.IssueTicket(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	access := s.AccessSession(Random(), time.Now().Add(time.Hour))
	state := stateOnDisk(t, path, login.RawToken, ticket, temporary, password)
	if len(state.Sessions) != 1 || len(state.Tickets) != 1 || state.Tickets[0].Hash != tokenHash(ticket) {
		t.Fatal("persistent scope")
	}
	s.Close()
	s = loadStore(t, path, record)
	loaded := requestSession(s, login.RawToken)
	if loaded == nil || loaded.ID != login.ID || loaded.CSRF != login.CSRF || !loaded.CreatedAt.Equal(login.CreatedAt) || loaded.RawToken != "" || !s.Register(loaded.ID, "connection", func() {}) {
		t.Fatal("session reload")
	}
	s.Unregister(loaded.ID, "connection")
	if s.Get(access.ID) != nil {
		t.Fatal("access session persisted")
	}
	if _, err = s.RedeemTicket("temporary", temporary); err == nil {
		t.Fatal("temporary ticket persisted")
	}
	for i := 0; i < 2; i++ {
		session, err := s.RedeemTicket("permanent", ticket)
		if err != nil || session == nil || !session.Permanent || requestSession(s, session.RawToken) == nil {
			t.Fatal("reusable ticket", err)
		}
		stateOnDisk(t, path, ticket, session.RawToken, login.RawToken)
	}
	s.Close()
	s = loadStore(t, path, record)
	if _, err = s.RedeemTicket("reload", ticket); err != nil {
		t.Fatal(err)
	}
}
func TestSingleRevocationPersists(t *testing.T) {
	record, err := Hash(Random())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	s := loadStore(t, path, record)
	ticket, err := s.IssuePermanentTicket()
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.RedeemTicket("first", ticket)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RedeemTicket("second", ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Revoke(first.ID); err != nil {
		t.Fatal(err)
	}
	state := stateOnDisk(t, path, first.RawToken, second.RawToken, ticket)
	if len(state.Sessions) != 1 || len(state.Tickets) != 1 {
		t.Fatal("single revocation scope")
	}
	s.Close()
	s = loadStore(t, path, record)
	if requestSession(s, first.RawToken) != nil || requestSession(s, second.RawToken) == nil {
		t.Fatal("single revocation reload")
	}
}
func TestAllRevocationAndPasswordChangePersist(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoke-all", true: "password"}[change], func(t *testing.T) {
			record, err := Hash(Random())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "auth-state.json")
			s := loadStore(t, path, record)
			permanent, err := s.IssuePermanentTicket()
			if err != nil {
				t.Fatal(err)
			}
			temporary, err := s.IssueTicket(time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			public, err := s.RedeemTicket("public", permanent)
			if err != nil {
				t.Fatal(err)
			}
			accessRaw := Random()
			access := s.AccessSession(accessRaw, time.Now().Add(time.Hour))
			var closed atomic.Int32
			for _, session := range []*Session{public, access} {
				if !s.Register(session.ID, "connection", func() { closed.Add(1) }) {
					t.Fatal("registration")
				}
			}
			if change {
				record, err = Hash(Random())
				if err != nil {
					t.Fatal(err)
				}
				err = s.ChangePassword(record)
			} else {
				err = s.RevokeAll()
			}
			if err != nil {
				t.Fatal(err)
			}
			if closed.Load() != 2 || s.Count() != 0 || s.AccessSession(accessRaw, access.Expires) != nil {
				t.Fatal("active revocation")
			}
			state := stateOnDisk(t, path, public.RawToken, permanent, temporary)
			if len(state.Sessions) != 0 || len(state.Tickets) != 0 {
				t.Fatal("state not empty")
			}
			if _, err = s.RedeemTicket("temporary", temporary); err == nil {
				t.Fatal("temporary retained")
			}
			s.Close()
			s = loadStore(t, path, record)
			if requestSession(s, public.RawToken) != nil {
				t.Fatal("session revived")
			}
			if _, err = s.RedeemTicket("permanent", permanent); err == nil {
				t.Fatal("ticket revived")
			}
		})
	}
}
func TestTicketAtomicConsumptionAndPermanentLimit(t *testing.T) {
	record, err := Hash(Random())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	s := loadStore(t, path, record)
	temporary, err := s.IssueTicket(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := s.RedeemTicket(Random(), temporary); err == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatal("non-atomic ticket consumption")
	}
	var oldest, newest string
	for i := 0; i < 17; i++ {
		newest, err = s.IssuePermanentTicket()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldest = newest
		}
	}
	s.Close()
	s = loadStore(t, path, record)
	if len(s.permanentTickets) != 16 {
		t.Fatal("permanent ticket limit")
	}
	if _, err = s.RedeemTicket("oldest", oldest); err == nil {
		t.Fatal("oldest ticket retained")
	}
	if _, err = s.RedeemTicket("newest", newest); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemTicket("newest", newest); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentTicketBackoffAndPasswordRequirement(t *testing.T) {
	withoutPassword := New(false, nil)
	defer withoutPassword.Close()
	if _, err := withoutPassword.IssuePermanentTicket(); err == nil {
		t.Fatal("permanent ticket without password")
	}
	record, err := Hash(Random())
	if err != nil {
		t.Fatal(err)
	}
	s := loadStore(t, filepath.Join(t.TempDir(), "auth-state.json"), record)
	ticket, err := s.IssuePermanentTicket()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemTicket("limited", Random()); err == nil || err.Error() != "TICKET_REJECTED" {
		t.Fatal("invalid ticket accepted")
	}
	if _, err = s.RedeemTicket("limited", ticket); err == nil || err.Error() != "RATE_LIMITED" {
		t.Fatal("permanent ticket bypassed backoff")
	}
	if _, err = s.RedeemTicket("other-device", ticket); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemTicket("other-device", ticket); err != nil {
		t.Fatal(err)
	}
}
func TestMemoryOnlyAdminAndAccess(t *testing.T) {
	s := NewNamed("cwb_admin")
	defer s.Close()
	if err := s.SetStatePath(filepath.Join(t.TempDir(), "auth-state.json")); err == nil {
		t.Fatal("admin persistence allowed")
	}
	session := s.CookieSession(time.Now().Add(time.Hour))
	if session.Permanent || session.Expires.IsZero() || session.RawToken == session.ID || requestSession(s, session.RawToken) == nil || requestSession(s, session.ID) != nil {
		t.Fatal("admin identity")
	}
	short := s.CookieSession(time.Now().Add(30 * time.Millisecond))
	closed := make(chan struct{})
	if !s.Register(short.ID, "expiry", func() { close(closed) }) {
		t.Fatal("admin registration")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("admin expiry")
	}
	if requestSession(s, short.RawToken) != nil || s.Register(short.ID, "expired", func() {}) {
		t.Fatal("expired admin accepted")
	}
}
func TestPersistenceFailuresRejectIssuance(t *testing.T) {
	password := Random()
	record, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth-state.json")
	s := loadStore(t, path, record)
	if err = os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if session, err := s.Login("login", password); err == nil || session != nil || s.Count() != 0 {
		t.Fatal("login ignored write failure")
	}
	if raw, err := s.IssuePermanentTicket(); err == nil || raw != "" || len(s.permanentTickets) != 0 {
		t.Fatal("ticket ignored write failure")
	}
	if err = s.LoadState(); err == nil {
		t.Fatal("invalid state accepted")
	}
}

func TestPermanentRevocationClosesWebSocket(t *testing.T) {
	for _, action := range []string{"single", "all", "password"} {
		t.Run(action, func(t *testing.T) {
			record, err := Hash(Random())
			if err != nil {
				t.Fatal(err)
			}
			s := loadStore(t, filepath.Join(t.TempDir(), "auth-state.json"), record)
			ticket, err := s.IssuePermanentTicket()
			if err != nil {
				t.Fatal(err)
			}
			session, err := s.RedeemTicket("device", ticket)
			if err != nil {
				t.Fatal(err)
			}
			registered := make(chan struct{})
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer connection.Close()
				if !s.Register(session.ID, "socket", func() {
					connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "SESSION_ENDED"), time.Now().Add(time.Second))
					connection.Close()
				}) {
					t.Error("registration")
					return
				}
				close(registered)
				defer s.Unregister(session.ID, "socket")
				for {
					if _, _, err = connection.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			socket, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[4:], nil)
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			<-registered
			switch action {
			case "single":
				err = s.Revoke(session.ID)
			case "all":
				err = s.RevokeAll()
			case "password":
				updated, hashErr := Hash(Random())
				if hashErr != nil {
					t.Fatal(hashErr)
				}
				err = s.ChangePassword(updated)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = socket.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			_, _, err = socket.ReadMessage()
			if !websocket.IsCloseError(err, 4001) {
				t.Fatal("permanent session socket remained connected", err)
			}
		})
	}
}
