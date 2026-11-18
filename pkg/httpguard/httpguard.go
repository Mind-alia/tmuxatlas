package httpguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"
)

const (
	GlobalBodyLimit = 1 << 20
	SmallJSONLimit  = 4 << 10
	WebAuthnLimit   = 128 << 10
	PairingLimit    = 16 << 10
)

func GlobalBodyLimitMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// JSONBody validates the media type, reads a bounded body, and accepts exactly
// one JSON value before restoring the body for the route's decoder. A body it
// leaves unread, or could not read before the BodyReadDeadline deadline, ends
// the connection (see closeUnread).
func JSONBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				closeUnread(w, r)
				http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
			if err != nil {
				closeUnread(w, r)
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			decoder := json.NewDecoder(bytes.NewReader(body))
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			var trailing json.RawMessage
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				http.Error(w, "exactly one JSON value required", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// closeUnread closes an HTTP/1 connection after the answer instead of
// letting net/http drain the body left unread: that drain has no deadline
// once BodyReadDeadline has returned, and the answer waits for it, so a body
// sent byte by byte would still hold the connection. HTTP/2 resets the
// stream instead of draining it.
func closeUnread(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "close")
	}
}

func BodyReadDeadline(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(time.Now().Add(timeout)); err == nil {
				defer controller.SetReadDeadline(time.Time{})
			}
			next.ServeHTTP(w, r)
		})
	}
}
