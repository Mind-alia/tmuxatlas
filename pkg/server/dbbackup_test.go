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
	"github.com/LosFurina/tmuxatlas/pkg/dbbackup"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"golang.org/x/crypto/bcrypt"
)

func TestDBBackupRoutesAreClassifiedAdmin(t *testing.T) {
	if len(dbbackup.Routes) == 0 {
		t.Fatal("no database backup route declared")
	}
	for _, route := range dbbackup.Routes {
		method, path, _ := strings.Cut(route, " ")
		key := method + " /api" + path
		if accountOtherRoutes[key] != "admin" || accountUserRoutes[key] {
			t.Errorf("%s must be admin-only", key)
		}
	}
}

func TestDBRestoreSwitchIsOffUnlessAsked(t *testing.T) {
	for value, want := range map[string]bool{"": false, "off": false, "0": false, "maybe": false, "on": true, "ON": true, " true ": true, "1": true, "yes": true} {
		t.Setenv("TMUXATLAS_DB_RESTORE", value)
		if dbRestoreEnabled() != want {
			t.Errorf("TMUXATLAS_DB_RESTORE=%q: %v", value, !want)
		}
	}
}

// The routes behind the real server: authentication, admin role, Origin and
// CSRF checks. Docker is a fake socket with no database in it.
func TestDBBackupRoutesKeepTheServerGuards(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-dbbackup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("TMUXATLAS_MONITOR", "off")
	t.Setenv("TMUXATLAS_DB_RESTORE", "")
	t.Setenv("TMUXATLAS_DOCKER", "on")
	socket, dockerCalls := startFakeDocker(t, dir, 0)
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
	const body = `{"confirm":"web.1"}`
	for _, route := range dbbackup.Routes {
		method, path, _ := strings.Cut(route, " ")
		path = "/api" + strings.NewReplacer("{container}", "web.1", "{id}", "20261122T031500Z").Replace(path)
		if code, answer := call(method, path, base, login{}, false, body); code != http.StatusUnauthorized {
			t.Errorf("anonyme, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := call(method, path, base, alice, true, body); code != http.StatusForbidden {
			t.Errorf("compte user, %s %s : %d %s", method, path, code, answer)
		}
		if method == http.MethodGet {
			continue
		}
		if code, answer := call(method, path, base, admin, false, body); code != http.StatusForbidden {
			t.Errorf("admin sans jeton CSRF, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := call(method, path, "http://evil.example", admin, true, body); code != http.StatusForbidden {
			t.Errorf("admin, autre origine, %s %s : %d %s", method, path, code, answer)
		}
		if code, answer := call(method, path, "", admin, true, body); code != http.StatusForbidden {
			t.Errorf("admin sans origine, %s %s : %d %s", method, path, code, answer)
		}
	}
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("Docker appelé par des requêtes refusées : %v", calls)
	}

	// The admin gets the listing: Docker answers, no database in it, and
	// the restore switch is off.
	code, answer := call(http.MethodGet, "/api/db-backups", base, admin, false, "")
	if code != http.StatusOK || !strings.Contains(answer, `"available":true`) || !strings.Contains(answer, `"restore_enabled":false`) || !strings.Contains(answer, `"databases":[]`) {
		t.Fatalf("liste : %d %s", code, answer)
	}
	if strings.Contains(answer, "SECRET") {
		t.Fatalf("la liste montre une variable du conteneur : %s", answer)
	}
	// A container that is no database is refused, and nothing runs in it.
	if code, answer := call(http.MethodPost, "/api/db-backups/web.1/backup", base, admin, true, ""); code != http.StatusNotFound {
		t.Errorf("sauvegarde d’un conteneur sans base : %d %s", code, answer)
	}
	if code, answer := call(http.MethodPost, "/api/db-backups/web.1/files/20261122T031500Z/restore", base, admin, true, body); code != http.StatusForbidden || !strings.Contains(answer, "TMUXATLAS_DB_RESTORE") {
		t.Errorf("restauration, interrupteur éteint : %d %s", code, answer)
	}
	if calls := dockerCalls(); len(calls) != 0 {
		t.Fatalf("appels Docker : %v", calls)
	}
}
