package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
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
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm"
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
	waitShown := func(browser *websocket.Conn, text string) { t.Helper(); sudoWaitShown(t, browser, text) }
	waitClosed := func(browser *websocket.Conn, what string) { t.Helper(); sudoWaitClosed(t, browser, what) }

	if status := call(http.MethodGet, "/ws/sudo", userCookie, ""); status != http.StatusForbidden {
		t.Fatalf("a non-admin account reached /ws/sudo: %d", status)
	}

	first := rig.request(t, "echo premier")
	browser := rig.open(t, first.ID, admin, http.Header{"Cookie": []string{adminCookie}})
	waitShown(browser, "sudo echo premier")
	// Make this shell ignore SIGHUP: it then outlives the decision until the
	// sudo peer kills it 2 s later, so reopening at once would find the peer
	// busy. Ctrl-U clears the proposed line first; 40+2 is only on screen
	// once the line ran.
	if err := browser.WriteMessage(websocket.BinaryMessage, []byte("\x15trap '' HUP; echo $((40+2))\r")); err != nil {
		t.Fatal(err)
	}
	waitShown(browser, "42")

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
	// The decision closes the browser at once; the sudo peer then ends the
	// shell, frees its runner and only then logs "closed". Opening the next
	// terminal before that would find the peer still busy with this one.
	eventually(t, "the first terminal to end on the sudo peer", func() bool {
		return auditRecorded(rig.auditPath, first.ID, "closed")
	})

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

// auditRecorded reports whether the sudo peer's audit log holds event for
// the request.
func auditRecorded(path, requestID, event string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		var entry sudoterm.AuditEntry
		if json.Unmarshal([]byte(line), &entry) == nil && entry.RequestID == requestID && entry.Event == event {
			return true
		}
	}
	return false
}

// TestSudoTerminalClosesWhenPasskeySessionEnds is the passkey counterpart of
// the revocation check above: logging out, or deleting the passkey that
// opened the session, closes an open sudo terminal.
func TestSudoTerminalClosesWhenPasskeySessionEnds(t *testing.T) {
	sessions := auth.NewSessionManager(time.Hour)
	rig := newSudoRig(t, func(opts *Options) { opts.SessionMgr = sessions }, func(mux *http.ServeMux, service *sudoService) {
		mux.Handle("/ws/sudo", auth.Middleware(sessions, false)(http.HandlerFunc(service.HandleTerminal)))
	})
	// Outside the accounts mode the signed-in owner is the administrator.
	owner := sudo.Actor{ID: "owner", Name: "propriétaire"}
	for _, tt := range []struct {
		name string
		end  func(token string)
	}{
		{"logout", sessions.Revoke},
		{"deleted passkey", func(string) { sessions.RevokeCredential("cle-ouverture") }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			token, err := sessions.CreateForCredential("cle-ouverture")
			if err != nil {
				t.Fatal(err)
			}
			request := rig.request(t, "echo ouvert")
			browser := rig.open(t, request.ID, owner, http.Header{"Cookie": []string{"tmuxatlas_session=" + token}})
			sudoWaitShown(t, browser, "sudo echo ouvert")
			tt.end(token)
			sudoWaitClosed(t, browser, "the passkey session ended")
			// The next case opens a terminal: let this one end on the peer.
			eventually(t, "the terminal to end on the sudo peer", func() bool {
				return auditRecorded(rig.auditPath, request.ID, "closed")
			})
		})
	}
}

// sudoWaitShown reads the terminal until text is on screen.
func sudoWaitShown(t *testing.T, browser *websocket.Conn, text string) {
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

// sudoWaitClosed fails unless the terminal closes within ten seconds.
func sudoWaitClosed(t *testing.T, browser *websocket.Conn, what string) {
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
