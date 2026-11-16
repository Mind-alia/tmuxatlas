package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/state"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"golang.org/x/crypto/bcrypt"
)

func TestAccountPolicyFailsClosed(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Create("alice", string(hash), "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	authenticator, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateForAccount(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(newAccountPolicy(&Options{}).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/preferences", http.StatusNoContent},
		{http.MethodPost, "/api/auth/change-password", http.StatusNoContent},
		{http.MethodGet, "/api/sessions", http.StatusNoContent},
		{http.MethodGet, "/ws/session", http.StatusForbidden},
		{http.MethodGet, "/ws/events", http.StatusNoContent},
		{http.MethodGet, "/api/new-unclassified-route", http.StatusForbidden},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, response.Code, tc.status)
		}
	}
}

func TestAccountPolicyRejectsRevokedRequestWaitingForLock(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.Create("alice", string(hash), "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateForAccount(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy := newAccountPolicy(&Options{})
	identified := make(chan struct{})
	result := make(chan int, 1)
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(identified)
		policy.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(w, r)
	}))
	policy.mu.Lock()
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/api/preferences", nil)
		request.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		result <- response.Code
	}()
	<-identified
	sessions.Revoke(token)
	policy.mu.Unlock()
	if status := <-result; status != http.StatusUnauthorized {
		t.Fatalf("requête révoquée autorisée : %d", status)
	}
}

func TestGuestUsesOnlySessionsOfGrantedHost(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-accounts-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	store := accounts.OpenStore(filepath.Join(dir, "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Create("alice", string(hash), "guest")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Create("bob", string(hash), "guest")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewOwnedSession("alice", "", alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.NewOwnedSession("bob", "", bob.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	local, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	manager := state.NewManager(client)
	peers := peer.NewManager(local, nil, manager)
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateForAccount(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	options := &Options{Client: client, StateMgr: manager, PeerMgr: peers}
	h := a.Middleware(newAccountPolicy(options).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	check := func(want int) {
		t.Helper()
		for _, name := range []string{"alice", "bob"} {
			for _, operation := range []string{"preview", "rename", "kill", "select-window", "select-pane", "reply", "release"} {
				method := http.MethodPost
				url := "/api/session/" + operation
				body := fmt.Sprintf(`{"host_id":%q,"session":%q}`, peers.LocalID(), name)
				if operation == "preview" {
					method = http.MethodGet
					url += "?host_id=" + peers.LocalID() + "&session=" + name
				}
				r := httptest.NewRequest(method, url, strings.NewReader(body))
				r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != want {
					t.Errorf("%s %s: %d, want %d", name, operation, w.Code, want)
				}
			}
			url := "/ws/session?host=" + peers.LocalID() + "&name=" + name
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Errorf("%s pty: %d, want %d", name, w.Code, want)
			}
		}
	}
	// No grant: every session of the host is out of reach.
	check(http.StatusForbidden)
	if _, err := store.SetAccess(alice.ID, []string{peers.LocalID()}, "", nil); err != nil {
		t.Fatal(err)
	}
	// The hub's own host stays forbidden unless explicitly allowed.
	check(http.StatusForbidden)
	options.GuestLocalHost = true
	// On a granted host the owner is a label: every session can be used.
	check(http.StatusNoContent)
	// Atlas's internal session and unknown sessions stay out of reach.
	for _, name := range []string{"tmuxatlas-ctrl", "inexistante"} {
		r := httptest.NewRequest(http.MethodGet, "/ws/session?host="+peers.LocalID()+"&name="+name, nil)
		r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s pty: %d, want 403", name, w.Code)
		}
	}
}

func TestToolEventDeleteCannotTargetAnotherHost(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, _ := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	user, err := store.Create("alice", string(hash), "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateForAccount(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	handler := a.Middleware(newAccountPolicy(&Options{}).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { invoked = true })))
	r := httptest.NewRequest("DELETE", "/api/tool-event", strings.NewReader(`{"host_id":"local","host":"foreign","session":"same"}`))
	r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 400 || invoked {
		t.Fatalf("status=%d handler=%v", w.Code, invoked)
	}
}

// The microphone follows terminal access: a guest may feed the microphone of a
// host granted to them (never the hub's own host, nor a missing host), an
// admin any host.
func TestMicrophoneFollowsTerminalAccess(t *testing.T) {
	dir := t.TempDir()
	store := accounts.OpenStore(filepath.Join(dir, "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Create("admin", string(hash), "admin")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Create("alice", string(hash), "guest")
	if err != nil {
		t.Fatal(err)
	}
	local, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	peers := peer.NewManager(local, nil, state.NewManager(nil))
	const granted, other = "hote-alice", "hote-autre"
	if _, err := store.SetAccess(alice.ID, []string{granted}, "", nil); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	options := &Options{PeerMgr: peers}
	h := a.Middleware(newAccountPolicy(options).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	check := func(account accounts.User, host string, want int) {
		t.Helper()
		token, err := sessions.CreateForAccount(account.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/ws/mic?id=abcdefgh12", "/api/voice?x=1"} {
			url := path
			if host != "" {
				url += "&host=" + host
			}
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Errorf("%s %s host=%q: %d, want %d", account.Username, path, host, w.Code, want)
			}
		}
	}
	check(alice, granted, http.StatusNoContent)
	check(alice, other, http.StatusForbidden)
	check(alice, "", http.StatusForbidden)              // the hub's own microphone
	check(alice, peers.LocalID(), http.StatusForbidden) // same, named
	check(admin, granted, http.StatusNoContent)
	check(admin, other, http.StatusNoContent)
	check(admin, "", http.StatusNoContent)
	// Revoking the host revokes its microphone.
	if _, err := store.SetAccess(alice.ID, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	check(alice, granted, http.StatusForbidden)
	// Same rule as the terminal: the hub's host only with GuestLocalHost.
	if _, err := store.SetAccess(alice.ID, []string{peers.LocalID()}, "", nil); err != nil {
		t.Fatal(err)
	}
	check(alice, peers.LocalID(), http.StatusForbidden)
	options.GuestLocalHost = true
	check(alice, peers.LocalID(), http.StatusNoContent)
}
