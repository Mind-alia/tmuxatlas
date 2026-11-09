package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/webauthn"
)

// A restart must not sign anyone out, nor bring back a session that ended.
func TestSessionsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	before := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = before.Close() })
	if err := before.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	kept, err := before.CreateForAccount("aris")
	if err != nil {
		t.Fatal(err)
	}
	csrf, _ := before.CSRF(kept)
	signedOut, _ := before.CreateForAccount("aris")
	before.Revoke(signedOut)
	otherDevice, _ := before.CreateForAccount("novanns")
	before.RevokeAccount("novanns", "")
	if err := before.Flush(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), kept) {
		t.Fatal("the file must hold token hashes only")
	}

	after := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = after.Close() })
	if err := after.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if account, _, ok := after.AccountSession(kept); !ok || account != "aris" {
		t.Fatalf("session lost across the restart: %q %v", account, ok)
	}
	if got, _ := after.CSRF(kept); got != csrf {
		t.Fatal("the CSRF token must survive with its session")
	}
	if after.Validate(signedOut) || after.Validate(otherDevice) {
		t.Fatal("a revoked session came back")
	}
}

func TestExpiredSessionsAreNotRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	before := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = before.Close() })
	if err := before.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	token, _ := before.CreateForAccount("aris")
	before.mu.Lock()
	value := before.sessions[sessionKey(token)]
	value.expiry = time.Now().Add(-time.Second)
	before.sessions[sessionKey(token)] = value
	before.mu.Unlock()
	if err := before.Flush(); err != nil {
		t.Fatal(err)
	}

	after := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = after.Close() })
	if err := after.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if after.Validate(token) {
		t.Fatal("an expired session was restored")
	}
}

func TestUnreadableSessionFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.PersistTo(path); err == nil {
		t.Fatal("a corrupt file must be reported")
	}
	token, err := manager.CreateForAccount("aris")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Flush(); err != nil {
		t.Fatal(err)
	}
	restored := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = restored.Close() })
	if err := restored.PersistTo(path); err != nil || !restored.Validate(token) {
		t.Fatalf("the file must be rewritten after a corrupt start: %v", err)
	}
}

// A change reaches the file without Flush: the background writer saves it.
func TestSessionChangesAreSavedInTheBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	manager := NewSessionManager(time.Hour)
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.PersistTo(path); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateForAccount("aris"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(path)
		var stored []storedSession
		if json.Unmarshal(raw, &stored) == nil && len(stored) == 1 {
			if stored[0].Issued.IsZero() || stored[0].AccountID != "aris" {
				t.Fatalf("stored session: %+v", stored[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session never reached the file: %s", raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A session restored without a full CSRF token would accept requests that send none.
func TestSessionWithoutCSRFIsNotRestored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	key := strings.Repeat("a", 64)
	for _, csrf := range []string{"", "short", strings.Repeat("z", 64)} {
		stored := []storedSession{{Key: key, CSRF: csrf, AccountID: "aris", Issued: time.Now(), Expiry: time.Now().Add(time.Hour)}}
		raw, _ := json.Marshal(stored)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		manager := NewSessionManager(time.Hour)
		t.Cleanup(func() { _ = manager.Close() })
		if err := manager.PersistTo(path); err != nil {
			t.Fatal(err)
		}
		manager.mu.RLock()
		_, restored := manager.sessions[key]
		manager.mu.RUnlock()
		if restored {
			t.Fatalf("csrf %q: session restored", csrf)
		}
	}
}

func TestCSRFMiddlewareRejectsAnEmptyToken(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	token, err := manager.CreateForAccount("aris")
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	value := manager.sessions[sessionKey(token)]
	value.csrf = ""
	manager.sessions[sessionKey(token)] = value
	manager.mu.Unlock()
	handler := CSRFMiddleware(manager)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/x", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestRetainEndsRejectedSessions(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	kept, _ := manager.CreateForCredential("phone")
	dropped, _ := manager.CreateForCredential("laptop")
	_, done, _ := manager.AccountSession(dropped)
	manager.Retain(func(info SessionInfo) bool { return info.Credential == "phone" && !info.Issued.IsZero() })
	if !manager.Validate(kept) || manager.Validate(dropped) {
		t.Fatal("Retain kept the wrong sessions")
	}
	select {
	case <-done:
	default:
		t.Fatal("an ended session must close its open connections")
	}
}

// Deleting a passkey ends the sessions it opened, and only those.
func TestDeletingAPasskeyEndsItsSessions(t *testing.T) {
	store := newTestPasskeyStore(t)
	for _, id := range []string{"phone", "laptop"} {
		if err := store.add(id, &webauthn.Credential{ID: []byte(id), PublicKey: []byte("public-" + id)}); err != nil {
			t.Fatal(err)
		}
	}
	sessions := NewSessionManager(time.Hour)
	manager := &PasskeyManager{store: store, sessions: sessions}
	phone, _ := sessions.CreateForCredential(encodeCredentialID([]byte("phone")))
	laptop, _ := sessions.CreateForCredential(encodeCredentialID([]byte("laptop")))
	router := chi.NewRouter()
	router.Delete("/api/auth/passkeys/{credentialID}", manager.DeleteHandler())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/auth/passkeys/"+encodeCredentialID([]byte("laptop")), nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !sessions.Validate(phone) || sessions.Validate(laptop) {
		t.Fatal("deleting a passkey must end its sessions only")
	}
	if got := manager.CredentialIDs(); len(got) != 1 || got[0] != encodeCredentialID([]byte("phone")) {
		t.Fatalf("credential IDs: %v", got)
	}
}
