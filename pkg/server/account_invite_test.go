package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/go-chi/chi/v5"
)

func TestInvitationGrantsAndRoleChanges(t *testing.T) {
	store := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := accounts.HashPassword("mot de passe admin")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Create("admin", hash, accounts.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := sessions.CreateForAccount(admin.ID)
	pairing := identity.NewPairingManager()
	opts := &Options{AccountsAuth: a, PairingMgr: pairing}
	router := chi.NewRouter()
	router.Post("/invitation/inspect", a.InspectInvitation)
	router.Post("/invitation/accept", a.AcceptInvitation)
	router.Group(func(r chi.Router) {
		r.Use(a.Middleware, accountAdminOnly)
		r.Post("/users", func(w http.ResponseWriter, r *http.Request) { handleAdminUsers(w, r, opts) })
		r.Patch("/users/{id}", func(w http.ResponseWriter, r *http.Request) { handleAdminUserUpdate(w, r, opts) })
		r.Post("/users/{id}/invitation", func(w http.ResponseWriter, r *http.Request) { handleAdminInvitation(w, r, opts) })
		r.Post("/users/{id}/pairing", func(w http.ResponseWriter, r *http.Request) { handleAdminPairing(w, r, opts) })
	})
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	created := call(http.MethodPost, "/users", `{"username":"alice","role":"guest","unix_user":"inv-alice"}`, adminToken)
	var view struct {
		accountView
		Invitation *invitationView `json:"invitation"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &view) != nil || view.Invitation == nil || !view.Pending {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	if !strings.HasPrefix(view.Invitation.Path, "/invite#") || strings.Contains(view.Invitation.Path, "?") {
		t.Fatalf("token outside the fragment: %s", view.Invitation.Path)
	}
	token := view.Invitation.Token
	if got := call(http.MethodPost, "/invitation/inspect", `{"token":"`+token+`"}`, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "alice") {
		t.Fatalf("inspect: %d %s", got.Code, got.Body)
	}
	if got := call(http.MethodPost, "/invitation/accept", `{"token":"`+token+`","password":"court"}`, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", got.Code)
	}
	if got := call(http.MethodPost, "/invitation/accept", `{"token":"`+token+`","password":"mot de passe alice"}`, ""); got.Code != http.StatusNoContent {
		t.Fatalf("accept: %d %s", got.Code, got.Body)
	}
	if got := call(http.MethodPost, "/invitation/accept", `{"token":"`+token+`","password":"autre mot de passe"}`, ""); got.Code != http.StatusNotFound {
		t.Fatalf("reused invitation: %d", got.Code)
	}

	// The guest's own requests cannot reach the administration.
	aliceToken, _ := sessions.CreateForAccount(view.ID)
	if got := call(http.MethodPatch, "/users/"+view.ID, `{"role":"admin"}`, aliceToken); got.Code != http.StatusForbidden {
		t.Fatalf("self promotion: %d", got.Code)
	}
	if got := call(http.MethodPatch, "/users/"+view.ID, `{"hosts":["h-alice"]}`, adminToken); got.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", got.Code, got.Body)
	}
	if !sessions.Validate(aliceToken) {
		t.Fatal("adding a host ended the session")
	}
	if got := call(http.MethodPatch, "/users/"+view.ID, `{"hosts":[]}`, adminToken); got.Code != http.StatusOK {
		t.Fatalf("revoke: %d", got.Code)
	}
	if sessions.Validate(aliceToken) {
		t.Fatal("removing a host kept the session open")
	}
	if got := call(http.MethodPatch, "/users/"+admin.ID, `{"role":"guest"}`, adminToken); got.Code != http.StatusConflict {
		t.Fatalf("last admin demoted: %d", got.Code)
	}
	if got := call(http.MethodPatch, "/users/"+view.ID, `{"role":"admin"}`, adminToken); got.Code != http.StatusOK {
		t.Fatalf("promotion: %d %s", got.Code, got.Body)
	}

	// A pairing code made for an account grants it the paired host.
	code := call(http.MethodPost, "/users/"+view.ID+"/pairing", "", adminToken)
	var pairingCode struct {
		Code string `json:"code"`
	}
	if code.Code != http.StatusOK || json.Unmarshal(code.Body.Bytes(), &pairingCode) != nil || pairingCode.Code == "" {
		t.Fatalf("pairing code: %d %s", code.Code, code.Body)
	}
	var account string
	if err := pairing.CompleteFor(pairingCode.Code, func(got string) error { account = got; return nil }); err != nil || account != view.ID {
		t.Fatalf("pairing account=%q err=%v", account, err)
	}
	if got := call(http.MethodPost, "/users/missing/pairing", "", adminToken); got.Code != http.StatusNotFound {
		t.Fatalf("pairing for unknown account: %d", got.Code)
	}
}
