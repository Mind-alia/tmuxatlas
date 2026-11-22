package deploy

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/httpguard"
)

// Routes lists every route of the package, relative to /api. The server's
// account policy classifies them all as admin-only.
var Routes = []string{
	"GET /docker/stacks/{project}/deploy",
	"GET /docker/stacks/{project}/deploy/history/{id}",
	"POST /docker/stacks/{project}/deploy",
	"POST /docker/stacks/{project}/deploy/rollback",
	"POST /docker/stacks/{project}/deploy/cancel",
}

const rollbackBody = 1 << 10

// Handler serves the routes. actor names sessions without an account.
type Handler struct {
	service *Service
	actor   string
}

// NewHandler serves service; actor names who acts when no account is signed
// in ("passkey", for instance).
func NewHandler(service *Service, actor string) *Handler {
	if actor == "" {
		actor = "passkey"
	}
	return &Handler{service: service, actor: actor}
}

// Register mounts the routes. read carries the server's authentication;
// mutate adds the Origin and CSRF checks of every other state change. Every
// route is admin-only, including the readings: a record holds build output.
func (h *Handler) Register(read, mutate chi.Router) {
	read = read.With(dockerui.AdminOnly)
	mutate = mutate.With(dockerui.AdminOnly, httpguard.BodyReadDeadline(15*time.Second))
	read.Get("/docker/stacks/{project}/deploy", h.state)
	read.Get("/docker/stacks/{project}/deploy/history/{id}", h.record)
	mutate.Post("/docker/stacks/{project}/deploy", h.deploy)
	mutate.With(httpguard.JSONBody(rollbackBody)).Post("/docker/stacks/{project}/deploy/rollback", h.rollback)
	mutate.Post("/docker/stacks/{project}/deploy/cancel", h.cancel)
}

func (h *Handler) who(r *http.Request) string {
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		return identity.User.Username
	}
	return h.actor
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// fail answers a refusal. Changes are logged even when refused; the stack is
// only named when it has the shape of a project name.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, action string, err error) {
	status, message := http.StatusInternalServerError, "erreur interne"
	var refusal *Error
	if errors.As(err, &refusal) {
		status, message = refusal.Status, refusal.Message
	}
	if r.Method != http.MethodGet {
		stack := chi.URLParam(r, "project")
		if !ValidProject(stack) {
			stack = "invalide"
		}
		h.service.logger.WithFields(logrus.Fields{
			"actor": h.who(r), "stack": stack, "action": action, "status": status, "error": message,
		}).Warn("application deployment refused")
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	state, err := h.service.State(r.Context(), chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, r, "state", err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) record(w http.ResponseWriter, r *http.Request) {
	record, err := h.service.Record(chi.URLParam(r, "project"), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, "record", err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (h *Handler) started(r *http.Request, record Record) {
	h.service.logger.WithFields(logrus.Fields{
		"actor": record.Actor, "stack": record.Stack, "action": record.Kind, "deployment": record.ID,
	}).Info("application deployment started")
}

func (h *Handler) deploy(w http.ResponseWriter, r *http.Request) {
	record, err := h.service.Deploy(r.Context(), chi.URLParam(r, "project"), h.who(r))
	if err != nil {
		h.fail(w, r, KindDeploy, err)
		return
	}
	h.started(r, record)
	writeJSON(w, http.StatusAccepted, record)
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		h.fail(w, r, KindRollback, refuse(http.StatusBadRequest, "requête illisible"))
		return
	}
	record, err := h.service.Rollback(r.Context(), chi.URLParam(r, "project"), body.ID, h.who(r))
	if err != nil {
		h.fail(w, r, KindRollback, err)
		return
	}
	h.started(r, record)
	writeJSON(w, http.StatusAccepted, record)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	record, err := h.service.Cancel(chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, r, "cancel", err)
		return
	}
	h.service.logger.WithFields(logrus.Fields{
		"actor": h.who(r), "stack": record.Stack, "action": "cancel", "deployment": record.ID,
	}).Info("application deployment cancellation asked")
	writeJSON(w, http.StatusOK, record)
}
