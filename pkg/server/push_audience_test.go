package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wp "github.com/SherClockHolmes/webpush-go"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/webpush"
)

func TestPushFollowsAccountChanges(t *testing.T) {
	dir := t.TempDir()
	store := accounts.OpenStore(filepath.Join(dir, "users.json"))
	hash, _ := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	admin, _ := store.Create("admin", string(hash), "admin")
	other, _ := store.Create("autre", string(hash), "admin")
	third, _ := store.Create("troisieme", string(hash), "admin")
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	push, err := webpush.NewStoreAt(filepath.Join(dir, "push.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []accounts.User{admin, other, third} {
		if err := push.Add(&wp.Subscription{Endpoint: "https://push.example/" + user.Username}, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	opts := &Options{AccountsAuth: a, PushStore: push}
	audience := pushAudience{policy: newAccountPolicy(opts)}
	if !audience.IsAdmin(other.ID) || audience.IsAdmin("inconnu") {
		t.Fatal("audience does not follow the account store")
	}
	token, _ := sessions.CreateForAccount(admin.ID)
	router := chi.NewRouter()
	router.Use(a.Middleware, accountAdminOnly)
	router.Patch("/users/{id}", func(w http.ResponseWriter, r *http.Request) { handleAdminUserUpdate(w, r, opts) })
	patch := func(id, body string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/users/"+id, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	patch(other.ID, `{"role":"guest"}`)
	patch(third.ID, `{"disabled":true}`)
	if audience.IsAdmin(other.ID) || audience.IsAdmin(third.ID) {
		t.Fatal("demoted or disabled account still an admin for push")
	}
	var left []string
	for _, sub := range push.All() {
		left = append(left, sub.AccountID)
	}
	if len(left) != 1 || left[0] != admin.ID {
		t.Fatalf("subscriptions left: %v", left)
	}
}
