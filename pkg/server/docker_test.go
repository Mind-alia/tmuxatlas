package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	"golang.org/x/crypto/bcrypt"
)

const dockerTestID = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

// startFakeDocker serves a running container "web.1" on a unix socket and
// records every state change it receives. A stop takes stopDelay.
func startFakeDocker(t *testing.T, dir string, stopDelay time.Duration) (string, func() []string) {
	t.Helper()
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		switch r.URL.Path {
		case "/containers/json":
			fmt.Fprintf(w, `[{"Id":%q,"Names":["/web.1"],"Image":"nginx","State":"running","Status":"Up 1 hour","Labels":{},"Ports":[]}]`, dockerTestID)
		case "/info":
			fmt.Fprint(w, `{"NCPU":2,"MemTotal":1024}`)
		case "/containers/" + dockerTestID + "/json":
			fmt.Fprintf(w, `{"Id":%q,"State":{"Status":"running"},"Config":{"Env":["SECRET=x"]}}`, dockerTestID)
		case "/containers/" + dockerTestID + "/stats":
			fmt.Fprint(w, `{"cpu_stats":{},"memory_stats":{"usage":10}}`)
		case "/containers/" + dockerTestID + "/stop":
			select {
			case <-time.After(stopDelay):
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return socket, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func waitForServer(t *testing.T, base string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; {
		response, err := http.Get(base + "/api/version")
		if err == nil {
			response.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

func runServer(t *testing.T, options *Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("serveur non arrêté")
		}
	})
	waitForServer(t, options.PublicURL)
}

func TestDockerRoutesKeepTheServerGuards(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", "on")
	socket, dockerCalls := startFakeDocker(t, dir, 1500*time.Millisecond)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	// A fake docker CLI records the arguments tmux starts it with.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "docker.args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nexec sleep 300\n", argsFile)
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	client, err := tmux.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("placeholder"); err != nil {
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
	if _, err := store.Create("alice", string(hash), "user"); err != nil {
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
	call := func(method, path, origin string, who login, csrf bool) (int, string) {
		t.Helper()
		request, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if who.cookie != nil {
			request.AddCookie(who.cookie)
			if csrf {
				request.Header.Set(auth.CSRFHeader, who.csrf)
			}
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	admin, alice := loginAs("admin"), loginAs("alice")
	stop := "/api/docker/containers/" + dockerTestID[:12] + "/stop"
	terminal := "/api/docker/containers/" + dockerTestID[:12] + "/terminal"

	for _, check := range []struct {
		name, method, path, origin string
		who                        login
		csrf                       bool
		want                       int
	}{
		{"anonyme", http.MethodGet, "/api/docker/containers", base, login{}, false, http.StatusUnauthorized},
		{"compte user", http.MethodGet, "/api/docker/containers", base, alice, true, http.StatusForbidden},
		{"compte user, journaux", http.MethodGet, "/api/docker/containers/" + dockerTestID + "/logs", base, alice, true, http.StatusForbidden},
		{"compte user, arrêt", http.MethodPost, stop, base, alice, true, http.StatusForbidden},
		{"compte user, terminal", http.MethodPost, terminal, base, alice, true, http.StatusForbidden},
		{"admin sans jeton CSRF", http.MethodPost, stop, base, admin, false, http.StatusForbidden},
		{"admin, autre origine", http.MethodPost, stop, "http://evil.example", admin, true, http.StatusForbidden},
		{"admin sans origine", http.MethodDelete, "/api/docker/containers/" + dockerTestID, "", admin, true, http.StatusForbidden},
	} {
		if code, body := call(check.method, check.path, check.origin, check.who, check.csrf); code != check.want {
			t.Fatalf("%s : %d %s", check.name, code, body)
		}
	}
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("une requête refusée a atteint Docker : %v", calls)
	}

	code, body := call(http.MethodGet, "/api/docker/containers", base, admin, false)
	if code != http.StatusOK || !strings.Contains(body, `"name":"web.1"`) || !strings.Contains(body, `"terminal":true`) {
		t.Fatalf("liste admin : %d %s", code, body)
	}
	if code, body := call(http.MethodGet, "/api/docker/containers/"+dockerTestID+"/inspect", base, admin, false); code != http.StatusOK ||
		strings.Contains(body, "SECRET=x") || !strings.Contains(body, "SECRET=••••") {
		t.Fatalf("inspect admin : %d %s", code, body)
	}
	// A slow stop (1.5 s here, a grace period in real life) must not hold
	// the account policy's lock, which every other request takes.
	stopped := make(chan int, 1)
	go func() {
		code, _ := call(http.MethodPost, stop, base, admin, true)
		stopped <- code
	}()
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	if code, body := call(http.MethodGet, "/api/sessions", base, admin, false); code != http.StatusOK {
		t.Fatalf("sessions pendant un arrêt : %d %s", code, body)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Fatalf("un arrêt en cours a bloqué les autres requêtes %s", elapsed)
	}
	if code := <-stopped; code != http.StatusOK {
		t.Fatalf("arrêt admin : %d", code)
	}
	if calls := dockerCalls(); len(calls) != 1 || calls[0] != "POST /containers/"+dockerTestID+"/stop" {
		t.Fatalf("appels Docker : %v", calls)
	}

	// The shell runs in a tmux session made by the "new" action, whose pane
	// is the docker client alone, started with the full validated ID.
	code, body = call(http.MethodPost, terminal, base, admin, true)
	var opened struct {
		HostID  string `json:"host_id"`
		Session string `json:"session"`
		Created bool   `json:"created"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &opened) != nil ||
		opened.Session != "docker-web-1" || opened.HostID != peerManager.LocalID() || !opened.Created {
		t.Fatalf("terminal : %d %s", code, body)
	}
	var args []byte
	for deadline := time.Now().Add(5 * time.Second); ; {
		args, _ = os.ReadFile(argsFile)
		if len(args) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := strings.Join([]string{"--host", "unix://" + socket, "exec", "-it", dockerTestID, "sh", "-c",
		"command -v bash >/dev/null 2>&1 && exec bash || exec sh"}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("arguments du client docker :\n%s\nattendus :\n%s", args, want)
	}
	marker, err := client.Exec("show-options", "-qv", "-t", tmux.ExactWindow("docker-web-1", ""), dockerSessionOption)
	if err != nil || strings.TrimSpace(marker) != dockerTestID {
		t.Fatalf("marqueur de session : %q %v", marker, err)
	}
	owner, _ := client.Exec("show-options", "-qv", "-t", tmux.ExactWindow("docker-web-1", ""), "@atlas-owner-name")
	if strings.TrimSpace(owner) != "admin" {
		t.Fatalf("propriétaire de la session : %q", owner)
	}
	code, body = call(http.MethodPost, terminal, base, admin, true)
	if code != http.StatusOK || !strings.Contains(body, `"session":"docker-web-1"`) || strings.Contains(body, `"created":true`) {
		t.Fatalf("réutilisation : %d %s", code, body)
	}
	// A session of the same name opened by hand is never typed into.
	if err := client.KillSession("docker-web-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("docker-web-1"); err != nil {
		t.Fatal(err)
	}
	code, body = call(http.MethodPost, terminal, base, admin, true)
	if code != http.StatusOK || !strings.Contains(body, `"session":"docker-web-1-2"`) {
		t.Fatalf("session homonyme : %d %s", code, body)
	}
	// The dead shell of a container recreated under another ID frees its name.
	if err := client.KillSession("docker-web-1-2"); err != nil {
		t.Fatal(err)
	}
	if err := client.KillSession("docker-web-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("docker-web-1"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"set-option", "-q", "-t", "=docker-web-1:", dockerSessionOption, strings.Repeat("0", 64)},
		{"set-option", "-w", "-q", "-t", "=docker-web-1:", "remain-on-exit", "failed"},
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
	code, body = call(http.MethodPost, terminal, base, admin, true)
	if code != http.StatusOK || !strings.Contains(body, `"session":"docker-web-1"`) {
		t.Fatalf("reprise d’une session morte : %d %s", code, body)
	}
	marker, _ = client.Exec("show-options", "-qv", "-t", tmux.ExactWindow("docker-web-1", ""), dockerSessionOption)
	alive, _ := client.Exec("display-message", "-p", "-t", "=docker-web-1:", "#{pane_dead}")
	if strings.TrimSpace(marker) != dockerTestID || strings.TrimSpace(alive) != "0" {
		t.Fatalf("session reprise : marqueur %q, pane morte %q", marker, alive)
	}
}

func startWithoutAuth(t *testing.T, dir string) string {
	t.Helper()
	address := freeAddress(t)
	base := "http://" + address
	tmuxClient := &tmux.Client{}
	runServer(t, &Options{ListenAddress: address, PublicURL: base, SocketPath: filepath.Join(dir, "server.sock"),
		Client: tmuxClient, StateMgr: state.NewManager(tmuxClient), Tracker: toolevents.NewTracker(),
		ActivityTracker: activity.NewTracker()})
	return base
}

func TestDockerViewIsOffWithoutAuthUnlessAsked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", "")
	socket, dockerCalls := startFakeDocker(t, dir, 0)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	base := startWithoutAuth(t, dir)
	response, err := http.Get(base + "/api/docker/containers")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(data), `"available":false`) || !strings.Contains(string(data), "sans authentification") {
		t.Fatalf("liste sans authentification : %s", data)
	}
	request, _ := http.NewRequest(http.MethodPost, base+"/api/docker/containers/"+dockerTestID+"/stop", nil)
	request.Header.Set("Origin", base)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || len(dockerCalls()) != 0 {
		t.Fatalf("arrêt sans authentification : %d %v", response.StatusCode, dockerCalls())
	}
}

func TestDockerMutationsCheckOriginWithoutAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", "on")
	socket, dockerCalls := startFakeDocker(t, dir, 0)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	base := startWithoutAuth(t, dir)
	post := func(origin string) int {
		request, _ := http.NewRequest(http.MethodPost, base+"/api/docker/containers/"+dockerTestID+"/stop", nil)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if code := post(""); code != http.StatusForbidden {
		t.Fatalf("sans origine : %d", code)
	}
	if code := post("http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("autre origine : %d", code)
	}
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("Docker atteint : %v", calls)
	}
	if code := post(base); code != http.StatusOK {
		t.Fatalf("même origine : %d", code)
	}
}

func TestDockerRoutesWithPasskeySessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	socket, dockerCalls := startFakeDocker(t, dir, 0)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	address := freeAddress(t)
	base := "http://" + address
	sessions := auth.NewSessionManager(time.Hour)
	passkeys, err := auth.NewPasskeyManager(base, sessions)
	if err != nil {
		t.Fatal(err)
	}
	tmuxClient := &tmux.Client{}
	runServer(t, &Options{ListenAddress: address, PublicURL: base, SocketPath: filepath.Join(dir, "server.sock"),
		Client: tmuxClient, StateMgr: state.NewManager(tmuxClient), Tracker: toolevents.NewTracker(),
		ActivityTracker: activity.NewTracker(), AuthEnabled: true, SessionMgr: sessions, PasskeyManager: passkeys})
	token, err := sessions.Create()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _ := sessions.CSRF(token)
	call := func(method, path string, cookie, withCSRF bool) int {
		t.Helper()
		request, _ := http.NewRequest(method, base+path, nil)
		request.Header.Set("Origin", base)
		if cookie {
			request.AddCookie(&http.Cookie{Name: "tmuxatlas_session", Value: token})
		}
		if withCSRF {
			request.Header.Set(auth.CSRFHeader, csrf)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	stop := "/api/docker/containers/" + dockerTestID + "/stop"
	for _, check := range []struct {
		name         string
		method, path string
		cookie, csrf bool
		want         int
	}{
		{"liste sans session", http.MethodGet, "/api/docker/containers", false, false, http.StatusUnauthorized},
		{"liste avec session", http.MethodGet, "/api/docker/containers", true, false, http.StatusOK},
		{"arrêt sans session", http.MethodPost, stop, false, false, http.StatusUnauthorized},
		{"arrêt sans jeton CSRF", http.MethodPost, stop, true, false, http.StatusForbidden},
	} {
		if code := call(check.method, check.path, check.cookie, check.csrf); code != check.want {
			t.Fatalf("%s : %d, attendu %d", check.name, code, check.want)
		}
	}
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("Docker atteint : %v", calls)
	}
	if code := call(http.MethodPost, stop, true, true); code != http.StatusOK {
		t.Fatalf("arrêt avec session et jeton : %d", code)
	}
	if calls := dockerCalls(); len(calls) != 1 {
		t.Fatalf("appels Docker : %v", calls)
	}
}

// Every account's shells run under the hub's Unix account, which reaches the
// Docker socket: while an account other than an admin can sign in, the view
// is refused unless the operator asked for it with TMUXATLAS_DOCKER=on.
func TestDockerViewIsOffWhileAUserAccountIsActive(t *testing.T) {
	for _, mode := range []struct {
		name, env string
		refused   bool
	}{{"default", "", true}, {"asked", "on", false}} {
		t.Run(mode.name, func(t *testing.T) {
			// The socket path must be made of plain characters.
			dir, err := os.MkdirTemp("/tmp", "atlas-docker-gate-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			t.Setenv("TMUXATLAS_MONITOR", "off")
			t.Setenv("TMUXATLAS_DOCKER", mode.env)
			socket, dockerCalls := startFakeDocker(t, dir, 0)
			t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
			store := accounts.OpenStore(filepath.Join(dir, "users.json"))
			hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			admin, err := store.Create("admin", string(hash), "admin")
			if err != nil {
				t.Fatal(err)
			}
			sessions := auth.NewSessionManager(time.Hour)
			a, err := auth.NewAccountsAuth(store, sessions)
			if err != nil {
				t.Fatal(err)
			}
			address := freeAddress(t)
			base := "http://" + address
			tmuxClient := &tmux.Client{}
			runServer(t, &Options{ListenAddress: address, PublicURL: base, SocketPath: filepath.Join(dir, "server.sock"),
				Client: tmuxClient, StateMgr: state.NewManager(tmuxClient), Tracker: toolevents.NewTracker(),
				ActivityTracker: activity.NewTracker(), AuthEnabled: true, AccountsAuth: a, SessionMgr: sessions})

			request, _ := http.NewRequest(http.MethodPost, base+"/api/auth/login", strings.NewReader(`{"username":"admin","password":"mot de passe"}`))
			request.Header.Set("Origin", base)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var token struct {
				CSRF string `json:"csrf_token"`
			}
			if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&token) != nil || len(response.Cookies()) == 0 {
				t.Fatalf("connexion : %s", response.Status)
			}
			response.Body.Close()
			cookie := response.Cookies()[0]
			call := func(method, path string) (int, string) {
				t.Helper()
				request, _ := http.NewRequest(method, base+path, nil)
				request.Header.Set("Origin", base)
				request.Header.Set(auth.CSRFHeader, token.CSRF)
				request.AddCookie(cookie)
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, _ := io.ReadAll(response.Body)
				return response.StatusCode, string(data)
			}
			list := func() string {
				t.Helper()
				code, body := call(http.MethodGet, "/api/docker/containers")
				if code != http.StatusOK {
					t.Fatalf("liste : %d %s", code, body)
				}
				return body
			}
			stop := "/api/docker/containers/" + dockerTestID + "/stop"

			if body := list(); !strings.Contains(body, `"available":true`) {
				t.Fatalf("admins seuls : %s", body)
			}
			alice, err := store.Create("alice", string(hash), "user")
			if err != nil {
				t.Fatal(err)
			}
			body := list()
			code, _ := call(http.MethodPost, stop)
			if mode.refused {
				if !strings.Contains(body, `"available":false`) || !strings.Contains(body, "compte non administrateur") {
					t.Fatalf("liste avec un compte user : %s", body)
				}
				if code != http.StatusServiceUnavailable || len(dockerCalls()) != 0 {
					t.Fatalf("arrêt avec un compte user : %d %v", code, dockerCalls())
				}
			} else if !strings.Contains(body, `"available":true`) || code != http.StatusOK {
				t.Fatalf("vue demandée avec un compte user : %d %s", code, body)
			}
			disabled := true
			if _, err := store.Update(alice.ID, admin.ID, "", &disabled); err != nil {
				t.Fatal(err)
			}
			if body := list(); !strings.Contains(body, `"available":true`) {
				t.Fatalf("compte user désactivé : %s", body)
			}
		})
	}
}
