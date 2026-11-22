package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/secrets"
	"golang.org/x/crypto/bcrypt"
)

func TestSecretsRoutesAreClassifiedAdmin(t *testing.T) {
	if len(secrets.Routes) == 0 {
		t.Fatal("no secrets route declared")
	}
	for _, route := range secrets.Routes {
		method, path, _ := strings.Cut(route, " ")
		key := method + " /api" + path
		if accountOtherRoutes[key] != "admin" || accountUserRoutes[key] {
			t.Errorf("%s must be admin-only", key)
		}
	}
}

// The vault's routes behind the real server: authentication, admin role,
// Origin and CSRF checks, then the vault's own lock.
func TestSecretsRoutesKeepTheServerGuards(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-secrets-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	home := filepath.Join(dir, "home")
	appDir := filepath.Join(home, "apps", "shop")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_SECRETS", "")
	// The restart goes to a fake Engine API socket, never to Docker.
	t.Setenv("TMUXATLAS_DOCKER", "on")
	socket, dockerCalls := startFakeDocker(t, dir, 0)
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
	previous := secretsScryptWorkFactor
	secretsScryptWorkFactor = 4
	t.Cleanup(func() { secretsScryptWorkFactor = previous })

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
	base := "http://" + address
	runServer(t, &Options{
		Role: "hub", ListenAddress: address, PublicURL: base,
		SocketPath: filepath.Join(dir, "server.sock"), AuthEnabled: true,
		AccountsAuth: a, SessionMgr: sessions, PeerHandler: &peer.Handler{}, PTYRelay: &peer.PTYRelay{},
		PeerMgr: peer.NewHubManager(hubIdentity, nil),
	})

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
	call := func(method, path, origin string, who login, csrf bool, body string) (int, string) {
		t.Helper()
		request, err := http.NewRequest(method, base+path, strings.NewReader(body))
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
	admin, alice := loginAs("admin"), loginAs("alice")
	const passphrase = `{"passphrase":"une phrase de passe de test"}`
	ok := func(method, path, body string) string {
		t.Helper()
		code, answer := call(method, path, base, admin, true, body)
		if code != http.StatusOK {
			t.Fatalf("%s %s : %d %s", method, path, code, answer)
		}
		return answer
	}

	// Every route refuses a visitor and a "user" account, whatever it sends.
	for _, route := range secrets.Routes {
		method, path, _ := strings.Cut(route, " ")
		path = "/api" + strings.NewReplacer("{app}", "shop", "{name}", "TOKEN").Replace(path)
		if code, body := call(method, path, base, login{}, false, passphrase); code != http.StatusUnauthorized {
			t.Errorf("anonyme, %s %s : %d %s", method, path, code, body)
		}
		if code, body := call(method, path, base, alice, true, passphrase); code != http.StatusForbidden {
			t.Errorf("compte user, %s %s : %d %s", method, path, code, body)
		}
		if method == http.MethodGet {
			continue
		}
		// Changes, and the readings of values, need the Origin and the token.
		if code, body := call(method, path, base, admin, false, passphrase); code != http.StatusForbidden {
			t.Errorf("admin sans jeton CSRF, %s %s : %d %s", method, path, code, body)
		}
		if code, body := call(method, path, "http://evil.example", admin, true, passphrase); code != http.StatusForbidden {
			t.Errorf("admin, autre origine, %s %s : %d %s", method, path, code, body)
		}
		if code, body := call(method, path, "", admin, true, passphrase); code != http.StatusForbidden {
			t.Errorf("admin sans origine, %s %s : %d %s", method, path, code, body)
		}
	}
	vaultDir := filepath.Join(home, ".local", "share", "tmuxatlas", "secrets")
	if _, err := os.Stat(filepath.Join(vaultDir, "vault.json")); !os.IsNotExist(err) {
		t.Fatalf("une requête refusée a initialisé le coffre : %v", err)
	}

	if status := ok(http.MethodGet, "/api/secrets/status", ""); !strings.Contains(status, `"initialized":false`) {
		t.Fatalf("état initial : %s", status)
	}
	ok(http.MethodPost, "/api/secrets/init", passphrase)
	ok(http.MethodPost, "/api/secrets/apps", `{"name":"shop"}`)
	ok(http.MethodPut, "/api/secrets/apps/shop/vars/TOKEN", `{"value":"valeur-de-test"}`)
	target := filepath.Join(appDir, ".env")
	ok(http.MethodPut, "/api/secrets/apps/shop/target", fmt.Sprintf(`{"env_file":%q,"container":"web.1"}`, target))
	// No target among the hub's own files.
	for _, forbidden := range []string{filepath.Join(vaultDir, ".env"), filepath.Join(home, ".config", "tmuxatlas", ".env"), "/tmp/.env"} {
		if code, body := call(http.MethodPut, "/api/secrets/apps/shop/target", base, admin, true, fmt.Sprintf(`{"env_file":%q}`, forbidden)); code != http.StatusBadRequest {
			t.Errorf("cible %s : %d %s", forbidden, code, body)
		}
	}

	// Locked: no value, no file, no restart.
	for _, path := range []string{"/api/secrets/apps/shop/vars/TOKEN/reveal", "/api/secrets/apps/shop/export", "/api/secrets/apps/shop/apply"} {
		if code, body := call(http.MethodPost, path, base, admin, true, ""); code != http.StatusLocked || strings.Contains(body, "valeur-de-test") {
			t.Fatalf("coffre verrouillé, %s : %d %s", path, code, body)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("un coffre verrouillé a écrit le fichier cible")
	}
	if code, body := call(http.MethodPost, "/api/secrets/unlock", base, admin, true, `{"passphrase":"pas la bonne phrase"}`); code != http.StatusForbidden || !strings.Contains(body, "wrong_passphrase") {
		t.Fatalf("mauvaise phrase : %d %s", code, body)
	}
	ok(http.MethodPost, "/api/secrets/unlock", passphrase)
	// Unlocked by the admin is still closed to the other account.
	if code, body := call(http.MethodPost, "/api/secrets/apps/shop/vars/TOKEN/reveal", base, alice, true, ""); code != http.StatusForbidden || strings.Contains(body, "valeur-de-test") {
		t.Fatalf("révélation par un compte user : %d %s", code, body)
	}
	if answer := ok(http.MethodPost, "/api/secrets/apps/shop/vars/TOKEN/reveal", ""); !strings.Contains(answer, `"value":"valeur-de-test"`) {
		t.Fatalf("révélation : %s", answer)
	}

	// The unlock belongs to the browser session that gave the passphrase:
	// the same admin signed in from another browser reads nothing with it.
	elsewhere := loginAs("admin")
	if code, body := call(http.MethodGet, "/api/secrets/status", base, elsewhere, false, ""); code != http.StatusOK ||
		!strings.Contains(body, `"unlocked":false`) || !strings.Contains(body, `"unlocked_elsewhere":true`) {
		t.Fatalf("état vu d’une autre session : %d %s", code, body)
	}
	for _, path := range []string{"/api/secrets/apps/shop/vars/TOKEN/reveal", "/api/secrets/apps/shop/export", "/api/secrets/apps/shop/apply"} {
		if code, body := call(http.MethodPost, path, base, elsewhere, true, ""); code != http.StatusLocked || strings.Contains(body, "valeur-de-test") {
			t.Fatalf("autre session admin, %s : %d %s", path, code, body)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("une session sans la phrase de passe a écrit le fichier cible")
	}

	// Apply writes the file, then restarts through the Containers view: the
	// name is resolved in a listing and the Engine API gets the full ID.
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("Docker appelé avant l’application : %v", calls)
	}
	answer := ok(http.MethodPost, "/api/secrets/apps/shop/apply", "")
	if raw, _ := os.ReadFile(target); string(raw) != "TOKEN=valeur-de-test\n" {
		t.Fatalf("fichier appliqué : %q", raw)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode du fichier appliqué : %o", info.Mode().Perm())
	}
	if calls := dockerCalls(); len(calls) != 1 || calls[0] != "POST /containers/"+dockerTestID+"/restart" {
		t.Fatalf("appels Docker : %v (réponse %s)", calls, answer)
	}
	audit := ok(http.MethodGet, "/api/secrets/audit", "")
	if !strings.Contains(audit, `"actor":"admin"`) || !strings.Contains(audit, `"action":"apply"`) || strings.Contains(audit, "valeur-de-test") {
		t.Fatalf("journal : %s", audit)
	}
	ok(http.MethodPost, "/api/secrets/lock", "")
	if code, _ := call(http.MethodPost, "/api/secrets/apps/shop/vars/TOKEN/reveal", base, admin, true, ""); code != http.StatusLocked {
		t.Fatalf("révélation après verrouillage : %d", code)
	}

	// Signing out ends the unlock of that session: the private key does not
	// stay in memory behind a session that no longer exists.
	ok(http.MethodPost, "/api/secrets/unlock", passphrase)
	if code, body := call(http.MethodGet, "/api/secrets/status", base, elsewhere, false, ""); !strings.Contains(body, `"unlocked_elsewhere":true`) {
		t.Fatalf("avant déconnexion : %d %s", code, body)
	}
	if code, body := call(http.MethodPost, "/api/auth/logout", base, admin, true, ""); code >= 300 {
		t.Fatalf("déconnexion : %d %s", code, body)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, body := call(http.MethodGet, "/api/secrets/status", base, elsewhere, false, "")
		if !strings.Contains(body, `"unlocked_elsewhere":true`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("la déconnexion n’a pas verrouillé le coffre : %s", body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSecretsVaultNeedsAuthenticationOrAnExplicitSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, test := range []struct {
		value   string
		auth    bool
		problem string
	}{
		{"", true, ""}, {"on", true, ""}, {"off", true, "TMUXATLAS_SECRETS=off"}, {"0", false, "TMUXATLAS_SECRETS=off"},
		{"", false, "sans authentification"}, {"on", false, ""}, {"yes", false, ""},
	} {
		t.Setenv("TMUXATLAS_SECRETS", test.value)
		problem := secretsProblem(test.auth)
		if (test.problem == "") != (problem == "") || !strings.Contains(problem, test.problem) {
			t.Errorf("TMUXATLAS_SECRETS=%q, auth=%v : %q", test.value, test.auth, problem)
		}
	}
}
