package dbbackup

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/httpguard"
)

// Routes lists every route of the package, relative to /api. The server's
// account policy classifies them all as admin-only.
var Routes = []string{
	"GET /db-backups",
	"POST /db-backups/{container}/backup",
	"PUT /db-backups/{container}/settings",
	"GET /db-backups/{container}/files/{id}/download",
	"DELETE /db-backups/{container}/files/{id}",
	"POST /db-backups/{container}/files/{id}/restore",
}

// Handler serves the database backups over HTTP.
type Handler struct {
	manager *Manager
	// problem, when set, refuses every route with this message.
	problem string
	// actor names who acts when no account is signed in.
	actor  string
	logger *logrus.Entry
}

// NewHandler serves manager. A nil manager, or a non-empty problem, answers
// 503 on every route.
func NewHandler(manager *Manager, problem, actor string) *Handler {
	if manager == nil && problem == "" {
		problem = "sauvegardes de bases indisponibles sur ce serveur"
	}
	if actor == "" {
		actor = "passkey"
	}
	return &Handler{manager: manager, problem: problem, actor: actor, logger: logrus.WithField("component", "dbbackup")}
}

// Register mounts the routes. read carries the server's authentication;
// mutate adds the Origin and CSRF checks of every other state change.
func (h *Handler) Register(read, mutate chi.Router) {
	read = read.With(adminOnly, h.available)
	mutate = mutate.With(adminOnly, h.available, httpguard.BodyReadDeadline(15*time.Second))
	small := mutate.With(httpguard.JSONBody(httpguard.SmallJSONLimit))

	read.Get("/db-backups", h.list)
	mutate.Post("/db-backups/{container}/backup", h.backup)
	small.Put("/db-backups/{container}/settings", h.settings)
	read.Get("/db-backups/{container}/files/{id}/download", h.download)
	mutate.Delete("/db-backups/{container}/files/{id}", h.remove)
	small.Post("/db-backups/{container}/files/{id}/restore", h.restore)
}

// adminOnly refuses signed-in accounts that are not admins. The account
// policy already does; this check stays if its allow-list ever changes.
// Passkey sessions have no account and are admins by design.
func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := auth.AccountFromContext(r.Context()); ok && identity.User.Role != "admin" {
			writeError(w, http.StatusForbidden, "accès interdit")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) available(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.problem != "" || h.manager == nil {
			writeError(w, http.StatusServiceUnavailable, h.problem)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// usable applies the switches of the Containers view to a route that acts
// on files only: with Docker closed, nothing here answers but the listing.
func (h *Handler) usable(w http.ResponseWriter) bool {
	if problem := h.manager.docker.Problem(); problem != "" {
		writeError(w, http.StatusServiceUnavailable, problem)
		return false
	}
	return true
}

func (h *Handler) who(r *http.Request) string {
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		return identity.User.Username
	}
	return h.actor
}

// requestTarget reads the container and, when the route has one, the backup ID.
// Both are validated here, and nothing else from the request ever names a
// file.
func requestTarget(w http.ResponseWriter, r *http.Request, withID bool) (name, id string, ok bool) {
	name, id = chi.URLParam(r, "container"), chi.URLParam(r, "id")
	if !ValidName(name) || (withID && !ValidID(id)) {
		writeError(w, http.StatusBadRequest, "sauvegarde invalide")
		return "", "", false
	}
	return name, id, true
}

func (h *Handler) audit(r *http.Request, action, name, id string) *logrus.Entry {
	return h.logger.WithFields(logrus.Fields{
		"actor": h.who(r), "remote": r.RemoteAddr, "action": action, "container": name, "backup": id,
	})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, action, name, id string, err error) {
	status, message := errorStatus(err)
	h.audit(r, action, name, id).WithFields(logrus.Fields{"status": status, "error": message}).Warn("database backup action refused or failed")
	writeError(w, status, message)
}

func errorStatus(err error) (int, string) {
	var refusal *Error
	switch {
	case errors.As(err, &refusal):
		return refusal.Status, refusal.Message
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, errInvalidName), errors.Is(err, errInvalidID):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusBadGateway, err.Error()
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.manager.List(r.Context()))
}

func (h *Handler) backup(w http.ResponseWriter, r *http.Request) {
	name, _, ok := requestTarget(w, r, false)
	if !ok || !h.usable(w) {
		return
	}
	if err := h.manager.StartBackup(r.Context(), name); err != nil {
		h.fail(w, r, "backup", name, "", err)
		return
	}
	h.audit(r, "backup", name, "").Info("database backup requested")
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	name, _, ok := requestTarget(w, r, false)
	if !ok || !h.usable(w) {
		return
	}
	var settings Settings
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	if !h.manager.Known(r.Context(), name) {
		h.fail(w, r, "settings", name, "", ErrNoDatabase)
		return
	}
	if err := h.manager.SetSettings(name, settings); err != nil {
		h.fail(w, r, "settings", name, "", err)
		return
	}
	h.audit(r, "settings", name, "").WithFields(logrus.Fields{"mode": settings.Mode, "keep": settings.Keep}).Info("database backup settings changed")
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	name, id, ok := requestTarget(w, r, true)
	if !ok || !h.usable(w) {
		return
	}
	file, rec, err := h.manager.Open(name, id)
	if err != nil {
		status, message := errorStatus(err)
		writeError(w, status, message)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	h.audit(r, "download", name, id).Info("database backup downloaded")
	header := w.Header()
	header.Set("Content-Type", "application/gzip")
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + "-" + rec.ID + dumpSuffix}))
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "", time.Time{}, file)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	name, id, ok := requestTarget(w, r, true)
	if !ok || !h.usable(w) {
		return
	}
	if err := h.manager.Delete(name, id); err != nil {
		h.fail(w, r, "delete", name, id, err)
		return
	}
	h.audit(r, "delete", name, id).Info("database backup deleted")
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	name, id, ok := requestTarget(w, r, true)
	if !ok || !h.usable(w) {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	if err := h.manager.StartRestore(name, id, body.Confirm, h.who(r)); err != nil {
		h.fail(w, r, "restore", name, id, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
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
