package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/LosFurina/tmuxatlas/pkg/activity"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/state"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm/sudotermtest"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

type fixedAdmin struct{ actor sudo.Actor }

func (a fixedAdmin) Admin(*http.Request) (sudo.Actor, bool) { return a.actor, true }

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startTestPeer(t *testing.T, ctx context.Context, hubURL string, id *identity.Identity, store *identity.PeerStore) *peer.Client {
	t.Helper()
	client, err := tmux.NewClient()
	if err != nil {
		t.Skip("tmux unavailable: ", err)
	}
	stateMgr := state.NewManager(client)
	manager := peer.NewManager(id, store, stateMgr)
	go manager.Run()
	return peer.NewClient(hubURL, id, store, stateMgr, manager, activity.NewTracker(), toolevents.NewTracker(), client.TmuxPath())
}

// TestSudoTerminalEndToEnd follows one request from an agent peer through
// the hub to the sudo peer and a browser, with a fake sudo.
func TestSudoTerminalEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	sudotermtest.Install(t)

	hubID, _ := identity.Generate("hub")
	agentID, _ := identity.Generate("agents")
	sudoID, _ := identity.Generate("admin")
	store, err := identity.NewPeerStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []*identity.Identity{agentID, sudoID} {
		if err := store.Add(identity.Peer{Name: id.Name, PublicKey: id.PublicKey, PairedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	manager := peer.NewHubManager(hubID, store)
	go manager.Run()
	relay := peer.NewPTYRelay()
	tracker := toolevents.NewTracker()
	handler := peer.NewHandler(manager, store, tracker, identity.NewPairingManager(), relay, "")
	opts := &Options{AuthEnabled: true, PeerMgr: manager, Tracker: tracker, PTYRelay: relay, PeerHandler: handler, SudoPeers: sudoID.Fingerprint(), SudoRequesters: agentID.Fingerprint()}
	service := newSudoService(opts)
	admin := sudo.Actor{ID: "u-admin", Name: "admin"}
	service.admins = fixedAdmin{admin}
	handler.SetSudoHub(service)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handler.HandlePeer)
	mux.HandleFunc("/ws/peer-pty", relay.HandlePeerPTY)
	mux.HandleFunc("/ws/sudo", service.HandleTerminal)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agent := startTestPeer(t, ctx, server.URL, agentID, store)
	go agent.Run(ctx)
	sudoPeer := startTestPeer(t, ctx, server.URL, sudoID, store)
	runner, err := sudoterm.NewRunner(sudoterm.Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	auditPath, _ := sudoterm.AuditPath()
	audit, err := sudoterm.NewAudit(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	sudoPeer.EnableSudoTerminal(runner, audit)
	go sudoPeer.Run(ctx)

	eventually(t, "the sudo peer", func() bool {
		peers := service.Peers()
		return len(peers) == 1 && peers[0].Online && peers[0].UnixUser != ""
	})
	eventually(t, "the agent peer", func() bool { return manager.GetPeerConnection(agentID.Fingerprint()) != nil })

	// The agent asks through its own peer; the hub stamps its identity.
	broker := agent.SudoBroker()
	var view sudo.AgentView
	eventually(t, "the request", func() bool {
		view, err = broker.Create(ctx, sudo.CreateInput{
			Command: "echo bonjour; echo erreur >&2", Reason: "test",
			Origin: sudo.Origin{HostID: "forged", Session: "claude", Pane: "%1"},
		})
		return err == nil
	})
	request, _ := service.store.Get(view.ID)
	if request.Origin.HostID != agentID.Fingerprint() || request.Status != sudo.StatusPending {
		t.Fatalf("request = %+v", request)
	}
	if events := sudoEvents(tracker); len(events) != 1 || events[0].Host != agentID.Fingerprint() {
		t.Fatalf("sudo events = %+v", events)
	}
	if _, err := service.terminalPeer(agentID.Fingerprint(), request.Origin.HostID); err == nil {
		t.Fatal("the agent's own peer was accepted as a sudo terminal")
	}
	_, grant, err := service.store.Approve(view.ID, 1, sudoID.Fingerprint(), admin)
	if err != nil {
		t.Fatal(err)
	}

	header := http.Header{"Origin": []string{server.URL}}
	browser, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/sudo", header)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if err := browser.WriteJSON(map[string]any{"type": "open", "grant": grant.Token, "cols": 80, "rows": 24}); err != nil {
		t.Fatal(err)
	}
	var screen strings.Builder
	typed := false
	sawEchoOff := false
	_ = browser.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		kind, data, err := browser.ReadMessage()
		if err != nil {
			break
		}
		if kind == websocket.TextMessage {
			var control struct {
				Type    string `json:"type"`
				On      bool   `json:"on"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(data, &control)
			if control.Type == "error" {
				t.Fatalf("terminal error: %s", control.Message)
			}
			if control.Type == "echo" && !control.On {
				sawEchoOff = true
			}
		} else {
			screen.Write(data)
		}
		if !typed && sawEchoOff && strings.Contains(screen.String(), "mot de passe de") {
			typed = true
			if err := browser.WriteMessage(websocket.BinaryMessage, []byte(sudotermtest.TypedLine+"\r")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !typed || !strings.Contains(screen.String(), "bonjour") || strings.Contains(screen.String(), sudotermtest.TypedLine) {
		t.Fatalf("screen = %q (typed=%v)", screen.String(), typed)
	}

	eventually(t, "the capture", func() bool {
		request, _ = service.store.Get(view.ID)
		return request.Status == sudo.StatusAwaitingReview
	})
	if !strings.Contains(request.Capture.Output, "bonjour") || !strings.Contains(request.Capture.Output, "erreur") ||
		strings.Contains(request.Capture.Output, sudotermtest.TypedLine) || strings.Contains(request.Capture.Output, "mot de passe") {
		t.Fatalf("capture = %q", request.Capture.Output)
	}
	// Nothing reaches the agent before the review.
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	pending, _ := broker.Wait(waitCtx, view.ID)
	stop()
	if pending.Output != "" || pending.Status.Final() {
		t.Fatalf("agent saw %+v before review", pending)
	}
	if _, err := service.store.Review(view.ID, sudo.DecisionSend, admin, "ok"); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop = context.WithTimeout(ctx, 5*time.Second)
	final, err := broker.Wait(waitCtx, view.ID)
	stop()
	if err != nil || final.Status != sudo.StatusDelivered || !strings.Contains(final.Output, "bonjour") || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("agent result = %+v, %v", final, err)
	}
	eventually(t, "the sudo event to clear", func() bool { return len(sudoEvents(tracker)) == 0 })

	data, err := os.ReadFile(auditPath)
	if err != nil || strings.Count(string(data), "\n") != 2 || strings.Contains(string(data), sudotermtest.TypedLine) {
		t.Fatalf("audit = %q, %v", data, err)
	}
}

func TestSudoTerminalRejectsMissingOrigin(t *testing.T) {
	opts := &Options{AuthEnabled: true, PeerMgr: peer.NewHubManager(mustIdentity(t), nil)}
	service := newSudoService(opts)
	service.admins = fixedAdmin{sudo.Actor{ID: "a", Name: "a"}}
	server := httptest.NewServer(http.HandlerFunc(service.HandleTerminal))
	defer server.Close()
	_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("dial without Origin: err=%v response=%v", err, response)
	}
}

func mustIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	return id
}
