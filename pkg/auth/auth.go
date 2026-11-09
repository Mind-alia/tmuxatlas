package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SessionManager manages session tokens with expiry, in memory unless PersistTo
// gives it a file.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]session
	ttl      time.Duration
	path     string
	savedAt  time.Time
	wake     chan struct{}
	stopped  chan struct{}
	writeMu  sync.Mutex
}

type session struct {
	expiry     time.Time
	issued     time.Time
	csrf       string
	accountID  string
	credential string
	done       chan struct{}
}

// SessionInfo describes a session to Retain, without its token.
type SessionInfo struct {
	AccountID  string
	Credential string
	Issued     time.Time
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{sessions: make(map[string]session), ttl: ttl}
}

func (sm *SessionManager) TTL() time.Duration {
	return sm.ttl
}

func (sm *SessionManager) Create() (string, error) {
	return sm.CreateForAccount("")
}

func sessionKey(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

// CreateForAccount binds a browser session to an account without storing its bearer token.
func (sm *SessionManager) CreateForAccount(accountID string) (string, error) {
	return sm.create(accountID, "")
}

// CreateForCredential binds a browser session to the passkey that opened it, so
// that deleting the passkey ends the session.
func (sm *SessionManager) CreateForCredential(credential string) (string, error) {
	return sm.create("", credential)
}

func (sm *SessionManager) create(accountID, credential string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	csrfBytes := make([]byte, 32)
	if _, err := rand.Read(csrfBytes); err != nil {
		return "", err
	}
	sm.mu.Lock()
	key := sessionKey(token)
	now := time.Now()
	sm.sessions[key] = session{
		expiry: now.Add(sm.ttl), issued: now, csrf: hex.EncodeToString(csrfBytes),
		accountID: accountID, credential: credential, done: make(chan struct{}),
	}
	sm.persistLocked()
	sm.mu.Unlock()
	time.AfterFunc(sm.ttl, func() { sm.expire(key) })
	return token, nil
}

// expire rechecks the latest deadline under the session lock before revoking.
func (sm *SessionManager) expire(key string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	value, ok := sm.sessions[key]
	if !ok {
		return
	}
	remaining := time.Until(value.expiry)
	if remaining > 0 {
		time.AfterFunc(remaining, func() { sm.expire(key) })
		return
	}
	delete(sm.sessions, key)
	close(value.done)
	sm.persistLocked()
}

func (sm *SessionManager) Validate(token string) bool {
	_, _, ok := sm.AccountSession(token)
	return ok
}

// AccountSession validates and refreshes a session and exposes its revocation signal.
func (sm *SessionManager) AccountSession(token string) (string, <-chan struct{}, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	key := sessionKey(token)
	value, ok := sm.sessions[key]
	if !ok || time.Now().After(value.expiry) {
		if ok {
			close(value.done)
			delete(sm.sessions, key)
			sm.persistLocked()
		}
		return "", nil, false
	}
	value.expiry = time.Now().Add(sm.ttl)
	sm.sessions[key] = value
	sm.persistRefreshLocked()
	return value.accountID, value.done, true
}

func (sm *SessionManager) CSRF(token string) (string, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	value, ok := sm.sessions[sessionKey(token)]
	if !ok || time.Now().After(value.expiry) {
		return "", false
	}
	return value.csrf, true
}

func (sm *SessionManager) Revoke(token string) {
	sm.mu.Lock()
	key := sessionKey(token)
	if value, ok := sm.sessions[key]; ok {
		close(value.done)
		delete(sm.sessions, key)
		sm.persistLocked()
	}
	sm.mu.Unlock()
}

// RevokeAccount invalidates all sessions for an account except an optional token.
func (sm *SessionManager) RevokeAccount(accountID, except string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	revoked := false
	for key, value := range sm.sessions {
		if value.accountID == accountID && accountID != "" && (except == "" || key != sessionKey(except)) {
			close(value.done)
			delete(sm.sessions, key)
			revoked = true
		}
	}
	if revoked {
		sm.persistLocked()
	}
}

// RevokeCredential ends every session opened with a passkey.
func (sm *SessionManager) RevokeCredential(credential string) {
	if credential == "" {
		return
	}
	sm.Retain(func(info SessionInfo) bool { return info.Credential != credential })
}

// Retain ends every session that keep rejects. At startup it drops restored
// sessions that a change made while TmuxAtlas was stopped has invalidated.
func (sm *SessionManager) Retain(keep func(SessionInfo) bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	removed := false
	for key, value := range sm.sessions {
		if !keep(SessionInfo{AccountID: value.accountID, Credential: value.credential, Issued: value.issued}) {
			close(value.done)
			delete(sm.sessions, key)
			removed = true
		}
	}
	if removed {
		sm.persistLocked()
	}
}

func (sm *SessionManager) Cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	removed := false
	for token, value := range sm.sessions {
		if now.After(value.expiry) {
			close(value.done)
			delete(sm.sessions, token)
			removed = true
		}
	}
	if removed {
		sm.persistLocked()
	}
}

const cookieName = "tmuxatlas_session"

// SessionCookie returns the bearer token from a request, if present.
func SessionCookie(r *http.Request) string {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// SetSessionCookie sets the browser's session cookie.
func SetSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	refreshSessionCookie(w, token, ttl, secure)
}

func isUnixSocket(r *http.Request) bool {
	addr := r.Context().Value(http.LocalAddrContextKey)
	if addr == nil {
		return false
	}
	_, ok := addr.(*net.UnixAddr)
	return ok
}

// Middleware enforces a browser session. Local CLI requests over the Unix
// socket remain trusted.
func Middleware(sm *SessionManager, secureCookies bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isUnixSocket(r) {
				next.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(cookieName)
			if err != nil || !sm.Validate(cookie.Value) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			refreshSessionCookie(w, cookie.Value, sm.TTL(), secureCookies)
			next.ServeHTTP(w, r)
		})
	}
}

const CSRFHeader = "X-TmuxAtlas-CSRF"

// CSRFMiddleware protects cookie-authenticated browser mutations. It must run
// after Middleware so that the active session has already been validated.
func CSRFMiddleware(sm *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			if err != nil {
				writeError(w, http.StatusForbidden, "csrf validation failed")
				return
			}
			expected, ok := sm.CSRF(cookie.Value)
			provided := r.Header.Get(CSRFHeader)
			if !ok || expected == "" || len(provided) != len(expected) ||
				subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				writeError(w, http.StatusForbidden, "csrf validation failed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func OriginMiddleware(publicURL string) (func(http.Handler) http.Handler, error) {
	expected, err := normalizedOrigin(publicURL)
	if err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin, err := normalizedOrigin(r.Header.Get("Origin"))
			if err != nil || origin != expected {
				writeError(w, http.StatusForbidden, "origin validation failed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

func normalizedOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", errors.New("invalid origin scheme")
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

func refreshSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	maxAge := int((ttl + time.Second - 1) / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode,
		MaxAge: maxAge, Expires: time.Now().Add(ttl),
	})
}

func setSessionCookie(w http.ResponseWriter, sm *SessionManager, credential string, secure bool) error {
	token, err := sm.CreateForCredential(credential)
	if err != nil {
		return err
	}
	refreshSessionCookie(w, token, sm.TTL(), secure)
	return nil
}

func LogoutHandler(sm *SessionManager, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(cookieName); err == nil {
			sm.Revoke(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Path: "/", HttpOnly: true,
			Secure: secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1,
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

func CheckHandler(sm *SessionManager, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil || !sm.Validate(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"authenticated":false}`)
			return
		}
		refreshSessionCookie(w, cookie.Value, sm.TTL(), secureCookies)
		csrf, _ := sm.CSRF(cookie.Value)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"authenticated":true,"csrf_token":%q}`, csrf)
	}
}
