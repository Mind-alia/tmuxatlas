package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
)

func voiceTestOptions(t *testing.T) (*Options, string) {
	t.Helper()
	local, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	peers := peer.NewManager(local, nil, nil)
	// An agent of another Unix account on the same machine, not connected.
	peers.RegisterPeer("agents-host", "agents", "", nil)
	return &Options{PeerMgr: peers}, peers.LocalID()
}

func TestVoiceStatusFollowsTheSessionHost(t *testing.T) {
	opts, localID := voiceTestOptions(t)
	for name, host := range map[string]string{"no host": "", "local host": localID, "offline agent": "agents-host"} {
		t.Run(name, func(t *testing.T) {
			if status := voiceStatus(httptest.NewRequest(http.MethodGet, "/api/voice", nil), opts, nil, host); status.Enabled || status.Ready {
				t.Fatalf("status = %#v", status)
			}
		})
	}
	if _, remote := remoteVoiceHost(opts, localID); remote {
		t.Fatal("the local host is not remote")
	}
	if _, remote := remoteVoiceHost(opts, "agents-host"); !remote {
		t.Fatal("another account's agent must get its own microphone")
	}
	if remoteMicID(nil, "tab-0123456789") != "" {
		t.Fatal("mic id sent to an agent without voice")
	}
}

func TestMicHandlerRoutesByHost(t *testing.T) {
	opts, localID := voiceTestOptions(t)
	handler := micHandler(opts, nil, peer.NewMicRelay())
	for name, test := range map[string]struct {
		query  string
		status int
	}{
		"local voice off":    {"?id=tab-0123456789&host=" + localID, http.StatusNotFound},
		"no host, voice off": {"?id=tab-0123456789", http.StatusNotFound},
		"remote bad id":      {"?id=x&host=agents-host", http.StatusBadRequest},
		"remote offline":     {"?id=tab-0123456789&host=agents-host", http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler(recorder, httptest.NewRequest(http.MethodGet, "/ws/mic"+test.query, nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
}

// Another host's microphone feeds recorders that other people's programs run
// under: an account that is not an administrator cannot stream there.
func TestRemoteMicIsForAdministrators(t *testing.T) {
	opts, localID := voiceTestOptions(t)
	handler := micHandler(opts, nil, peer.NewMicRelay())
	as := func(role, query string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/ws/mic"+query, nil)
		return request.WithContext(auth.WithAccount(request.Context(), auth.AccountIdentity{User: accounts.User{Role: role}}))
	}
	for name, test := range map[string]struct {
		request *http.Request
		status  int
	}{
		"user, remote":  {as("user", "?id=tab-0123456789&host=agents-host"), http.StatusForbidden},
		"user, local":   {as("user", "?id=tab-0123456789&host="+localID), http.StatusNotFound},
		"admin, remote": {as("admin", "?id=tab-0123456789&host=agents-host"), http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler(recorder, test.request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
		})
	}
	if mayUseRemoteMic(as("user", "")) || !mayUseRemoteMic(as("admin", "")) {
		t.Fatal("remote microphone permission does not follow the role")
	}
}
