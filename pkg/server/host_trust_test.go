package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/state"
	"golang.org/x/crypto/bcrypt"
)

func TestHostTrustListsOnlyHubAndPinnedSudoPeers(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, _ := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	admin, _ := store.Create("admin", string(hash), "admin")
	alice, _ := store.Create("alice", string(hash), "user")
	local, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	peers := peer.NewManager(local, nil, state.NewManager(nil))
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	options := &Options{PeerMgr: peers, SudoPeers: "sudo-admin, sudo-autre"}
	policy := newAccountPolicy(options)
	h := a.Middleware(policy.Middleware(handleHostTrust(options, policy)))
	get := func(account accounts.User) hostTrust {
		t.Helper()
		token, _ := sessions.CreateForAccount(account.ID)
		r := httptest.NewRequest(http.MethodGet, "/api/hosts/trust", nil)
		r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", account.Username, w.Code)
		}
		var trust hostTrust
		if err := json.Unmarshal(w.Body.Bytes(), &trust); err != nil {
			t.Fatal(err)
		}
		return trust
	}
	if got, want := get(admin), (hostTrust{Hub: peers.LocalID(), Local: true, Trusted: []string{peers.LocalID(), "sudo-admin", "sudo-autre"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("admin: %+v, want %+v", got, want)
	}
	// An account that is not an admin learns only that the hub is trusted, not
	// which peers open sudo terminals.
	if got, want := get(alice), (hostTrust{Hub: peers.LocalID(), Local: true, Trusted: []string{peers.LocalID()}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("user: %+v, want %+v", got, want)
	}
}
