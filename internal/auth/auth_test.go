package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPasswordSessionAndRateLimit(t *testing.T) {
	password := Random()
	record, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(record, password) || Verify(record, Random()) {
		t.Fatal("scrypt verification")
	}
	changed := *record
	changed.N = 2
	if Verify(&changed, password) {
		t.Fatal("variable scrypt parameters")
	}
	store := New(false, record)
	item, err := store.Login("127.0.0.1", password)
	if err != nil {
		t.Fatal(err)
	}
	cookie := store.Cookie(item.RawToken, PermanentCookieMaxAge)
	if cookie.Name != "__Host-cwb_session" || !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 34560000 {
		t.Fatal("cookie attributes")
	}
	if _, err = store.Login("192.0.2.1", Random()); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err = store.Login("192.0.2.1", password); err == nil || err.Error() != "RATE_LIMITED" {
		t.Fatal("login backoff")
	}
	store.slots <- struct{}{}
	store.slots <- struct{}{}
	if _, err = store.Login("192.0.2.2", password); err == nil || err.Error() != "RATE_LIMITED" {
		t.Fatal("concurrency limit")
	}
	<-store.slots
	<-store.slots
	store.RevokeAll()
}
func TestRevocationClosesRealWebSocket(t *testing.T) {
	store := New(true, nil)
	session := store.AccessSession(Random(), time.Now().Add(time.Hour))
	defer store.RevokeAll()
	upgrader := websocket.Upgrader{}
	registered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		store.Register(session.ID, "connection", func() {
			connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "SESSION_ENDED"), time.Now().Add(time.Second))
			connection.Close()
		})
		close(registered)
		defer connection.Close()
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
	store.Revoke(session.ID)
	socket.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err = socket.ReadMessage()
	if !websocket.IsCloseError(err, 4001) {
		t.Fatal("revocation did not close websocket")
	}
	if store.Get(session.ID) != nil {
		t.Fatal("session retained")
	}
}
func TestAccessJWTAndJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := Random()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/cdn-cgi/access/certs" {
			t.Error("JWKS path")
		}
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}})
	}))
	defer server.Close()
	access := &Access{Issuer: server.URL, Audience: Random(), Client: server.Client()}
	claims := jwt.RegisteredClaims{Issuer: server.URL, Audience: jwt.ClaimStrings{access.Audience}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)), NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute))}
	sign := func(claims jwt.RegisteredClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = kid
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err = access.Validate(sign(claims)); err != nil {
		t.Fatal(err)
	}
	if _, err = access.Validate(sign(claims)); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("JWKS cache")
	}
	for _, kind := range []string{"issuer", "audience", "expired", "nbf"} {
		changed := claims
		switch kind {
		case "issuer":
			changed.Issuer = "https://example.test"
		case "audience":
			changed.Audience = jwt.ClaimStrings{Random()}
		case "expired":
			changed.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		case "nbf":
			changed.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
		}
		if _, err = access.Validate(sign(changed)); err == nil {
			t.Fatal("invalid claims accepted", kind)
		}
	}
	if _, err = access.Validate(Random()); err == nil {
		t.Fatal("invalid token accepted")
	}
	_ = io.Discard
}
func TestAccessRevocationAndSessionExpiry(t *testing.T) {
	store := New(true, nil)
	token := Random()
	session := store.AccessSession(token, time.Now().Add(time.Hour))
	store.Revoke(session.ID)
	if store.AccessSession(token, time.Now().Add(time.Hour)) != nil {
		t.Fatal("revoked access token recreated session")
	}
	short := store.AccessSession(Random(), time.Now().Add(20*time.Millisecond))
	closed := make(chan struct{})
	if !store.Register(short.ID, "expiry", func() { close(closed) }) {
		t.Fatal("expiry registration")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("expiry did not close connection")
	}
	if store.Get(short.ID) != nil {
		t.Fatal("expired session retained")
	}
	store.RevokeAll()
}
func TestOneTimeTicketSecurity(t *testing.T) {
	password := Random()
	record, err := Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	unconfigured := New(false, nil)
	if _, err = unconfigured.IssueTicket(time.Minute); err == nil {
		t.Fatal("ticket issued without password")
	}
	store := New(false, record)
	defer store.RevokeAll()
	ticket, err := store.IssueTicket(time.Minute)
	if err != nil || len(ticket) != 64 {
		t.Fatal("issue ticket", err)
	}
	session, err := store.RedeemTicket("198.51.100.1", ticket)
	if err != nil || session == nil {
		t.Fatal("redeem ticket", err)
	}
	if _, err = store.RedeemTicket("198.51.100.2", ticket); err == nil || err.Error() != "TICKET_REJECTED" {
		t.Fatal("ticket reused")
	}
	if _, err = store.RedeemTicket("198.51.100.2", ticket); err == nil || err.Error() != "RATE_LIMITED" {
		t.Fatal("ticket rate limit")
	}
	expired, err := store.IssueTicket(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err = store.RedeemTicket("198.51.100.3", expired); err == nil {
		t.Fatal("expired ticket accepted")
	}
	revoked, err := store.IssueTicket(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.RevokeAll()
	if _, err = store.RedeemTicket("198.51.100.4", revoked); err == nil {
		t.Fatal("revoked ticket accepted")
	}
}
