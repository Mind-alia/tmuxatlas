package httpguard

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJSONBodyRejectsInvalidInputsBeforeMutation(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		limit       int64
		wantStatus  int
	}{
		{name: "wrong media type", contentType: "text/plain", body: `{}`, limit: 32, wantStatus: http.StatusUnsupportedMediaType},
		{name: "oversized", contentType: "application/json", body: `{"value":"too long"}`, limit: 8, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "trailing value", contentType: "application/json", body: `{} {}`, limit: 32, wantStatus: http.StatusBadRequest},
		{name: "malformed", contentType: "application/json", body: `{`, limit: 32, wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := false
			handler := JSONBody(tt.limit)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				mutated = true
			}))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if mutated {
				t.Fatal("mutation ran after rejected request")
			}
		})
	}
}

func TestJSONBodyAcceptsSingleJSONValue(t *testing.T) {
	called := false
	handler := JSONBody(32)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("status = %d, called = %v", rec.Code, called)
	}
}

// A body sent byte by byte gets its error once the deadline passes: the
// connection is closed rather than drained, which net/http would do with no
// deadline left before answering.
func TestSlowBodyIsRefusedAtTheDeadline(t *testing.T) {
	mutated := false
	server := httptest.NewServer(BodyReadDeadline(300 * time.Millisecond)(JSONBody(1 << 10)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mutated = true
	}))))
	t.Cleanup(server.Close)
	for _, tt := range []struct {
		contentType string
		want        string
	}{
		{"application/json", "HTTP/1.1 400"},
		{"text/plain", "HTTP/1.1 415"},
	} {
		t.Run(tt.contentType, func(t *testing.T) {
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			started := time.Now()
			fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: atlas\r\nContent-Type: %s\r\nContent-Length: 1000\r\n\r\n{", tt.contentType)
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				for {
					select {
					case <-stop:
						return
					case <-time.After(50 * time.Millisecond):
						if _, err := conn.Write([]byte(" ")); err != nil {
							return
						}
					}
				}
			}()
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			status, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil || !strings.HasPrefix(status, tt.want) || time.Since(started) > 2*time.Second {
				t.Fatalf("after %s: %q %v", time.Since(started).Round(time.Millisecond), status, err)
			}
		})
	}
	if mutated {
		t.Fatal("handler ran with an unread body")
	}
}
