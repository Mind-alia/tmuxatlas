package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFileListingStopsOnABadPeer(t *testing.T) {
	for _, tt := range []struct {
		name      string
		next      func(offset int) int
		status    int
		maxCalls  int
		truncated bool
	}{
		{"next does not advance", func(int) int { return 0 }, http.StatusBadGateway, 2, false},
		{"next goes backwards", func(offset int) int { return map[bool]int{true: 5, false: 1}[offset == 0] }, http.StatusBadGateway, 2, false},
		{"endless small pages", func(offset int) int { return offset + 1 }, http.StatusOK, maxListPages, true},
		{"last page", func(int) int { return -1 }, http.StatusOK, 1, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			api := &filesAPI{opts: &Options{}, active: make(map[string]int)}
			api.execute = func(_ context.Context, _, _ string, payload any) (json.RawMessage, error) {
				calls++
				if calls > maxListPages+1 {
					t.Fatal("listing did not stop")
				}
				offset := payload.(map[string]any)["offset"].(int)
				return json.RawMessage(fmt.Sprintf(`{"path":"/","entries":[{"name":"f%d"}],"next":%d}`, offset, tt.next(offset))), nil
			}
			w := httptest.NewRecorder()
			api.list(w, httptest.NewRequest(http.MethodGet, "/api/files/list?host_id=h&path=/", nil))
			if w.Code != tt.status || calls > tt.maxCalls {
				t.Fatalf("status %d after %d calls, want %d within %d", w.Code, calls, tt.status, tt.maxCalls)
			}
			if tt.status == http.StatusOK {
				var body struct {
					Truncated bool `json:"truncated"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				if body.Truncated != tt.truncated {
					t.Fatalf("truncated = %v", body.Truncated)
				}
			}
		})
	}
}
