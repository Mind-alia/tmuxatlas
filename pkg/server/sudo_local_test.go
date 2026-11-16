package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

func sudoLocalMux(store *sudo.Store) *http.ServeMux {
	mux := http.NewServeMux()
	registerSudoLocalRoutes(mux, sudo.StoreBroker{Store: store, HostID: "host-local", Host: "local"})
	return mux
}

func doSudoLocal(t *testing.T, mux http.Handler, method, path, body string) (*httptest.ResponseRecorder, sudo.AgentView) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var view sudo.AgentView
	_ = json.Unmarshal(response.Body.Bytes(), &view)
	return response, view
}

func TestSudoLocalRoutes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		body   string
		status int
	}{
		{"create", http.MethodPost, `{"command":"apt-get install -y jq","reason":"jq","origin":{"session":"claude","pane":"%1"}}`, http.StatusCreated},
		{"multi-line command", http.MethodPost, `{"command":"true\nreboot"}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, `{"command":"true","approve":true}`, http.StatusBadRequest},
		{"no GET listing on the socket", http.MethodGet, ``, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := sudo.NewStore(sudo.DefaultLimits(), nil)
			response, _ := doSudoLocal(t, sudoLocalMux(store), tt.method, "/api/sudo/requests", tt.body)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", response.Code, tt.status, response.Body)
			}
		})
	}
}

func TestSudoLocalStampsHostAndWaits(t *testing.T) {
	store := sudo.NewStore(sudo.DefaultLimits(), nil)
	mux := sudoLocalMux(store)
	_, created := doSudoLocal(t, mux, http.MethodPost, "/api/sudo/requests",
		`{"command":"id","origin":{"host_id":"forged","session":"claude"}}`)
	request, ok := store.Get(created.ID)
	if !ok || request.Origin.HostID != "host-local" {
		t.Fatalf("origin = %+v", request.Origin)
	}
	response, view := doSudoLocal(t, mux, http.MethodGet, "/api/sudo/requests/"+created.ID+"/wait?timeout=20ms", "")
	if response.Code != http.StatusOK || view.Status != sudo.StatusPending {
		t.Fatalf("wait = %d %+v", response.Code, view)
	}
	if _, err := store.Refuse(created.ID, sudo.Actor{ID: "a", Name: "admin"}, "non"); err != nil {
		t.Fatal(err)
	}
	_, view = doSudoLocal(t, mux, http.MethodGet, "/api/sudo/requests/"+created.ID+"/wait?timeout=1s", "")
	if view.Status != sudo.StatusRefused || view.Note != "non" {
		t.Fatalf("wait after refuse = %+v", view)
	}
}

func TestSudoLocalCancel(t *testing.T) {
	store := sudo.NewStore(sudo.DefaultLimits(), nil)
	mux := sudoLocalMux(store)
	_, created := doSudoLocal(t, mux, http.MethodPost, "/api/sudo/requests", `{"command":"id"}`)
	response, view := doSudoLocal(t, mux, http.MethodPost, "/api/sudo/requests/"+created.ID+"/cancel", "")
	if response.Code != http.StatusOK || view.Status != sudo.StatusCancelled {
		t.Fatalf("cancel = %d %+v", response.Code, view)
	}
	other, _ := store.Create(sudo.CreateInput{Origin: sudo.Origin{HostID: "host-other"}, Command: "id"})
	response, _ = doSudoLocal(t, mux, http.MethodPost, "/api/sudo/requests/"+other.ID+"/cancel", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("cancel of another host's request = %d", response.Code)
	}
	response, _ = doSudoLocal(t, mux, http.MethodGet, "/api/sudo/requests/"+other.ID+"/wait?timeout=10ms", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("wait on another host's request = %d", response.Code)
	}
}
