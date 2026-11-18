package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/activity"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/state"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/bcrypt"
)

// fakeDockerCLI puts first in PATH a docker client that records its arguments
// and stays alive, so no test ever reaches the real Docker.
func fakeDockerCLI(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "docker.args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\necho FAKE_DOCKER_SHELL\nexec sleep 300\n", argsFile)
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// A container shell runs docker exec, usually as root in the container: an
// account that is not an admin must not reach it through any session route,
// even with the exact (predictable) name of the session an admin opened.
func TestUserAccountCannotJoinAdminContainerShell(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-docker-shell-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", "on")
	socket, _ := startFakeDocker(t, dir, 0)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	argsFile := fakeDockerCLI(t, dir)

	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	store := accounts.OpenStore(filepath.Join(dir, "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("admin", string(hash), "admin"); err != nil {
		t.Fatal(err)
	}
	alice, err := store.Create("alice", string(hash), "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewOwnedSession("alice-work", "", alice.ID); err != nil {
		t.Fatal(err)
	}
	node, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	stateManager := state.NewManager(client)
	peerManager := peer.NewManager(node, nil, stateManager)
	peerManager.UpdatePeerSessions(peerManager.LocalID(), stateManager.GetSessions())
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	address := freeAddress(t)
	base := "http://" + address
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go peerManager.RunContext(ctx)
	runServer(t, &Options{Role: "standalone", ListenAddress: address, PublicURL: base,
		SocketPath: filepath.Join(dir, "server.sock"), Client: client, StateMgr: stateManager,
		Tracker: toolevents.NewTracker(), ActivityTracker: activity.NewTracker(), PeerMgr: peerManager,
		AuthEnabled: true, AccountsAuth: a, SessionMgr: sessions})
	host := peerManager.LocalID()

	type login struct {
		cookie *http.Cookie
		csrf   string
	}
	loginAs := func(username string) login {
		t.Helper()
		body := fmt.Sprintf(`{"username":%q,"password":"mot de passe"}`, username)
		request, _ := http.NewRequest(http.MethodPost, base+"/api/auth/login", strings.NewReader(body))
		request.Header.Set("Origin", base)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var token struct {
			CSRF string `json:"csrf_token"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&token) != nil || len(response.Cookies()) == 0 {
			t.Fatalf("connexion %s : %s", username, response.Status)
		}
		return login{cookie: response.Cookies()[0], csrf: token.CSRF}
	}
	// call sends a complete request (Origin, CSRF, JSON body), so that only
	// the account policy can refuse it.
	call := func(method, path, body string, who login) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", base)
		request.AddCookie(who.cookie)
		request.Header.Set(auth.CSRFHeader, who.csrf)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	dial := func(path string, who login) (*websocket.Conn, int, error) {
		t.Helper()
		headers := http.Header{"Origin": []string{base}, "Cookie": []string{who.cookie.String()}}
		conn, response, err := websocket.DefaultDialer.Dial("ws://"+address+path, headers)
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		return conn, status, err
	}
	admin, user := loginAs("admin"), loginAs("alice")

	code, body := call(http.MethodPost, "/api/docker/containers/"+dockerTestID[:12]+"/terminal", "", admin)
	if code != http.StatusOK || !strings.Contains(body, `"session":"docker-web-1"`) {
		t.Fatalf("terminal admin : %d %s", code, body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if data, _ := os.ReadFile(argsFile); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("le shell du conteneur n’a pas démarré")
		}
		time.Sleep(20 * time.Millisecond)
	}
	const shell = "docker-web-1"
	pane, err := client.Exec("display-message", "-p", "-t", tmux.ExactWindow(shell, ""), "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	pane = strings.TrimSpace(pane)

	// The terminal itself, with the exact (predictable) name of the session.
	terminalPath := "/ws/session?host=" + host + "&name=" + shell
	if conn, status, err := dial(terminalPath, user); err == nil || status != http.StatusForbidden {
		if conn != nil {
			conn.Close()
		}
		t.Errorf("terminal du conteneur ouvert par un compte user : statut %d, erreur %v", status, err)
	}
	// Every other route that reads or types into a session, or changes it.
	target := fmt.Sprintf(`{"host_id":%q,"session":%q`, host, shell)
	for _, check := range []struct{ name, method, path, body string }{
		{"aperçu", http.MethodGet, "/api/session/preview?host_id=" + host + "&session=" + shell, ""},
		{"aperçu d’une pane", http.MethodGet, "/api/session/preview?host_id=" + host + "&session=" + shell + "&pane=" + pane, ""},
		{"réponse", http.MethodPost, "/api/session/reply", target + fmt.Sprintf(`,"pane":%q,"key":"1"}`, pane)},
		{"fenêtre", http.MethodPost, "/api/session/select-window", target + `,"window":0}`},
		{"pane", http.MethodPost, "/api/session/select-pane", target + fmt.Sprintf(`,"pane":%q}`, pane)},
		{"prise", http.MethodPost, "/api/session/claim", target + "}"},
		{"libération", http.MethodPost, "/api/session/release", target + "}"},
		{"effacement d’événement", http.MethodDelete, "/api/tool-event", fmt.Sprintf(`{"host":%q,"session":%q}`, host, shell)},
		{"renommage", http.MethodPost, "/api/session/rename", target + `,"new_name":"pris"}`},
		{"fermeture", http.MethodPost, "/api/session/kill", target + "}"},
	} {
		if code, body := call(check.method, check.path, check.body, user); code != http.StatusForbidden {
			t.Errorf("%s par un compte user : %d %s", check.name, code, body)
		}
	}
	if !client.HasSession(shell) {
		t.Fatal("un compte user a fermé ou renommé le shell du conteneur")
	}
	if owner, _ := client.Exec("show-options", "-qv", "-t", tmux.ExactWindow(shell, ""), "@atlas-owner-name"); strings.TrimSpace(owner) != "admin" {
		t.Fatalf("propriétaire changé par un compte user : %q", owner)
	}
	// Nor does the session show in anything the account reads.
	for _, path := range []string{"/api/sessions", "/api/hosts", "/api/tool-events", "/api/activity"} {
		code, body := call(http.MethodGet, path, "", user)
		if code != http.StatusOK || strings.Contains(body, shell) {
			t.Errorf("%s pour un compte user : %d %s", path, code, body)
		}
	}
	events, _, err := dial("/ws/events?schema=1", user)
	if err != nil {
		t.Fatal(err)
	}
	_ = events.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, snapshot, err := events.ReadMessage()
	events.Close()
	if err != nil || strings.Contains(string(snapshot), shell) || !strings.Contains(string(snapshot), "alice-work") {
		t.Fatalf("flux d’événements d’un compte user : %v %s", err, snapshot)
	}
	// The account keeps its own sessions.
	conn, status, err := dial("/ws/session?host="+host+"&name=alice-work", user)
	if err != nil {
		t.Fatalf("terminal de sa propre session : %d %v", status, err)
	}
	conn.Close()

	// Admins keep the shell, in lists and in the terminal.
	if code, body := call(http.MethodGet, "/api/sessions", "", admin); code != http.StatusOK || !strings.Contains(body, shell) {
		t.Fatalf("sessions admin : %d %s", code, body)
	}
	conn, status, err = dial(terminalPath, admin)
	if err != nil {
		t.Fatalf("terminal du conteneur pour l’admin : %d %v", status, err)
	}
	conn.Close()
}

// The shell of a container starts only once its session carries the marker
// and the account terminals that may no longer use it are closed; if they do
// not close, docker never starts.
func TestContainerShellStartsAfterAccountTerminalsClose(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-docker-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	argsFile := fakeDockerCLI(t, dir)
	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("placeholder"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	node, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	stateManager := state.NewManager(client)
	peerManager := peer.NewManager(node, nil, stateManager)
	var allow bool
	var checks []string
	terminals := &dockerTerminals{
		opts:   &Options{Client: client, StateMgr: stateManager, PeerMgr: peerManager},
		router: peer.NewActionRouter(peerManager, peer.NewTmuxRuntimeExecutor(client), 0),
		closeTerminals: func() bool {
			marker, _ := client.Exec("show-options", "-qv", "-t", tmux.ExactWindow("docker-web-1", ""), dockerSessionOption)
			_, statErr := os.Stat(argsFile)
			checks = append(checks, fmt.Sprintf("marqueur=%s docker=%v", strings.TrimSpace(marker), statErr == nil))
			return allow
		},
	}
	argv := []string{"docker", "exec", "-it", dockerTestID, "sh"}
	request := httptest.NewRequest(http.MethodPost, "/api/docker/containers/x/terminal", nil)
	started := func() bool {
		time.Sleep(300 * time.Millisecond)
		_, err := os.Stat(argsFile)
		return err == nil
	}

	// Terminals still open: the session made for the shell is removed.
	if _, err := terminals.OpenTerminal(request, "docker-web-1", dockerTestID, argv); err == nil {
		t.Fatal("shell démarré alors que des terminaux restent ouverts")
	}
	want := "marqueur=" + dockerTestID + " docker=false"
	if len(checks) != 1 || checks[0] != want || started() || client.HasSession("docker-web-1") {
		t.Fatalf("refus : vérifications %v, docker lancé %v, session %v", checks, started(), client.HasSession("docker-web-1"))
	}
	// The dead shell of another container keeps it dead as well.
	for _, args := range [][]string{
		{"new-session", "-d", "-s", "docker-web-1"},
		{"set-option", "-q", "-t", "=docker-web-1:", dockerSessionOption, strings.Repeat("0", 64)},
		{"set-option", "-w", "-q", "-t", "=docker-web-1:", "remain-on-exit", "on"},
		{"respawn-pane", "-k", "-t", "=docker-web-1:", "--", "sh", "-c", "exit 3"},
	} {
		if _, err := client.Exec(args...); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		dead, _ := client.Exec("display-message", "-p", "-t", "=docker-web-1:", "#{pane_dead}")
		if strings.TrimSpace(dead) == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("le shell de test ne s’est pas arrêté")
		}
		time.Sleep(20 * time.Millisecond)
	}
	checks = nil
	if _, err := terminals.OpenTerminal(request, "docker-web-1", dockerTestID, argv); err == nil {
		t.Fatal("shell repris alors que des terminaux restent ouverts")
	}
	if len(checks) != 1 || checks[0] != want || started() {
		t.Fatalf("reprise refusée : vérifications %v", checks)
	}
	if err := client.KillSession("docker-web-1"); err != nil {
		t.Fatal(err)
	}

	// Terminals closed: docker starts, after the check.
	allow, checks = true, nil
	opened, err := terminals.OpenTerminal(request, "docker-web-1", dockerTestID, argv)
	if err != nil || opened.Session != "docker-web-1" || !opened.Created {
		t.Fatalf("ouverture : %+v %v", opened, err)
	}
	if len(checks) != 1 || checks[0] != want || !started() {
		t.Fatalf("ouverture : vérifications %v, docker lancé %v", checks, started())
	}
}

// closeReservedTerminals closes, and waits for, the account terminals on a
// local container shell, and leaves the others open.
func TestCloseReservedTerminalsWaitsForThem(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-docker-close-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plain", "shell"} {
		if err := client.NewSession(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	if _, err := client.Exec("set-option", "-t", "=shell:", dockerSessionOption, dockerTestID); err != nil {
		t.Fatal(err)
	}
	node, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	peerManager := peer.NewManager(node, nil, state.NewManager(client))
	policy := newAccountPolicy(&Options{Client: client, PeerMgr: peerManager})
	if !policy.closeReservedTerminals() {
		t.Fatal("aucun terminal ouvert : refus")
	}
	type terminal struct {
		cancelled chan struct{}
		done      chan struct{}
	}
	open := func(id uint64, host, session string) terminal {
		term := terminal{cancelled: make(chan struct{}), done: make(chan struct{})}
		policy.connections[id] = accountConnection{accountID: "alice", host: host, session: session,
			cancel: sync.OnceFunc(func() { close(term.cancelled) }), done: term.done}
		return term
	}
	onShell := open(1, peerManager.LocalID(), "shell")
	onPlain := open(2, peerManager.LocalID(), "plain")
	elsewhere := open(3, "autre-hote", "shell")
	result := make(chan bool, 1)
	go func() { result <- policy.closeReservedTerminals() }()
	select {
	case <-onShell.cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("le terminal du shell n’a pas été fermé")
	}
	select {
	case <-result:
		t.Fatal("retour avant la fin du terminal fermé")
	case <-time.After(100 * time.Millisecond):
	}
	close(onShell.done)
	if ok := <-result; !ok {
		t.Fatal("terminal fermé à temps, pourtant refusé")
	}
	for name, other := range map[string]terminal{"session ordinaire": onPlain, "autre hôte": elsewhere} {
		select {
		case <-other.cancelled:
			t.Errorf("%s : terminal fermé", name)
		default:
		}
	}
	if _, kept := policy.connections[1]; kept || len(policy.connections) != 2 {
		t.Fatalf("terminaux suivis : %+v", policy.connections)
	}
}

// A terminal that does not close in time refuses the shell, and keeps
// refusing it on the next attempt for as long as it is open.
func TestCloseReservedTerminalsKeepsRefusingAnOpenTerminal(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-docker-stuck-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("shell"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	if _, err := client.Exec("set-option", "-t", "=shell:", dockerSessionOption, dockerTestID); err != nil {
		t.Fatal(err)
	}
	node, err := identity.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	peerManager := peer.NewManager(node, nil, state.NewManager(client))
	policy := newAccountPolicy(&Options{Client: client, PeerMgr: peerManager})
	reservedTerminalsGrace = 200 * time.Millisecond
	t.Cleanup(func() { reservedTerminalsGrace = 5 * time.Second })
	done := make(chan struct{})
	policy.connections[1] = accountConnection{accountID: "alice", host: peerManager.LocalID(), session: "shell",
		cancel: func() {}, done: done}
	for attempt := 1; attempt <= 2; attempt++ {
		if policy.closeReservedTerminals() {
			t.Fatalf("essai %d : shell autorisé alors que le terminal est encore ouvert", attempt)
		}
	}
	close(done)
	if !policy.closeReservedTerminals() || len(policy.connections) != 0 {
		t.Fatalf("terminal fermé, pourtant refusé ou encore suivi : %+v", policy.connections)
	}
}

type ownerResult string

func (r ownerResult) Execute(context.Context, string, peer.SessionTarget, json.RawMessage) (peer.ActionResponse, error) {
	return peer.ActionResponse{Result: json.RawMessage(r)}, nil
}

// On another host, the agent says which sessions are container shells; the
// policy then treats them like missing ones for accounts that are not admins.
func TestRemoteContainerShellIsUnavailableToAccounts(t *testing.T) {
	policy := newAccountPolicy(&Options{})
	policy.router = ownerResult(`{"owner":"alice"}`)
	if owner, ok := policy.remoteOwner("hote", "travail"); !ok || owner != "alice" {
		t.Fatalf("session ordinaire : %q %v", owner, ok)
	}
	policy.router = ownerResult(`{"admin_only":true,"owner":"admin"}`)
	if _, ok := policy.remoteOwner("hote", "docker-web"); ok {
		t.Fatal("shell de conteneur distant accessible")
	}
}
