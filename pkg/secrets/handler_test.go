package secrets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

var (
	testUser  = &auth.AccountIdentity{User: accounts.User{ID: "u1", Username: "alice", Role: "user"}}
	testAdmin = &auth.AccountIdentity{User: accounts.User{ID: "a1", Username: "root", Role: "admin"}}
)

// serve runs one request through the routes, as the account given if any.
func serve(h *Handler, identity *auth.AccountIdentity, method, target, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	h.Register(router, router)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if identity != nil {
		request = request.WithContext(auth.WithAccount(request.Context(), *identity))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func errorCode(response *httptest.ResponseRecorder) string {
	var body struct{ Code string }
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Code
}

// routeRequests gives one request per declared route.
func routeRequests() [][2]string {
	var requests [][2]string
	for _, route := range Routes {
		method, path, _ := strings.Cut(route, " ")
		path = strings.NewReplacer("{app}", "shop", "{name}", "TOKEN").Replace(path)
		requests = append(requests, [2]string{method, path})
	}
	return requests
}

func TestEveryRouteIsDeclaredAndAdminOnly(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	h := NewHandler(v.Vault, "", "passkey")
	router := chi.NewRouter()
	h.Register(router, router)
	declared := make(map[string]bool)
	for _, route := range Routes {
		declared[route] = true
	}
	mounted := 0
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted++
		if !declared[method+" "+route] {
			t.Errorf("route %s %s is not in Routes", method, route)
		}
		return nil
	})
	if mounted != len(Routes) {
		t.Fatalf("%d routes mounted, %d declared", mounted, len(Routes))
	}
	before := string(v.vaultBytes(t))
	for _, request := range routeRequests() {
		response := serve(h, testUser, request[0], request[1], `{"passphrase":"`+testPassphrase+`","name":"x","value":"y","text":"A=1"}`)
		if response.Code != http.StatusForbidden {
			t.Errorf("%s %s as a user account: %d %s", request[0], request[1], response.Code, response.Body)
		}
	}
	if string(v.vaultBytes(t)) != before {
		t.Fatal("a refused request changed the vault")
	}
	if status, _ := v.Status(); status.Unlocked {
		t.Fatal("a user account unlocked the vault")
	}
}

func TestUnavailableVaultRefusesEveryRoute(t *testing.T) {
	h := NewHandler(nil, "coffre désactivé", "")
	for _, request := range routeRequests() {
		response := serve(h, testAdmin, request[0], request[1], `{}`)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "coffre désactivé") {
			t.Errorf("%s %s: %d %s", request[0], request[1], response.Code, response.Body)
		}
	}
}

func TestRoutesFlow(t *testing.T) {
	v := newTestVault(t)
	h := NewHandler(v.Vault, "", "passkey")
	target := filepath.Join(mkdir(t, v.home, "shop"), ".env")
	quoted := func(value string) string {
		raw, _ := json.Marshal(value)
		return string(raw)
	}
	var responses []*httptest.ResponseRecorder
	call := func(want int, code, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		// Passkey sessions carry no account: they are admins.
		var identity *auth.AccountIdentity
		if len(responses)%2 == 0 {
			identity = testAdmin
		}
		response := serve(h, identity, method, path, body)
		responses = append(responses, response)
		if response.Code != want || (code != "" && errorCode(response) != code) {
			t.Fatalf("%s %s: %d %s (want %d %s)", method, path, response.Code, response.Body, want, code)
		}
		// The body guard (415) and the router (405) answer by themselves,
		// with a fixed text.
		if want != http.StatusUnsupportedMediaType && want != http.StatusMethodNotAllowed &&
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: Cache-Control %q", method, path, response.Header().Get("Cache-Control"))
		}
		return response
	}

	if body := call(200, "", "GET", "/secrets/status", "").Body.String(); !strings.Contains(body, `"initialized":false`) {
		t.Fatalf("status before init: %s", body)
	}
	call(409, "not_initialized", "GET", "/secrets/apps", "")
	call(409, "not_initialized", "POST", "/secrets/unlock", `{"passphrase":`+quoted(testPassphrase)+`}`)
	call(400, "invalid", "POST", "/secrets/init", `{"passphrase":"too short"}`)
	call(400, "invalid", "POST", "/secrets/init", `{"passphrase":`+quoted(testPassphrase)+`,"extra":1}`)
	call(415, "", "POST", "/secrets/init", "")
	if body := call(200, "", "POST", "/secrets/init", `{"passphrase":`+quoted(testPassphrase)+`}`).Body.String(); !strings.Contains(body, `"initialized":true`) || strings.Contains(body, `"unlocked":true`) {
		t.Fatalf("init: %s", body)
	}
	call(409, "already_initialized", "POST", "/secrets/init", `{"passphrase":`+quoted(testPassphrase)+`}`)

	call(400, "invalid", "POST", "/secrets/apps", `{"name":"../x"}`)
	call(200, "", "POST", "/secrets/apps", `{"name":"shop"}`)
	call(409, "exists", "POST", "/secrets/apps", `{"name":"shop"}`)
	call(404, "not_found", "PUT", "/secrets/apps/other/vars/TOKEN", `{"value":"x"}`)
	call(400, "invalid", "PUT", "/secrets/apps/shop/vars/BAD-NAME", `{"value":"x"}`)
	call(400, "invalid", "PUT", "/secrets/apps/shop/vars/TOKEN", `{}`)
	// Locked: writing works, reading does not.
	call(200, "", "PUT", "/secrets/apps/shop/vars/TOKEN", `{"value":`+quoted(testValue)+`}`)
	call(200, "", "POST", "/secrets/apps/shop/import", `{"text":"DB_URL='postgres://u:`+testValue+`@db/app'\n"}`)
	call(400, "invalid", "POST", "/secrets/apps/shop/import", `{"text":"`+testValue+`"}`)
	call(423, "locked", "POST", "/secrets/apps/shop/vars/TOKEN/reveal", "")
	call(423, "locked", "POST", "/secrets/apps/shop/export", "")
	call(400, "invalid", "PUT", "/secrets/apps/shop/target", `{"env_file":"/etc/shop.env"}`)
	call(200, "", "PUT", "/secrets/apps/shop/target", `{"env_file":`+quoted(target)+`,"container":"shop-web"}`)
	call(423, "locked", "POST", "/secrets/apps/shop/apply", "")
	// A reveal is never a GET: it must go through the Origin and CSRF checks.
	call(405, "", "GET", "/secrets/apps/shop/vars/TOKEN/reveal", "")

	wrong := "this is not the passphrase"
	call(403, "wrong_passphrase", "POST", "/secrets/unlock", `{"passphrase":`+quoted(wrong)+`}`)
	if body := call(200, "", "POST", "/secrets/unlock", `{"passphrase":`+quoted(testPassphrase)+`}`).Body.String(); !strings.Contains(body, `"unlocked":true`) || !strings.Contains(body, `"expires_at"`) {
		t.Fatalf("unlock: %s", body)
	}
	listing := call(200, "", "GET", "/secrets/apps", "").Body.String()
	if !strings.Contains(listing, `"name":"TOKEN"`) || !strings.Contains(listing, `"name":"DB_URL"`) {
		t.Fatalf("listing: %s", listing)
	}
	audit := call(200, "", "GET", "/secrets/audit", "").Body.String()

	// Everything answered so far, errors included, is free of values and
	// passphrases.
	for _, response := range responses {
		for _, secret := range []string{testValue, testPassphrase, wrong, "AGE-SECRET-KEY"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatalf("a response holds %q: %s", secret, response.Body)
			}
		}
	}
	if !strings.Contains(audit, `"actor":"root"`) || !strings.Contains(audit, `"actor":"passkey"`) || !strings.Contains(audit, `"action":"unlock_failed"`) {
		t.Fatalf("audit: %s", audit)
	}

	if body := call(200, "", "POST", "/secrets/apps/shop/vars/TOKEN/reveal", "").Body.String(); body != `{"value":"`+testValue+`"}`+"\n" {
		t.Fatalf("reveal: %s", body)
	}
	call(404, "not_found", "POST", "/secrets/apps/shop/vars/MISSING/reveal", "")
	if body := call(200, "", "POST", "/secrets/apps/shop/export", "").Body.String(); !strings.Contains(body, "TOKEN="+testValue) {
		t.Fatalf("export: %s", body)
	}
	if body := call(200, "", "POST", "/secrets/apps/shop/apply", "").Body.String(); !strings.Contains(body, `"restarted":true`) || strings.Contains(body, testValue) {
		t.Fatalf("apply: %s", body)
	}
	if len(v.restarter.calls) != 1 || !strings.HasSuffix(v.restarter.calls[0], ":shop-web") {
		t.Fatalf("restarts: %v", v.restarter.calls)
	}
	call(200, "", "POST", "/secrets/lock", "")
	call(423, "locked", "POST", "/secrets/apps/shop/vars/TOKEN/reveal", "")
	call(200, "", "DELETE", "/secrets/apps/shop/vars/TOKEN", "")
	call(404, "not_found", "DELETE", "/secrets/apps/shop/vars/TOKEN", "")
	call(200, "", "DELETE", "/secrets/apps/shop", "")
	call(404, "not_found", "DELETE", "/secrets/apps/shop", "")
}

func TestUnlockRouteIsThrottled(t *testing.T) {
	v := newTestVault(t)
	v.ready(t)
	h := NewHandler(v.Vault, "", "passkey")
	for i := 0; i < freeAttempts; i++ {
		if response := serve(h, testAdmin, "POST", "/secrets/unlock", `{"passphrase":"wrong wrong wrong"}`); response.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i, response.Code)
		}
	}
	response := serve(h, testAdmin, "POST", "/secrets/unlock", `{"passphrase":"`+testPassphrase+`"}`)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "30" || errorCode(response) != "throttled" {
		t.Fatalf("blocked: %d %v %s", response.Code, response.Header(), response.Body)
	}
}

func TestOversizedBodiesAreRefused(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	h := NewHandler(v.Vault, "", "passkey")
	big := strings.Repeat("x", valueBody)
	if response := serve(h, testAdmin, "PUT", "/secrets/apps/shop/vars/A", `{"value":"`+big+`"}`); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized value: %d", response.Code)
	}
	if response := serve(h, testAdmin, "POST", "/secrets/unlock", `{"passphrase":"`+big[:8192]+`"}`); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized passphrase: %d", response.Code)
	}
}
