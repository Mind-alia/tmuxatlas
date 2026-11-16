package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"golang.org/x/crypto/bcrypt"
)

type accountContextKey struct{}

type AccountIdentity struct {
	User  accounts.User
	Token string
	Done  <-chan struct{}
}

// WithAccount returns a context carrying the signed-in account.
func WithAccount(ctx context.Context, identity AccountIdentity) context.Context {
	return context.WithValue(ctx, accountContextKey{}, identity)
}

func AccountFromContext(ctx context.Context) (AccountIdentity, bool) {
	identity, ok := ctx.Value(accountContextKey{}).(AccountIdentity)
	return identity, ok
}

const (
	userFreeFailures = 10 // failures on one username, all addresses together
	userFailureCap   = 20 // from there, 5 minutes between attempts
)

type loginFailures struct {
	count int
	until time.Time
	last  time.Time
}

type AccountsAuth struct {
	Store     *accounts.Store
	Sessions  *SessionManager
	lifecycle sync.RWMutex
	mu        sync.Mutex
	failures  map[string]loginFailures
	dummyHash string
}

func NewAccountsAuth(store *accounts.Store, sessions *SessionManager) (*AccountsAuth, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte("atlas-dummy-password"), 12)
	if err != nil {
		return nil, err
	}
	return &AccountsAuth{Store: store, Sessions: sessions, failures: make(map[string]loginFailures), dummyHash: string(hash)}, nil
}

func trustedLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DefaultProxySecretHeader carries the secret shared with the reverse proxy.
const DefaultProxySecretHeader = "X-Tmuxatlas-Proxy-Secret"

// MinProxySecretLength rejects secrets short enough to guess.
const MinProxySecretLength = 32

var trustedProxy struct {
	sync.RWMutex
	header string
	secret string
}

// ConfigureTrustedProxy sets the header and secret that the reverse proxy
// adds to every request (TMUXATLAS_PROXY_SECRET_HEADER, TMUXATLAS_PROXY_SECRET).
// X-Forwarded-For is only believed on requests that carry this secret: any
// local account can reach the loopback listener and forge the header. An
// empty or too short secret disables the trust entirely.
func ConfigureTrustedProxy(header, secret string) error {
	header = strings.TrimSpace(header)
	if header == "" {
		header = DefaultProxySecretHeader
	}
	if secret != "" && len(secret) < MinProxySecretLength {
		trustedProxy.Lock()
		trustedProxy.header, trustedProxy.secret = header, ""
		trustedProxy.Unlock()
		return errors.New("TMUXATLAS_PROXY_SECRET trop court : X-Forwarded-For ignoré")
	}
	trustedProxy.Lock()
	trustedProxy.header, trustedProxy.secret = http.CanonicalHeaderKey(header), secret
	trustedProxy.Unlock()
	return nil
}

// fromTrustedProxy reports whether r comes over loopback and carries the
// proxy secret.
func fromTrustedProxy(r *http.Request) bool {
	if !trustedLoopback(r.RemoteAddr) {
		return false
	}
	trustedProxy.RLock()
	header, secret := trustedProxy.header, trustedProxy.secret
	trustedProxy.RUnlock()
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.Header.Get(header)), []byte(secret)) == 1
}

func accountRequestSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustedLoopback(r.RemoteAddr) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// ClientIP returns the peer address of r, or the client address reported in
// X-Forwarded-For when r comes from the local reverse proxy, proven by the
// shared secret. Without a configured secret the header is never trusted.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if fromTrustedProxy(r) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				break
			}
			host = ip.String()
			if !ip.IsLoopback() {
				break
			}
		}
	}
	return host
}

func accountClientIP(r *http.Request) string { return ClientIP(r) }

func (a *AccountsAuth) blocked(keys ...string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for key, value := range a.failures {
		if now.Sub(value.last) > 30*time.Minute {
			delete(a.failures, key)
		}
	}
	for _, key := range keys {
		if now.Before(a.failures[key].until) {
			return true
		}
	}
	return false
}

func (a *AccountsAuth) failed(keys ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for _, key := range keys {
		entry := a.failures[key]
		entry.count++
		entry.last = now
		// A username is shared by every address: it tolerates more failures
		// before slowing down, so one attacker cannot lock out its owner as
		// fast, but it still caps the global guessing rate.
		free, ceiling := 3, 8
		if strings.HasPrefix(key, "user:") {
			free, ceiling = userFreeFailures, userFailureCap
		}
		if entry.count >= free {
			delay := time.Second << min(entry.count-free, 6)
			if entry.count >= ceiling {
				delay = 5 * time.Minute
			}
			entry.until = now.Add(delay)
		}
		a.failures[key] = entry
	}
}

func (a *AccountsAuth) identity(r *http.Request) (AccountIdentity, bool) {
	token := SessionCookie(r)
	id, done, valid := a.Sessions.AccountSession(token)
	if !valid || id == "" {
		return AccountIdentity{}, false
	}
	users, err := a.Store.List()
	if err != nil {
		return AccountIdentity{}, false
	}
	for _, user := range users {
		if user.ID == id && !user.Disabled {
			return AccountIdentity{User: user, Token: token, Done: done}, true
		}
	}
	a.Sessions.Revoke(token)
	return AccountIdentity{}, false
}

func (a *AccountsAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := a.identity(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentification requise")
			return
		}
		SetSessionCookie(w, identity.Token, a.Sessions.TTL(), accountRequestSecure(r))
		next.ServeHTTP(w, r.WithContext(WithAccount(r.Context(), identity)))
	})
}

func (a *AccountsAuth) Status(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.identity(r)
	result := map[string]any{"mode": "accounts", "authenticated": ok, "username": "", "role": ""}
	if ok {
		result["username"] = identity.User.Username
		result["role"] = identity.User.Role
		result["csrf_token"], _ = a.Sessions.CSRF(identity.Token)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}

// Logout removes the current browser session and expires its cookie using the
// same transport-security decision as login.
func (a *AccountsAuth) Logout(w http.ResponseWriter, r *http.Request) {
	LogoutHandler(a.Sessions, accountRequestSecure(r))(w, r)
}

func (a *AccountsAuth) Login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	ip := ClientIP(r)
	// "user:" bounds guesses on one account from any number of addresses.
	keys := []string{"ip:" + ip, "pair:" + ip + "\x00" + strings.ToLower(request.Username), "user:" + strings.ToLower(request.Username)}
	if a.blocked(keys...) {
		writeError(w, http.StatusTooManyRequests, "trop de tentatives")
		return
	}
	a.lifecycle.RLock()
	defer a.lifecycle.RUnlock()
	users, err := a.Store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	var found *accounts.User
	for i := range users {
		if strings.EqualFold(users[i].Username, request.Username) {
			found = &users[i]
			break
		}
	}
	hash := a.dummyHash
	if found != nil && found.PasswordHash != "" {
		hash = found.PasswordHash
	}
	valid := accounts.VerifyPassword(hash, request.Password)
	// An account still waiting for its invitation has no password: it is
	// compared with the dummy hash for timing, and always rejected.
	if !valid || found == nil || found.Disabled || found.PasswordHash == "" {
		a.failed(keys...)
		writeError(w, http.StatusUnauthorized, "identifiants invalides")
		return
	}
	if cost, err := bcrypt.Cost([]byte(found.PasswordHash)); err == nil && cost != 12 {
		hash, err := accounts.RehashPassword(request.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "erreur interne")
			return
		}
		if err := a.Store.UpgradeHash(found.ID, found.PasswordHash, hash); err != nil {
			writeError(w, http.StatusInternalServerError, "erreur interne")
			return
		}
	}
	token, err := a.Sessions.CreateForAccount(found.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	SetSessionCookie(w, token, a.Sessions.TTL(), accountRequestSecure(r))
	csrf, _ := a.Sessions.CSRF(token)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"authenticated": true, "username": found.Username, "role": found.Role, "csrf_token": csrf})
}

func (a *AccountsAuth) ChangePassword(w http.ResponseWriter, r *http.Request) {
	identity, ok := AccountFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentification requise")
		return
	}
	var request struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	current, valid := a.identity(r)
	if !valid || current.User.ID != identity.User.ID || !accounts.VerifyPassword(current.User.PasswordHash, request.OldPassword) {
		writeError(w, http.StatusUnauthorized, "identifiants invalides")
		return
	}
	hash, err := accounts.HashPassword(request.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.Store.Update(identity.User.ID, identity.User.ID, hash, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	a.Sessions.RevokeAccount(identity.User.ID, identity.Token)
	w.WriteHeader(http.StatusNoContent)
}

// UpdateAccount serializes credential changes with login, so that a successful
// login cannot create a session after the account was disabled or reset.
func (a *AccountsAuth) UpdateAccount(id, actor, hash string, disabled *bool) (accounts.User, error) {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	user, err := a.Store.Update(id, actor, hash, disabled)
	if err == nil && (hash != "" || (disabled != nil && *disabled)) {
		a.Sessions.RevokeAccount(id, "")
	}
	return user, err
}

// DeleteAccount prevents a concurrent login from outliving account deletion.
func (a *AccountsAuth) DeleteAccount(id, actor string) error {
	return a.DeleteAccountWith(id, actor, nil)
}

// DeleteAccountWith prevents a login from racing account deletion, and only
// deletes the record after its sessions have been released successfully.
func (a *AccountsAuth) DeleteAccountWith(id, actor string, beforeDelete func() error) error {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	if err := a.Store.DeleteWith(id, actor, beforeDelete); err != nil {
		return err
	}
	a.Sessions.RevokeAccount(id, "")
	return nil
}
