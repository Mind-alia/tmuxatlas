package dockerui

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

// Routes lists every route of the package, relative to /api. The server's
// account policy classifies them all as admin-only.
var Routes = []string{
	"GET /docker/containers",
	"GET /docker/containers/{id}/logs",
	"GET /docker/containers/{id}/inspect",
	"POST /docker/containers/{id}/start",
	"POST /docker/containers/{id}/stop",
	"POST /docker/containers/{id}/restart",
	"POST /docker/containers/{id}/terminal",
	"DELETE /docker/containers/{id}",
}

// Register mounts the routes. read carries the server's authentication;
// mutate adds the Origin and CSRF checks of every other state change.
func (s *Service) Register(read, mutate chi.Router) {
	read = read.With(AdminOnly)
	mutate = mutate.With(AdminOnly)
	read.Get("/docker/containers", s.handleList)
	read.Get("/docker/containers/{id}/logs", s.handleLogs)
	read.Get("/docker/containers/{id}/inspect", s.handleInspect)
	for _, action := range []string{"start", "stop", "restart"} {
		mutate.Post("/docker/containers/{id}/"+action, s.handleAction(action))
	}
	mutate.Post("/docker/containers/{id}/terminal", s.handleTerminal)
	mutate.Delete("/docker/containers/{id}", s.handleRemove)
}

// AdminOnly refuses signed-in accounts that are not admins. The account
// policy already does; this check stays if its allow-list ever changes.
// Passkey and local sessions have no account and are admins by design.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := auth.AccountFromContext(r.Context()); ok && identity.User.Role != "admin" {
			writeError(w, http.StatusForbidden, "accès interdit")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.List(r.Context()))
}

func (s *Service) handleLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	logs, err := s.Logs(r.Context(), chi.URLParam(r, "id"), query.Get("tail"), query.Get("since"))
	if err != nil {
		s.fail(w, r, "logs", "", err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (s *Service) handleInspect(w http.ResponseWriter, r *http.Request) {
	doc, err := s.Inspect(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "inspect", "", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc)
}

func (s *Service) handleAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := s.Action(r.Context(), action, chi.URLParam(r, "id"))
		if err != nil {
			s.fail(w, r, action, result.Name, err)
			return
		}
		s.audit(r, action, result.Name, result.ID).WithField("changed", result.Changed).Info("docker container action")
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	result, err := s.Remove(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, "delete", result.Name, err)
		return
	}
	s.audit(r, "delete", result.Name, result.ID).Info("docker container action")
	writeJSON(w, http.StatusOK, result)
}

func (s *Service) handleTerminal(w http.ResponseWriter, r *http.Request) {
	session, c, err := s.Terminal(r, chi.URLParam(r, "id"))
	if err != nil {
		name := ""
		if c.ID != "" {
			name = containerName(c.Names, c.ID)
		}
		s.fail(w, r, "terminal", name, err)
		return
	}
	s.audit(r, "terminal", containerName(c.Names, c.ID), c.ID).
		WithFields(logrus.Fields{"session": session.Session, "created": session.Created}).
		Info("docker container action")
	writeJSON(w, http.StatusOK, session)
}

// audit names who did what to which container.
func (s *Service) audit(r *http.Request, action, name, id string) *logrus.Entry {
	actor := "passkey"
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		actor = identity.User.Username
	}
	return s.logger.WithFields(logrus.Fields{
		"actor": actor, "remote": r.RemoteAddr, "request_id": chimiddleware.GetReqID(r.Context()),
		"action": action, "container": name, "container_id": shortID(id),
	})
}

// fail maps an error to its status. Mutations are logged even when refused;
// name is empty when the ID did not resolve to a container.
func (s *Service) fail(w http.ResponseWriter, r *http.Request, action, name string, err error) {
	status, message := errorStatus(err)
	if r.Method != http.MethodGet {
		id := chi.URLParam(r, "id")
		if !idPattern.MatchString(id) {
			id = "invalide" // never log raw input from the URL
		}
		s.audit(r, action, name, id).
			WithFields(logrus.Fields{"status": status, "error": message}).
			Warn("docker container action refused or failed")
	}
	writeError(w, status, message)
}

func errorStatus(err error) (int, string) {
	var request *requestError
	var conflict *conflictError
	var api *APIError
	switch {
	case errors.Is(err, errInvalidID), errors.Is(err, errAmbiguousID):
		return http.StatusBadRequest, err.Error()
	case errors.As(err, &request):
		return request.status, request.message
	case errors.Is(err, errNotFound):
		return http.StatusNotFound, err.Error()
	case errors.As(err, &conflict):
		return http.StatusConflict, conflict.message
	case errors.Is(err, errDisabled), errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, errTimeout):
		return http.StatusGatewayTimeout, err.Error()
	case errors.As(err, &api):
		switch api.Status {
		case http.StatusNotFound:
			return http.StatusNotFound, "conteneur introuvable"
		case http.StatusConflict, http.StatusBadRequest:
			return http.StatusConflict, "Docker refuse : " + api.Message
		default:
			return http.StatusBadGateway, "erreur Docker : " + api.Error()
		}
	default:
		return http.StatusBadGateway, err.Error()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
