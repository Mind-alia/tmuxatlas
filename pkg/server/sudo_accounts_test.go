package server

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// TestSudoTerminalDoesNotHoldAccountPolicy opens a sudo terminal in accounts
// mode and checks that the rest of the API keeps answering while it is
// open, that the decision still goes through, and that the role check and
// session revocation still apply to /ws/sudo.
func TestSudoTerminalDoesNotHoldAccountPolicy(t *testing.T) {
	accountStore := accounts.OpenStore(filepath.Join(t.TempDir(), "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	adminUser, err := accountStore.Create("alice", string(hash), "admin")
	if err != nil {
		t.Fatal(err)
	}
	plainUser, err := accountStore.Create("bob", string(hash), "user")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	authenticator, err := auth.NewAccountsAuth(accountStore, sessions)
	if err != nil {
		t.Fatal(err)
	}
	cookie := func(userID string) string {
		token, err := sessions.CreateForAccount(userID)
		if err != nil {
			t.Fatal(err)
		}
		return "tmuxatlas_session=" + token
	}
	adminCookie, userCookie := cookie(adminUser.ID), cookie(plainUser.ID)

	rig := newSudoRig(t, func(opts *Options) { opts.AccountsAuth = authenticator }, func(mux *http.ServeMux, service *sudoService) {
		policy := newAccountPolicy(service.opts)
		router := chi.NewRouter()
		router.Use(authenticator.Middleware, policy.Middleware)
		router.Get("/ws/sudo", service.HandleTerminal)
		router.Get("/api/sessions", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		router.Route("/api", func(r chi.Router) { service.registerSudoAPI(r, r) })
		mux.Handle("/", router)
	})
	admin := sudo.Actor{ID: adminUser.ID, Name: adminUser.Username}
	client := &http.Client{Timeout: 3 * time.Second}
	call := func(method, path, cookie, body string) int {
		t.Helper()
		request, err := http.NewRequest(method, rig.server.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Cookie", cookie)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s while the sudo terminal is open: %v", method, path, err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	// waitShown reads the terminal until text is on screen.
	waitShown := func(browser *websocket.Conn, text string) {
		t.Helper()
		var screen strings.Builder
		_ = browser.SetReadDeadline(time.Now().Add(15 * time.Second))
		for !strings.Contains(screen.String(), text) {
			kind, data, err := browser.ReadMessage()
			if err != nil {
				t.Fatalf("terminal closed before showing %q: %v (screen %q)", text, err, screen.String())
			}
			if kind == websocket.BinaryMessage {
				screen.Write(data)
			}
		}
	}
	waitClosed := func(browser *websocket.Conn, what string) {
		t.Helper()
		_ = browser.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			if _, _, err := browser.ReadMessage(); err != nil {
				if strings.Contains(err.Error(), "i/o timeout") {
					t.Fatalf("the terminal stayed open after %s", what)
				}
				return
			}
		}
	}

	if status := call(http.MethodGet, "/ws/sudo", userCookie, ""); status != http.StatusForbidden {
		t.Fatalf("a non-admin account reached /ws/sudo: %d", status)
	}

	first := rig.request(t, "echo premier")
	browser := rig.open(t, first.ID, admin, http.Header{"Cookie": []string{adminCookie}})
	waitShown(browser, "sudo echo premier")

	if status := call(http.MethodGet, "/api/sessions", userCookie, ""); status != http.StatusNoContent {
		t.Fatalf("GET /api/sessions = %d", status)
	}
	if status := call(http.MethodGet, "/api/sudo/requests/"+first.ID, adminCookie, ""); status != http.StatusOK {
		t.Fatalf("following the request = %d", status)
	}
	if status := call(http.MethodPost, "/api/sudo/requests/"+first.ID+"/done", adminCookie, `{"note":"fait"}`); status != http.StatusOK {
		t.Fatalf("the decision = %d", status)
	}
	if request, _ := rig.service.store.Get(first.ID); request.Status != sudo.StatusDone {
		t.Fatalf("request after the decision = %+v", request)
	}
	waitClosed(browser, "the decision")

	// Revoking the administrator's session closes a terminal it opened.
	second := rig.request(t, "echo second")
	browser = rig.open(t, second.ID, admin, http.Header{"Cookie": []string{adminCookie}})
	waitShown(browser, "sudo echo second")
	sessions.Revoke(strings.TrimPrefix(adminCookie, "tmuxatlas_session="))
	waitClosed(browser, "the session was revoked")
	if status := call(http.MethodGet, "/ws/sudo", adminCookie, ""); status != http.StatusUnauthorized {
		t.Fatalf("a revoked session reached /ws/sudo: %d", status)
	}
}
