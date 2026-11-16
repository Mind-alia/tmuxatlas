package peer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

func TestAccountPairingGrantsHostOrUndoesPairing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	hub, _ := identity.Generate("hub")
	store, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	pairing := identity.NewPairingManager()
	handler := NewHandler(NewHubManager(hub, store), store, toolevents.NewTracker(), pairing, NewPTYRelay(), "https://hub")
	pair := func(account, name string) (*identity.Identity, int) {
		t.Helper()
		remote, _ := identity.Generate(name)
		code, err := pairing.GenerateFor(account)
		if err != nil {
			t.Fatal(err)
		}
		proof, _ := remote.Sign(identity.PairingTranscript("https://hub", code.Code, name, remote.PublicKey))
		body, _ := json.Marshal(map[string]any{
			"code": code.Code, "name": name, "public_key": remote.PublicKey,
			"version": identity.PairingVersion, "signature": base64.StdEncoding.EncodeToString(proof),
		})
		w := httptest.NewRecorder()
		handler.HandlePairing(w, httptest.NewRequest(http.MethodPost, "/api/pair/complete", bytes.NewReader(body)))
		return remote, w.Code
	}

	if _, status := pair("alice-id", "alice@host"); status != http.StatusUnauthorized || store.Get("alice@host") != nil {
		t.Fatalf("account code without a grant callback: %d", status)
	}
	var granted [2]string
	handler.OnAccountPaired = func(account, host, _ string) error {
		if account == "bob-id" {
			return errors.New("host taken")
		}
		granted = [2]string{account, host}
		return nil
	}
	remote, status := pair("alice-id", "alice@host")
	if status != http.StatusOK || granted != [2]string{"alice-id", remote.Fingerprint()} {
		t.Fatalf("grant: %d %v", status, granted)
	}
	if _, status := pair("bob-id", "bob@host"); status != http.StatusUnauthorized || store.Get("bob@host") != nil {
		t.Fatalf("failed grant kept the peer: %d", status)
	}
}
