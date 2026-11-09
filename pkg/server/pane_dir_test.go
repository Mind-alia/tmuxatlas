package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePaneDirValidatesPane(t *testing.T) {
	for name, tc := range map[string]struct {
		target string
		status int
	}{
		"missing pane":   {"/api/files/pane-dir", http.StatusBadRequest},
		"not a pane id":  {"/api/files/pane-dir?pane=main", http.StatusBadRequest},
		"injection":      {"/api/files/pane-dir?pane=%251;ls", http.StatusBadRequest},
		"no tmux client": {"/api/files/pane-dir?pane=%251", http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handlePaneDir(recorder, httptest.NewRequest(http.MethodGet, tc.target, nil), nil)
			if recorder.Code != tc.status {
				t.Fatalf("status %d, want %d", recorder.Code, tc.status)
			}
		})
	}
}
