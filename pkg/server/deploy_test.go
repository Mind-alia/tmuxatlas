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
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/deploy"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"golang.org/x/crypto/bcrypt"
)

func TestDeployRoutesAreClassifiedAdmin(t *testing.T) {
	if len(deploy.Routes) == 0 {
		t.Fatal("no deploy route declared")
	}
	for _, route := range deploy.Routes {
		method, path, _ := strings.Cut(route, " ")
		key := method + " /api" + path
		if accountOtherRoutes[key] != "admin" || accountUserRoutes[key] {
			t.Errorf("%s must be admin-only", key)
		}
	}
}

func TestDeploySwitchIsOffByDefault(t *testing.T) {
	for value, want := range map[string]bool{"": false, "off": false, "0": false, "maybe": false, "on": true, " ON ": true, "true": true, "1": true, "yes": true} {
		t.Setenv("TMUXATLAS_DEPLOY", value)
		if deployEnabled() != want {
			t.Errorf("TMUXATLAS_DEPLOY=%q : %v", value, !want)
		}
	}
}

// recordingRunner stands for git and docker: it records the commands and
// runs nothing.
type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) Run(_ context.Context, cmd deploy.Command, out io.Writer) error {
	line := cmd.Tool + " " + strings.Join(cmd.Args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, line)
	r.mu.Unlock()
	switch {
	case strings.Contains(line, " status "):
		io.WriteString(out, "# branch.oid 1111\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -1\n")
	case strings.Contains(line, " log "):
		io.WriteString(out, "1111111111111111111111111111111111111111\x1f1111111\x1f2026-01-05T10:00:00+00:00\x1fAdd the cart page\n")
	case strings.Contains(line, " inspect "):
		// The build produced a new image under the service's name.
		io.WriteString(out, "sha256:"+strings.Repeat("9", 64)+"\n")
	case strings.Contains(line, " build"):
		io.WriteString(out, "building with NPM_TOKEN=build-secret-value\n")
	}
	return nil
}

// changes returns the commands other than the two read-only git ones.
func (r *recordingRunner) changes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, call := range r.calls {
		if !strings.HasPrefix(call, "git --no-optional-locks ") {
			out = append(out, call)
		}
	}
	return out
}

type deployTestServer struct {
	base         string
	runner       *recordingRunner
	admin, alice deployLogin
	appDir       string
	home         string
}

type deployLogin struct {
	cookie *http.Cookie
	csrf   string
}

// startDeployServer runs the real server over a fake Engine API socket that
// lists one Compose project, "shop", and a recording runner.
func startDeployServer(t *testing.T, dockerSwitch, deploySwitch string) *deployTestServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "atlas-deploy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s := &deployTestServer{runner: &recordingRunner{}, home: filepath.Join(dir, "home")}
	s.appDir = filepath.Join(s.home, "apps", "shop")
	if err := os.MkdirAll(s.appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(s.appDir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", s.home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DOCKER", dockerSwitch)
	t.Setenv("TMUXATLAS_DEPLOY", deploySwitch)
	previous := deployRunner
	deployRunner = s.runner
	t.Cleanup(func() { deployRunner = previous })

	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	engine := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": dockerTestID, "Names": []string{"/shop-web-1"}, "Image": "shop-web",
			"ImageID": "sha256:" + dockerTestID, "State": "running", "Status": "Up 1 hour", "Ports": []any{},
			"Labels": map[string]string{
				"com.docker.compose.project": "shop", "com.docker.compose.service": "web",
				"com.docker.compose.project.working_dir":  s.appDir,
				"com.docker.compose.project.config_files": compose,
			},
		}})
	}))
	engine.Listener = listener
	engine.Start()
	t.Cleanup(engine.Close)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)

	store := accounts.OpenStore(filepath.Join(dir, "users.json"))
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for username, role := range map[string]string{"admin": "admin", "alice": "user"} {
		if _, err := store.Create(username, string(hash), role); err != nil {
			t.Fatal(err)
		}
	}
	sessions := auth.NewSessionManager(time.Hour)
	a, err := auth.NewAccountsAuth(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	hubIdentity, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	address := freeAddress(t)
	s.base = "http://" + address
	runServer(t, &Options{
		Role: "hub", ListenAddress: address, PublicURL: s.base,
		SocketPath: filepath.Join(dir, "server.sock"), AuthEnabled: true,
		AccountsAuth: a, SessionMgr: sessions, PeerHandler: &peer.Handler{}, PTYRelay: &peer.PTYRelay{},
		PeerMgr: peer.NewHubManager(hubIdentity, nil),
	})
	loginAs := func(username string) deployLogin {
		t.Helper()
		body := fmt.Sprintf(`{"username":%q,"password":"mot de passe"}`, username)
		request, _ := http.NewRequest(http.MethodPost, s.base+"/api/auth/login", strings.NewReader(body))
		request.Header.Set("Origin", s.base)
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
		return deployLogin{cookie: response.Cookies()[0], csrf: token.CSRF}
	}
	s.admin, s.alice = loginAs("admin"), loginAs("alice")
	return s
}

func (s *deployTestServer) call(t *testing.T, method, path, origin string, who deployLogin, csrf bool, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
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

const deployTestRecord = "20260105T100000.000000000Z-0a0b0c0d"

// The deployment routes behind the real server: authentication, admin role,
// Origin and CSRF checks, then the deployment's own switch.
func TestDeployRoutesKeepTheServerGuards(t *testing.T) {
	s := startDeployServer(t, "on", "on")
	const base = "/api/docker/stacks/shop/deploy"
	body := `{"id":"` + deployTestRecord + `"}`
	for _, route := range deploy.Routes {
		method, path, _ := strings.Cut(route, " ")
		path = "/api" + strings.NewReplacer("{project}", "shop", "{id}", deployTestRecord).Replace(path)
		if code, answer := s.call(t, method, path, s.base, deployLogin{}, false, body); code != http.StatusUnauthorized {
			t.Errorf("anonyme, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := s.call(t, method, path, s.base, s.alice, true, body); code != http.StatusForbidden {
			t.Errorf("compte user, %s %s : %d %s", method, path, code, answer)
		}
		if method == http.MethodGet {
			continue
		}
		if code, answer := s.call(t, method, path, s.base, s.admin, false, body); code != http.StatusForbidden {
			t.Errorf("admin sans jeton CSRF, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := s.call(t, method, path, "http://evil.example", s.admin, true, body); code != http.StatusForbidden {
			t.Errorf("admin, autre origine, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := s.call(t, method, path, "", s.admin, true, body); code != http.StatusForbidden {
			t.Errorf("admin sans origine, %s %s : %d %s", method, path, code, answer)
		}
	}
	if got := s.runner.changes(); len(got) != 0 {
		t.Fatalf("une requête refusée a lancé des commandes : %v", got)
	}
	records := filepath.Join(s.home, ".local", "share", "tmuxatlas", "deploy")
	if entries, _ := os.ReadDir(records); len(entries) != 0 {
		t.Fatal("une requête refusée a écrit un enregistrement")
	}

	// The admin deploys; the operation runs in the background.
	code, answer := s.call(t, http.MethodPost, base, s.base, s.admin, true, "")
	var started deploy.Record
	if code != http.StatusAccepted || json.Unmarshal([]byte(answer), &started) != nil || started.Actor != "admin" {
		t.Fatalf("déploiement : %d %s", code, answer)
	}
	var state deploy.State
	for deadline := time.Now().Add(20 * time.Second); ; {
		code, answer := s.call(t, http.MethodGet, base, "", s.admin, false, "")
		state = deploy.State{}
		if code != http.StatusOK || json.Unmarshal([]byte(answer), &state) != nil {
			t.Fatalf("état : %d %s", code, answer)
		}
		if state.Current == nil && len(state.History) == 1 && state.History[0].Result != deploy.ResultRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("déploiement sans fin : %s", answer)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if state.History[0].Result != deploy.ResultSuccess || state.RollbackID != started.ID || !state.Enabled {
		t.Fatalf("état final : %+v", state)
	}
	compose := "docker compose -p shop --project-directory " + s.appDir + " -f " + filepath.Join(s.appDir, "compose.yaml")
	changes := s.runner.changes()
	if len(changes) != 6 || changes[0] != "git fetch" || changes[1] != "git merge --ff-only @{upstream}" ||
		!strings.HasPrefix(changes[2], "docker image tag sha256:"+dockerTestID+" shop-web:atlas-rollback-") ||
		changes[3] != compose+" build" || changes[4] != "docker image inspect --format {{.Id}} shop-web" || changes[5] != compose+" up -d" {
		t.Fatalf("commandes : %v", changes)
	}
	// The output of the build goes to the admin only, secrets masked.
	code, answer = s.call(t, http.MethodGet, base+"/history/"+started.ID, "", s.admin, false, "")
	if code != http.StatusOK || !strings.Contains(answer, "NPM_TOKEN=***") || strings.Contains(answer, "build-secret-value") {
		t.Fatalf("enregistrement : %d %s", code, answer)
	}
	if code, answer := s.call(t, http.MethodGet, base+"/history/"+started.ID, "", s.alice, false, ""); code != http.StatusForbidden || strings.Contains(answer, "NPM_TOKEN") {
		t.Fatalf("enregistrement lu par un compte user : %d %s", code, answer)
	}
	info, err := os.Stat(filepath.Join(records, "shop", started.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fichier de l’enregistrement : %v %v", info, err)
	}
}

// Without TMUXATLAS_DEPLOY=on the state is readable and nothing can be run.
func TestDeployIsReadOnlyUnlessAsked(t *testing.T) {
	s := startDeployServer(t, "on", "")
	const base = "/api/docker/stacks/shop/deploy"
	code, answer := s.call(t, http.MethodGet, base, "", s.admin, false, "")
	var state deploy.State
	if code != http.StatusOK || json.Unmarshal([]byte(answer), &state) != nil {
		t.Fatalf("état : %d %s", code, answer)
	}
	if state.Enabled || !strings.Contains(state.DisabledReason, "TMUXATLAS_DEPLOY=on") || state.Git.Branch != "main" || state.Git.Behind != 1 {
		t.Fatalf("état : %+v", state)
	}
	for _, request := range [][2]string{{base, ""}, {base + "/rollback", `{"id":"` + deployTestRecord + `"}`}} {
		if code, answer := s.call(t, http.MethodPost, request[0], s.base, s.admin, true, request[1]); code != http.StatusServiceUnavailable || !strings.Contains(answer, "TMUXATLAS_DEPLOY=on") {
			t.Errorf("POST %s : %d %s", request[0], code, answer)
		}
	}
	if got := s.runner.changes(); len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}
}

// The switches of the Docker actions apply: while a "user" account is active
// and the operator did not ask for the Containers view, nothing is served.
func TestDeployFollowsTheDockerGate(t *testing.T) {
	s := startDeployServer(t, "", "on")
	const base = "/api/docker/stacks/shop/deploy"
	if code, answer := s.call(t, http.MethodGet, base, "", s.admin, false, ""); code != http.StatusServiceUnavailable || !strings.Contains(answer, "compte non administrateur") {
		t.Fatalf("état : %d %s", code, answer)
	}
	if code, answer := s.call(t, http.MethodPost, base, s.base, s.admin, true, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("déploiement : %d %s", code, answer)
	}
	if got := s.runner.calls; len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}
}
