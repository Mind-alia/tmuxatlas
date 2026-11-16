package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
)

func TestAccountsLoginStatusAndPasswordChange(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := accounts.HashPassword("mot de passe initial")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Create("alice", hash, "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionManager(time.Hour)
	a, err := NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	login := func(name, password string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"username": name, "password": password})
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
		r.RemoteAddr = "127.0.0.1:3000"
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		a.Login(w, r)
		return w
	}
	wrong := login("inconnu", "mot de passe initial")
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected failed login: %d", wrong.Code)
	}
	response := login("ALICE", "mot de passe initial")
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure cookie")
	}
	other, err := sessions.CreateForAccount(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, done, _ := sessions.AccountSession(other)
	status := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	r.AddCookie(cookie)
	a.Status(status, r)
	if !strings.Contains(status.Body.String(), `"username":"alice"`) || strings.Contains(status.Body.String(), hash) {
		t.Fatalf("status: %s", status.Body.String())
	}
	change := httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(`{"old_password":"mot de passe initial","new_password":"nouveau mot de passe"}`))
	r.AddCookie(cookie)
	a.Middleware(http.HandlerFunc(a.ChangePassword)).ServeHTTP(change, r)
	if change.Code != http.StatusNoContent || !sessions.Validate(cookie.Value) || sessions.Validate(other) {
		t.Fatalf("password change: %d", change.Code)
	}
	select {
	case <-done:
	default:
		t.Fatal("other session was not actively revoked")
	}
}

func TestForwardedForNeedsTheProxySecret(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	t.Cleanup(func() { _ = ConfigureTrustedProxy("", "") })
	request := func(remote, header, value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-For", "203.0.113.1")
		if header != "" {
			r.Header.Set(header, value)
		}
		return r
	}
	// Without a secret, even loopback (any local account) is not believed.
	_ = ConfigureTrustedProxy("", "")
	if ip := ClientIP(request("127.0.0.1:1234", "", "")); ip != "127.0.0.1" {
		t.Fatalf("forged X-Forwarded-For from loopback trusted: %s", ip)
	}
	if ip := ClientIP(request("192.0.2.8:1234", "", "")); ip != "192.0.2.8" {
		t.Fatalf("X-Forwarded-For trusted from a remote host: %s", ip)
	}
	if err := ConfigureTrustedProxy("", "court"); err == nil {
		t.Fatal("short secret accepted")
	}
	if ip := ClientIP(request("127.0.0.1:1234", DefaultProxySecretHeader, "court")); ip != "127.0.0.1" {
		t.Fatalf("short secret enabled trust: %s", ip)
	}
	if err := ConfigureTrustedProxy("X-Caddy-Secret", secret); err != nil {
		t.Fatal(err)
	}
	if ip := ClientIP(request("127.0.0.1:1234", "X-Caddy-Secret", secret)); ip != "203.0.113.1" {
		t.Fatalf("proxy with the secret not believed: %s", ip)
	}
	if ip := ClientIP(request("192.0.2.8:1234", "X-Caddy-Secret", secret)); ip != "192.0.2.8" {
		t.Fatalf("secret from a remote host believed: %s", ip)
	}
	for _, value := range []string{"", "mauvais", secret[:31]} {
		if ip := ClientIP(request("127.0.0.1:1234", "X-Caddy-Secret", value)); ip != "127.0.0.1" {
			t.Fatalf("wrong secret %q believed: %s", value, ip)
		}
	}
	if ip := ClientIP(request("127.0.0.1:1234", DefaultProxySecretHeader, secret)); ip != "127.0.0.1" {
		t.Fatalf("secret in another header believed: %s", ip)
	}
}

func TestAccountProxyProtoRequiresLoopback(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.8:1234"
	r.Header.Set("X-Forwarded-Proto", "https")
	if accountRequestSecure(r) {
		t.Fatal("trusted X-Forwarded-Proto from remote host")
	}
	r.RemoteAddr = "127.0.0.1:1234"
	if !accountRequestSecure(r) {
		t.Fatal("ignored X-Forwarded-Proto from the local proxy")
	}
}

func TestLoginLimitsGuessesPerUsernameAcrossAddresses(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("le bon mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("admin", string(hash), "admin"); err != nil {
		t.Fatal(err)
	}
	a, err := NewAccountsAuth(store, NewSessionManager(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	login := func(remote, name, password string) int {
		body, _ := json.Marshal(map[string]string{"username": name, "password": password})
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		a.Login(w, r)
		return w.Code
	}
	// Each guess from a new address: the per-IP limits never trigger.
	for i := range userFreeFailures {
		if code := login(fmt.Sprintf("198.51.100.%d:4000", i+1), "Admin", "essai"); code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, code)
		}
	}
	if code := login("198.51.100.200:4000", "admin", "le bon mot de passe"); code != http.StatusTooManyRequests {
		t.Fatalf("username not limited across addresses: %d", code)
	}
}

func TestAdminAccountChangesRevokeSessions(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := accounts.HashPassword("mot de passe initial")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Create("alice", hash, "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionManager(time.Hour)
	a, err := NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateForAccount(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, done, _ := sessions.AccountSession(token)
	disabled := true
	if _, err := a.UpdateAccount(user.ID, "admin", "", &disabled); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("disabling an account did not close its sessions")
	}
	if sessions.Validate(token) {
		t.Fatal("disabled account session is still valid")
	}
	if err := a.DeleteAccount(user.ID, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestPendingAccountCannotSignInWithDummyPassword(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.CreatePending("alice", accounts.RoleGuest, ""); err != nil {
		t.Fatal(err)
	}
	a, err := NewAccountsAuth(store, NewSessionManager(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"atlas-dummy-password", ""} {
		body := strings.NewReader(`{"username":"alice","password":"` + password + `"}`)
		recorder := httptest.NewRecorder()
		a.Login(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", body))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("pending account signed in with %q: %d", password, recorder.Code)
		}
	}
}
