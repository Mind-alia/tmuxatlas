package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
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

// silentPeer never answers "owner" before the caller gives up.
type silentPeer struct {
	calls   atomic.Int32
	started chan struct{}
}

func (s *silentPeer) Execute(ctx context.Context, _ string, _ peer.SessionTarget, _ json.RawMessage) (peer.ActionResponse, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
	}
	<-ctx.Done()
	return peer.ActionResponse{}, ctx.Err()
}

func TestSilentPeerDoesNotBlockAdminAPI(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, _ := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	admin, _ := store.Create("admin", string(hash), "admin")
	guest, _ := store.Create("invite", string(hash), "guest")
	if _, err := store.SetAccess(guest.ID, []string{"hote-lent"}, "", nil); err != nil {
		t.Fatal(err)
	}
	local, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	policy := newAccountPolicy(&Options{PeerMgr: peer.NewManager(local, nil, state.NewManager(nil))})
	slow := &silentPeer{started: make(chan struct{})}
	policy.router = slow
	h := a.Middleware(policy.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	do := func(account accounts.User, url string) int {
		token, _ := sessions.CreateForAccount(account.ID)
		r := httptest.NewRequest(http.MethodGet, url, nil)
		r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	guestDone := make(chan int, 1)
	go func() { guestDone <- do(guest, "/api/session/preview?host_id=hote-lent&session=claude") }()
	<-slow.started

	start := time.Now()
	if code := do(admin, "/api/admin/users"); code != http.StatusNoContent {
		t.Fatalf("admin: %d", code)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("admin API waited %v for a silent peer", elapsed)
	}
	if code := <-guestDone; code != http.StatusForbidden {
		t.Fatalf("guest on a silent peer: %d, want 403", code)
	}
	// The failed answer is cached: asking again does not call the peer again.
	if code := do(guest, "/api/session/preview?host_id=hote-lent&session=claude"); code != http.StatusForbidden {
		t.Fatalf("guest again: %d", code)
	}
	if calls := slow.calls.Load(); calls != 1 {
		t.Fatalf("owner called %d times, want 1 (cached)", calls)
	}
}

func TestOwnerCacheCoalescesExpiresAndRateLimits(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cache := newOwnerCache(3*time.Second, 1, 2)
	cache.now = func() time.Time { return now }
	calls := 0
	fetch := func() (tmux.Owner, bool) { calls++; return "alice", true }
	for range 3 {
		if owner, ok := cache.get("h", "s", fetch); !ok || owner != "alice" {
			t.Fatal("cached answer lost")
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	now = now.Add(4 * time.Second)
	cache.get("h", "s", fetch)
	if calls != 2 {
		t.Fatalf("expired entry not refreshed: %d", calls)
	}
	// Burst of 2, then 1 per second: other sessions of the same host are refused.
	cache.get("h", "a", fetch)
	if _, ok := cache.get("h", "b", fetch); ok {
		t.Fatal("rate limit not applied")
	}
	if _, ok := cache.get("autre", "b", fetch); !ok {
		t.Fatal("rate limit must be per host")
	}
}
