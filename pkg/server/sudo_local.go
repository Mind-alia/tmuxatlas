package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

const (
	sudoLocalBodyLimit = 8 << 10
	// sudoMaxWait stays below the 10 minute ceiling of Claude's Bash tool.
	sudoMaxWait = 9 * time.Minute
)

// registerSudoLocalRoutes exposes the agent side of sudo requests on the
// local Unix socket: ask, wait, give up. Nothing here can approve.
func registerSudoLocalRoutes(mux *http.ServeMux, broker sudo.Broker) {
	if broker == nil {
		return
	}
	mux.HandleFunc("POST /api/sudo/requests", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command string      `json:"command"`
			Reason  string      `json:"reason"`
			Origin  sudo.Origin `json:"origin"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, sudoLocalBodyLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeSudoError(w, sudo.ErrInvalidText)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		view, err := broker.Create(ctx, sudo.CreateInput{Origin: body.Origin, Command: body.Command, Reason: body.Reason})
		if err != nil {
			writeSudoError(w, err)
			return
		}
		writeSudoJSON(w, http.StatusCreated, view)
	})
	mux.HandleFunc("GET /api/sudo/requests/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		timeout := 8 * time.Minute
		if raw := r.URL.Query().Get("timeout"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil || parsed <= 0 {
				writeSudoError(w, sudo.ErrInvalidText)
				return
			}
			timeout = min(parsed, sudoMaxWait)
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		view, err := broker.Wait(ctx, r.PathValue("id"))
		if err != nil {
			writeSudoError(w, err)
			return
		}
		writeSudoJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("POST /api/sudo/requests/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		view, err := broker.Cancel(ctx, r.PathValue("id"))
		if err != nil {
			writeSudoError(w, err)
			return
		}
		writeSudoJSON(w, http.StatusOK, view)
	})
}

func writeSudoJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeSudoError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, sudo.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, sudo.ErrInvalidCommand), errors.Is(err, sudo.ErrInvalidText):
		status = http.StatusBadRequest
	case errors.Is(err, sudo.ErrInvalidState), errors.Is(err, sudo.ErrVersionMismatch):
		status = http.StatusConflict
	case errors.Is(err, sudo.ErrGrantInvalid), errors.Is(err, sudo.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, sudo.ErrTooMany):
		status = http.StatusTooManyRequests
	case errors.Is(err, sudo.ErrUnavailable):
		status = http.StatusServiceUnavailable
	}
	message := err.Error()
	if sudo.ErrorCode(err) == "unavailable" {
		message = sudo.ErrUnavailable.Error()
	}
	writeSudoJSON(w, status, map[string]string{"error": message, "code": sudo.ErrorCode(err)})
}
