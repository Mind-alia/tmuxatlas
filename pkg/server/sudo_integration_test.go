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

// sudoRig is a hub with an agent peer and a sudo peer running a fake sudo.
type sudoRig struct {
	ctx       context.Context
	service   *sudoService
	server    *httptest.Server
	tracker   *toolevents.Tracker
	agentID   *identity.Identity
	sudoID    *identity.Identity
	broker    sudo.Broker
	auditPath string
}

// newSudoRig starts the rig. configure adjusts the hub options before the
// sudo service exists; routes mounts /ws/sudo and whatever else the test
// needs.
func newSudoRig(t *testing.T, configure func(*Options), routes func(*http.ServeMux, *sudoService)) *sudoRig {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	fakeSudo := sudotermtest.Install(t)

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
	if configure != nil {
		configure(opts)
	}
	service := newSudoService(opts)
	handler.SetSudoHub(service)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/peer", handler.HandlePeer)
	mux.HandleFunc("/ws/peer-pty", relay.HandlePeerPTY)
	routes(mux, service)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	agent := startTestPeer(t, ctx, server.URL, agentID, store)
	go agent.Run(ctx)
	sudoPeer := startTestPeer(t, ctx, server.URL, sudoID, store)
	runner, err := sudoterm.NewRunner(sudoterm.Config{
		MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second,
		Path: filepath.Dir(fakeSudo) + ":" + sudoterm.SystemPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := runner.LookSudo(); err != nil || resolved != fakeSudo {
		t.Fatalf("the terminal would run %q, not the fake sudo: refusing", resolved)
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
	return &sudoRig{ctx: ctx, service: service, server: server, tracker: tracker, agentID: agentID, sudoID: sudoID,
		broker: agent.SudoBroker(), auditPath: auditPath}
}

// request files a request through the agent's own peer.
func (rig *sudoRig) request(t *testing.T, command string) sudo.AgentView {
	t.Helper()
	var view sudo.AgentView
	var err error
	eventually(t, "the request", func() bool {
		view, err = rig.broker.Create(rig.ctx, sudo.CreateInput{
			Command: command, Reason: "test",
			Origin: sudo.Origin{HostID: "forged", Session: "claude", Pane: "%1"},
		})
		return err == nil
	})
	return view
}

// open approves a request as admin and opens its terminal in a browser.
func (rig *sudoRig) open(t *testing.T, id string, admin sudo.Actor, header http.Header) *websocket.Conn {
	t.Helper()
	_, grant, err := rig.service.store.Approve(id, 1, rig.sudoID.Fingerprint(), admin)
	if err != nil {
		t.Fatal(err)
	}
	header.Set("Origin", rig.server.URL)
	browser, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(rig.server.URL, "http")+"/ws/sudo", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { browser.Close() })
	if err := browser.WriteJSON(map[string]any{"type": "open", "grant": grant.Token, "cols": 80, "rows": 24}); err != nil {
		t.Fatal(err)
	}
	return browser
}

// TestSudoTerminalEndToEnd follows one request from an agent peer through
// the hub to the sudo peer and a browser, with a fake sudo.
func TestSudoTerminalEndToEnd(t *testing.T) {
	admin := sudo.Actor{ID: "u-admin", Name: "admin"}
	rig := newSudoRig(t, nil, func(mux *http.ServeMux, service *sudoService) {
		service.admins = fixedAdmin{admin}
		mux.HandleFunc("/ws/sudo", service.HandleTerminal)
	})
	service, tracker, agentID, broker, ctx, auditPath := rig.service, rig.tracker, rig.agentID, rig.broker, rig.ctx, rig.auditPath

	// The agent asks through its own peer; the hub stamps its identity.
	view := rig.request(t, "echo bonjour-$((40+2))")
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
	browser := rig.open(t, view.ID, admin, http.Header{})
	var screen strings.Builder
	step := 0 // 0: wait for the proposal, 1: wait for sudo's prompt, 2: wait for the output
	sawEchoOff := false
	send := func(text string) {
		if err := browser.WriteMessage(websocket.BinaryMessage, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
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
		shown := screen.String()
		switch {
		case step == 0 && strings.Contains(shown, "sudo echo bonjour-$((40+2))"):
			if strings.Contains(shown, "bonjour-42") {
				t.Fatalf("the proposed command ran by itself: %q", shown)
			}
			step = 1
			send("\r") // the human runs it
		case step == 1 && sawEchoOff && strings.Contains(shown, "password:"):
			step = 2
			send(sudotermtest.TypedLine + "\r")
		case step == 2 && strings.Contains(shown, "bonjour-42"):
			step = 3
			send("exit\r")
		}
	}
	shown := screen.String()
	if step != 3 || !strings.Contains(shown, "# Proposé par "+agentID.Fingerprint()) || strings.Contains(shown, sudotermtest.TypedLine) {
		t.Fatalf("screen = %q (step %d)", shown, step)
	}

	eventually(t, "the terminal to close", func() bool {
		request, _ = service.store.Get(view.ID)
		return request.Status == sudo.StatusClosed
	})
	// Nothing reaches the agent before the decision.
	waitCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	pending, _ := broker.Wait(waitCtx, view.ID)
	stop()
	if pending.Status.Final() || pending.Note != "" {
		t.Fatalf("agent saw %+v before the decision", pending)
	}
	if _, err := service.store.Done(view.ID, admin, "jq installé"); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop = context.WithTimeout(ctx, 5*time.Second)
	final, err := broker.Wait(waitCtx, view.ID)
	stop()
	if err != nil || final != (sudo.AgentView{ID: view.ID, Status: sudo.StatusDone, Note: "jq installé"}) {
		t.Fatalf("agent result = %+v, %v", final, err)
	}
	eventually(t, "the sudo event to clear", func() bool { return len(sudoEvents(tracker)) == 0 })

	data, err := os.ReadFile(auditPath)
	if err != nil || strings.Count(string(data), "\n") != 2 || strings.Contains(string(data), sudotermtest.TypedLine) ||
		strings.Contains(string(data), "bonjour-42") {
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
