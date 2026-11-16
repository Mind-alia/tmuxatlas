package sudocmd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

func TestFinishExitCodes(t *testing.T) {
	tests := []struct {
		status sudo.Status
		code   int
	}{
		{sudo.StatusDone, exitDone},
		{sudo.StatusRefused, exitNotDone},
		{sudo.StatusExpired, exitNotDone},
		{sudo.StatusCancelled, exitNotDone},
		{sudo.StatusPending, exitPending},
		{sudo.StatusRunning, exitPending},
		{sudo.StatusClosed, exitPending},
	}
	devnull, _ := os.Open(os.DevNull)
	stdout := os.Stdout
	os.Stdout = devnull
	defer func() { os.Stdout = stdout }()
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			err := finish(sudo.AgentView{ID: "sudo-x", Status: tt.status})
			code := exitDone
			var exit exitError
			if errors.As(err, &exit) {
				code = exit.code
			}
			if code != tt.code {
				t.Fatalf("exit code = %d, want %d", code, tt.code)
			}
		})
	}
}

func TestSocketClientMapsErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sudo/requests", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"trop","code":"too-many"}`))
	})
	mux.HandleFunc("GET /api/sudo/requests/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","status":"done","note":"fait"}`))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	client := newSocketClient(path)
	if _, err := client.do(context.Background(), http.MethodPost, "/api/sudo/requests", map[string]string{}); !errors.Is(err, sudo.ErrTooMany) {
		t.Fatalf("create error = %v", err)
	}
	view, err := client.do(context.Background(), http.MethodGet, "/api/sudo/requests/sudo-1/wait", nil)
	if err != nil || view.Status != sudo.StatusDone || view.ID != "sudo-1" || view.Note != "fait" {
		t.Fatalf("wait = %+v, %v", view, err)
	}
	if _, err := client.do(context.Background(), http.MethodGet, "/api/nothing", nil); !errors.Is(err, sudo.ErrUnavailable) {
		t.Fatalf("old server error = %v", err)
	}
}
