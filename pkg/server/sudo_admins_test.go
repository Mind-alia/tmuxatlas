package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

func TestSudoAdminOnly(t *testing.T) {
	// Static lookup: the store is covered by TestSudoAdminsFollowAccountRoles.
	known := map[string]accounts.User{
		"1": {ID: "1", Username: "anovisclaw", Role: "admin"},
		"2": {ID: "2", Username: "invite", Role: "guest"},
		"3": {ID: "3", Role: "admin", Disabled: true},
	}
	lookup := func(id string) (accounts.User, bool) { user, ok := known[id]; return user, ok }
	tests := []struct {
		name    string
		admins  SudoAdmins
		account *accounts.User
		cookie  bool
		status  int
	}{
		{"accounts admin", defaultSudoAdmins{accounts: true, enabled: true, lookup: lookup}, &accounts.User{ID: "1", Username: "anovisclaw", Role: "admin"}, true, http.StatusOK},
		{"accounts user", defaultSudoAdmins{accounts: true, enabled: true, lookup: lookup}, &accounts.User{ID: "2", Username: "invite", Role: "guest"}, true, http.StatusForbidden},
		{"disabled admin", defaultSudoAdmins{accounts: true, enabled: true, lookup: lookup}, &accounts.User{ID: "3", Role: "admin", Disabled: true}, true, http.StatusForbidden},
		{"accounts without identity", defaultSudoAdmins{accounts: true, enabled: true, lookup: lookup}, nil, true, http.StatusForbidden},
		{"passkey owner", defaultSudoAdmins{enabled: true}, nil, true, http.StatusOK},
		{"passkey without session", defaultSudoAdmins{enabled: true}, nil, false, http.StatusForbidden},
		{"no authentication", defaultSudoAdmins{}, nil, true, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/sudo/requests", nil)
			if tt.account != nil {
				request = request.WithContext(auth.WithAccount(request.Context(), auth.AccountIdentity{User: *tt.account}))
			}
			if tt.cookie {
				request.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: "x"})
			}
			response := httptest.NewRecorder()
			sudoAdminOnly(tt.admins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
		})
	}
}

// SudoAdmins follows the admin role of the account store: only a current,
// enabled admin may approve sudo requests or open the sudo terminal. The
// role is re-read from the store, not taken from the request context.
func TestSudoAdminsFollowAccountRoles(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	create := func(name, role string) accounts.User {
		t.Helper()
		user, err := store.Create(name, string(hash), role)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	root, admin, guest, legacy := create("root-admin", "admin"), create("admin", "admin"), create("invite", "guest"), create("ancien", "user")
	admins := newSudoAdmins(&Options{AuthEnabled: true, AccountsAuth: &auth.AccountsAuth{Store: store}})
	check := func(user accounts.User, want bool) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/sudo/requests", nil)
		// The context carries the role seen at login; only the store decides.
		user.Role = accounts.RoleAdmin
		request = request.WithContext(auth.WithAccount(request.Context(), auth.AccountIdentity{User: user}))
		actor, ok := admins.Admin(request)
		if ok != want {
			t.Fatalf("%s: admin = %v, want %v", user.Username, ok, want)
		}
		if ok && (actor.ID != user.ID || actor.Name != user.Username) {
			t.Fatalf("%s: actor = %+v", user.Username, actor)
		}
	}
	check(root, true)
	check(admin, true)
	check(guest, false)
	check(legacy, false) // the old "user" role reads as guest
	check(accounts.User{ID: "inconnu", Username: "inconnu"}, false)

	if _, err := store.SetRole(admin.ID, root.ID, "guest"); err != nil {
		t.Fatal(err)
	}
	check(admin, false) // demoted: refused at once
	if _, err := store.SetRole(guest.ID, root.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	check(guest, true) // promoted
	disabled := true
	if _, err := store.Update(guest.ID, root.ID, "", &disabled); err != nil {
		t.Fatal(err)
	}
	check(guest, false) // disabled admin
}
