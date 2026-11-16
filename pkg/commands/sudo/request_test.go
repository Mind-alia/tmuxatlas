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
		{sudo.StatusDelivered, exitDelivered},
		{sudo.StatusWithheld, exitNotRun},
		{sudo.StatusRefused, exitNotRun},
		{sudo.StatusExpired, exitNotRun},
		{sudo.StatusPending, exitPending},
		{sudo.StatusAwaitingReview, exitPending},
	}
	devnull, _ := os.Open(os.DevNull)
	stdout := os.Stdout
	os.Stdout = devnull
	defer func() { os.Stdout = stdout }()
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			err := finish(sudo.AgentView{ID: "sudo-x", Status: tt.status})
			code := exitDelivered
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
		_, _ = w.Write([]byte(`{"id":"` + r.PathValue("id") + `","status":"delivered","command_requested":"id"}`))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	client := newSocketClient(path)
	if _, err := client.do(context.Background(), http.MethodPost, "/api/sudo/requests", map[string]string{}); !errors.Is(err, sudo.ErrTooMany) {
		t.Fatalf("create error = %v", err)
	}
	view, err := client.do(context.Background(), http.MethodGet, "/api/sudo/requests/sudo-1/wait", nil)
	if err != nil || view.Status != sudo.StatusDelivered || view.ID != "sudo-1" {
		t.Fatalf("wait = %+v, %v", view, err)
	}
	if _, err := client.do(context.Background(), http.MethodGet, "/api/nothing", nil); !errors.Is(err, sudo.ErrUnavailable) {
		t.Fatalf("old server error = %v", err)
	}
}
