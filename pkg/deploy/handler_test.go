package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

var (
	testAdmin = &auth.AccountIdentity{User: accounts.User{ID: "1", Username: "root-admin", Role: "admin"}}
	testUser  = &auth.AccountIdentity{User: accounts.User{ID: "2", Username: "alice", Role: "user"}}
)

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

func TestEveryRouteIsDeclaredAndAdminOnly(t *testing.T) {
	f := newFixture(t, nil)
	h := NewHandler(f.service, "passkey")
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
	for _, route := range Routes {
		method, path, _ := strings.Cut(route, " ")
		path = strings.NewReplacer("{project}", "shop", "{id}", "20260105T100000.000000000Z-0a0b0c0d").Replace(path)
		if response := serve(h, testUser, method, path, `{"id":"20260105T100000.000000000Z-0a0b0c0d"}`); response.Code != http.StatusForbidden {
			t.Errorf("%s %s as a user account: %d %s", method, path, response.Code, response.Body)
		}
	}
	if got := f.runner.commands(); len(got) != 0 {
		t.Fatalf("a refused request ran commands: %v", got)
	}
}

func TestRoutesFlow(t *testing.T) {
	f := newFixture(t, nil)
	h := NewHandler(f.service, "passkey")
	const base = "/docker/stacks/shop/deploy"

	response := serve(h, testAdmin, http.MethodGet, base, "")
	var state State
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &state) != nil || !state.Enabled || state.Git.Behind != 2 {
		t.Fatalf("état : %d %s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("réponse sans no-store")
	}

	f.runner.builds["shop-web"] = imageWebNew
	response = serve(h, testAdmin, http.MethodPost, base, "")
	var started Record
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &started) != nil || started.Actor != "root-admin" || started.Result != ResultRunning {
		t.Fatalf("déploiement : %d %s", response.Code, response.Body)
	}
	f.service.Wait("shop")
	response = serve(h, testAdmin, http.MethodGet, base+"/history/"+started.ID, "")
	var record Record
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &record) != nil || record.Result != ResultSuccess || record.step(stepFetch).Output == "" {
		t.Fatalf("enregistrement : %d %s", response.Code, response.Body)
	}

	// A session without account (passkey) acts under the handler's name.
	response = serve(h, nil, http.MethodPost, base+"/rollback", `{"id":"`+started.ID+`"}`)
	var rollback Record
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &rollback) != nil || rollback.Actor != "passkey" || rollback.Kind != KindRollback {
		t.Fatalf("retour arrière : %d %s", response.Code, response.Body)
	}
	f.service.Wait("shop")
	if done, _ := f.service.Record("shop", rollback.ID); done.Result != ResultSuccess {
		t.Fatalf("retour arrière : %s %q", done.Result, done.Error)
	}

	for name, tc := range map[string]struct {
		method, path, body string
		want               int
	}{
		"unknown stack":       {http.MethodGet, "/docker/stacks/unknown/deploy", "", http.StatusNotFound},
		"invalid stack":       {http.MethodPost, "/docker/stacks/shop%3Bid/deploy", "", http.StatusBadRequest},
		"unknown record":      {http.MethodGet, base + "/history/20200101T000000.000000000Z-00000000", "", http.StatusNotFound},
		"invalid record":      {http.MethodGet, base + "/history/..%2F..%2Fx", "", http.StatusBadRequest},
		"rollback no body":    {http.MethodPost, base + "/rollback", "", http.StatusUnsupportedMediaType},
		"rollback extra":      {http.MethodPost, base + "/rollback", `{"id":"` + started.ID + `","image":"evil"}`, http.StatusBadRequest},
		"rollback free text":  {http.MethodPost, base + "/rollback", `{"id":"--volume=/:/host"}`, http.StatusBadRequest},
		"cancel when idle":    {http.MethodPost, base + "/cancel", "", http.StatusConflict},
		"rollback big body":   {http.MethodPost, base + "/rollback", `{"id":"` + strings.Repeat("a", 4096) + `"}`, http.StatusRequestEntityTooLarge},
		"rollback not latest": {http.MethodPost, base + "/rollback", `{"id":"` + started.ID + `"}`, http.StatusConflict},
	} {
		if response := serve(h, testAdmin, tc.method, tc.path, tc.body); response.Code != tc.want {
			t.Errorf("%s : %d %s", name, response.Code, response.Body)
		}
	}
}
