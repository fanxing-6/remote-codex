package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/scrypt"
	"io"
	"net/http"
	"remote-codex/internal/config"
	"sync"
	"sync/atomic"
	"time"
)

func Random() string {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data)
}
func Hash(password string) (*config.Password, error) {
	if len(password) < 16 || len(password) > 1024 {
		return nil, errors.New("PASSWORD_LENGTH_INVALID")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	return &config.Password{Algo: "scrypt-v1", N: 32768, R: 8, P: 1, Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(key)}, nil
}
func Verify(record *config.Password, password string) bool {
	if record == nil || record.Algo != "scrypt-v1" || record.N != 32768 || record.R != 8 || record.P != 1 || len(password) > 1024 {
		return false
	}
	salt, err := hex.DecodeString(record.Salt)
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := hex.DecodeString(record.Hash)
	if err != nil || len(expected) != 32 {
		return false
	}
	actual, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1
}

type Session struct {
	ID        string    `json:"-"`
	RawToken  string    `json:"-"`
	CSRF      string    `json:"csrf"`
	Expires   time.Time `json:"expires"`
	CreatedAt time.Time `json:"createdAt"`
	Permanent bool      `json:"permanent"`
	closers   map[string]func()
	timer     *time.Timer
}
type attempt struct {
	count int
	next  time.Time
}
type Store struct {
	mu               sync.Mutex
	sessions         map[string]*Session
	revoked          map[string]time.Time
	pre              map[string]*Session
	tickets          map[string]time.Time
	permanentTickets map[string]time.Time
	statePath        string
	attempts         map[string]attempt
	slots            chan struct{}
	insecure         atomic.Bool
	password         *config.Password
	name             string
}

func New(insecure bool, password *config.Password) *Store {
	s := &Store{sessions: map[string]*Session{}, revoked: map[string]time.Time{}, pre: map[string]*Session{}, tickets: map[string]time.Time{}, permanentTickets: map[string]time.Time{}, attempts: map[string]attempt{}, slots: make(chan struct{}, 2), password: password}
	s.insecure.Store(insecure)
	return s
}
func (s *Store) SetInsecureLocalhost(enabled bool) { s.insecure.Store(enabled) }
func NewNamed(name string) *Store                  { s := New(true, nil); s.name = name; return s }
func (s *Store) CookieName() string {
	if s.name != "" {
		return s.name
	}
	if s.insecure.Load() {
		return "cwb_session"
	}
	return "__Host-cwb_session"
}
func (s *Store) Cookie(id string, ttl int) *http.Cookie {
	return &http.Cookie{Name: s.CookieName(), Value: id, Path: "/", HttpOnly: true, Secure: !s.insecure.Load(), SameSite: http.SameSiteStrictMode, MaxAge: ttl}
}
func (s *Store) Begin(w http.ResponseWriter) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, item := range s.pre {
		if now.After(item.Expires) {
			delete(s.pre, id)
		}
	}
	if len(s.pre) >= 1024 {
		return ""
	}
	id := Random()
	csrf := Random()
	s.pre[tokenHash(id)] = &Session{ID: tokenHash(id), CSRF: csrf, Expires: now.Add(5 * time.Minute)}
	cookie := s.Cookie(id, 300)
	cookie.Name += "_pre"
	http.SetCookie(w, cookie)
	return csrf
}
func (s *Store) CheckPre(r *http.Request, csrf string) bool {
	cookie, err := r.Cookie(s.CookieName() + "_pre")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.pre[tokenHash(cookie.Value)]
	return item != nil && time.Now().Before(item.Expires) && Equal(item.CSRF, csrf)
}
func Equal(a, b string) bool { return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (s *Store) Login(ip, password string) (*Session, error) {
	s.mu.Lock()
	item := s.attempts[ip]
	record := s.password
	if time.Now().Before(item.next) {
		s.mu.Unlock()
		return nil, errors.New("RATE_LIMITED")
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		return nil, errors.New("RATE_LIMITED")
	}
	s.mu.Unlock()
	valid := Verify(record, password)
	<-s.slots
	s.mu.Lock()
	defer s.mu.Unlock()
	if !valid || record != s.password {
		if len(s.attempts) >= 4096 {
			return nil, errors.New("RATE_LIMITED")
		}
		item.count++
		delay := time.Second * time.Duration(1<<min(item.count-1, 5))
		item.next = time.Now().Add(delay)
		s.attempts[ip] = item
		return nil, errors.New("LOGIN_REJECTED")
	}
	delete(s.attempts, ip)
	return s.createPermanentLocked()
}
func (s *Store) IssueTicket(ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 15*time.Minute {
		ttl = 5 * time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.password == nil {
		return "", errors.New("PASSWORD_NOT_CONFIGURED")
	}
	now := time.Now()
	for hash, expiry := range s.tickets {
		if now.After(expiry) {
			delete(s.tickets, hash)
		}
	}
	if len(s.tickets) >= 16 {
		var oldestKey string
		var oldestTime time.Time
		for hash, expiry := range s.tickets {
			if oldestKey == "" || expiry.Before(oldestTime) {
				oldestKey, oldestTime = hash, expiry
			}
		}
		if oldestKey != "" {
			delete(s.tickets, oldestKey)
		}
	}
	raw := Random()
	sum := sha256.Sum256([]byte(raw))
	s.tickets[hex.EncodeToString(sum[:])] = now.Add(ttl)
	return raw, nil
}
func (s *Store) RedeemTicket(ip, raw string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	item := s.attempts[ip]
	if now.Before(item.next) {
		return nil, errors.New("RATE_LIMITED")
	}
	valid := false
	if s.password != nil && len(raw) == 64 {
		key := tokenHash(raw)
		_, valid = s.permanentTickets[key]
		if expiry, ok := s.tickets[key]; ok {
			delete(s.tickets, key)
			valid = now.Before(expiry)
		}
	}
	if !valid {
		if len(s.attempts) >= 4096 {
			return nil, errors.New("RATE_LIMITED")
		}
		item.count++
		delay := time.Second * time.Duration(1<<min(item.count-1, 5))
		item.next = now.Add(delay)
		s.attempts[ip] = item
		return nil, errors.New("TICKET_REJECTED")
	}
	delete(s.attempts, ip)
	return s.createPermanentLocked()
}
func (s *Store) createLocked(id string, expiry time.Time) *Session {
	if len(s.sessions) >= 1024 {
		return nil
	}
	item := &Session{ID: id, CSRF: Random(), Expires: expiry, CreatedAt: time.Now(), closers: map[string]func(){}}
	s.sessions[id] = item
	item.timer = time.AfterFunc(time.Until(expiry), func() { s.Revoke(id) })
	return copySession(item)
}
func (s *Store) FromRequest(r *http.Request) *Session {
	cookie, err := r.Cookie(s.CookieName())
	if err != nil {
		return nil
	}
	return s.Get(tokenHash(cookie.Value))
}
func (s *Store) Get(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil || (!item.Permanent && time.Now().After(item.Expires)) {
		return nil
	}
	return copySession(item)
}
func (s *Store) CookieSession(expiry time.Time) *Session {
	raw := Random()
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.createLocked(tokenHash(raw), expiry)
	if item != nil {
		item.RawToken = raw
	}
	return item
}
func (s *Store) AccessSession(token string, expiry time.Time) *Session {
	sum := sha256.Sum256([]byte(token))
	id := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, until := range s.revoked {
		if time.Now().After(until) {
			delete(s.revoked, key)
		}
	}
	if _, denied := s.revoked[id]; denied || len(s.revoked) >= 4096 {
		return nil
	}
	if item := s.sessions[id]; item != nil {
		if time.Now().After(item.Expires) {
			return nil
		}
		return copySession(item)
	}
	return s.createLocked(id, expiry)
}
func (s *Store) Register(id, key string, close func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.sessions[id]
	if item == nil || (!item.Permanent && time.Now().After(item.Expires)) {
		return false
	}
	item.closers[key] = close
	return true
}
func (s *Store) Unregister(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item := s.sessions[id]; item != nil {
		delete(item.closers, key)
	}
}
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	item := s.sessions[id]
	if item != nil && !item.Permanent {
		s.revoked[id] = item.Expires
	}
	delete(s.sessions, id)
	var err error
	if item != nil && item.Permanent {
		err = s.persistLocked()
	}
	closers := stopSessions(map[string]*Session{id: item})
	s.mu.Unlock()
	for _, close := range closers {
		close()
	}
	return err
}
func (s *Store) clearLocked() []func() {
	items := s.sessions
	for id, item := range items {
		if !item.Permanent {
			s.revoked[id] = item.Expires
		}
	}
	s.sessions = map[string]*Session{}
	s.tickets = map[string]time.Time{}
	s.permanentTickets = map[string]time.Time{}
	return stopSessions(items)
}
func (s *Store) RevokeAll() error {
	s.mu.Lock()
	closers := s.clearLocked()
	err := s.persistLocked()
	s.mu.Unlock()
	for _, close := range closers {
		close()
	}
	return err
}
func (s *Store) ChangePassword(record *config.Password) error {
	s.mu.Lock()
	s.password = record
	closers := s.clearLocked()
	err := s.persistLocked()
	s.mu.Unlock()
	for _, close := range closers {
		close()
	}
	return err
}
func (s *Store) Count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sessions) }

type Access struct {
	mu               sync.Mutex
	Issuer, Audience string
	Client           *http.Client
	keys             jose.JSONWebKeySet
	until            time.Time
}

func NewAccess(domain, audience string) *Access {
	return &Access{Issuer: "https://" + domain, Audience: audience, Client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("JWKS_REDIRECT_DENIED") }}}
}
func (a *Access) Validate(raw string) (time.Time, error) {
	if a.Issuer == "https://" || a.Audience == "" || len(raw) > 16384 {
		return time.Time{}, errors.New("ACCESS_CONFIG_INVALID")
	}
	claims := jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("JWT_KID_INVALID")
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if time.Now().After(a.until) {
			response, err := a.Client.Get(a.Issuer + "/cdn-cgi/access/certs")
			if err != nil {
				return nil, errors.New("JWKS_UNAVAILABLE")
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				return nil, errors.New("JWKS_STATUS_INVALID")
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
			if err != nil || len(data) > 1<<20 {
				return nil, errors.New("JWKS_SIZE_INVALID")
			}
			var keys jose.JSONWebKeySet
			if err = json.Unmarshal(data, &keys); err != nil || len(keys.Keys) > 32 {
				return nil, errors.New("JWKS_INVALID")
			}
			a.keys = keys
			a.until = time.Now().Add(10 * time.Minute)
		}
		keys := a.keys.Key(kid)
		if len(keys) != 1 || keys[0].Algorithm != "RS256" || !keys[0].Valid() {
			return nil, errors.New("JWT_KEY_INVALID")
		}
		return keys[0].Key, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(a.Issuer), jwt.WithAudience(a.Audience), jwt.WithExpirationRequired())
	if err != nil {
		return time.Time{}, errors.New("ACCESS_REJECTED")
	}
	return claims.ExpiresAt.Time, nil
}
