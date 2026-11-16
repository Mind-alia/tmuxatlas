package loops

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

// Handler exposes HTTP endpoints for loops and quick chats.
type Handler struct {
	mgr *Manager
}

// NewHandler creates an HTTP handler backed by mgr.
func NewHandler(mgr *Manager) *Handler {
	return &Handler{mgr: mgr}
}

func callerIdentity(r *http.Request) (ownerID, ownerName string, isAdmin bool) {
	identity, ok := auth.AccountFromContext(r.Context())
	if !ok {
		return "", "", true
	}
	return identity.User.ID, identity.User.Username, identity.User.Role == "admin"
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

func writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// Meta serves available projects, per-CLI accounts, CLI availability, and presets.
func (h *Handler) Meta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.mgr.Meta())
}

// List returns all loops accessible to the caller.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ownerID, _, isAdmin := callerIdentity(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"loops": h.mgr.List(ownerID, isAdmin),
	})
}

// Logs returns the log output and git diff for a loop iteration.
func (h *Handler) Logs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "identifiant de boucle requis")
		return
	}
	iter := ParseIterationQuery(r.URL.Query().Get("iter"))
	ownerID, _, isAdmin := callerIdentity(r)
	resp, err := h.mgr.ReadLog(id, iter, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Create registers a new improvement loop.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "requête JSON invalide")
		return
	}
	req.OwnerID, req.OwnerName, _ = callerIdentity(r)
	loop, err := h.mgr.Create(req)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, loop)
}

// Update edits a stopped/idle/completed loop.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "identifiant et paramètres requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	loop, err := h.mgr.Update(req, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loop)
}

// Delete removes a loop.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "identifiant requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	if err := h.mgr.Delete(req.ID, ownerID, isAdmin); err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Start begins or resumes a loop.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "identifiant requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	loop, err := h.mgr.Start(req.ID, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loop)
}

// Pause asks a running loop to stop after the current iteration.
func (h *Handler) Pause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "identifiant requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	loop, err := h.mgr.Pause(req.ID, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loop)
}

// Stop immediately cancels a running loop.
func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "identifiant requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	loop, err := h.mgr.Stop(req.ID, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loop)
}

// WorkerAction toggles, pauses, resumes, enables, or retries a loop subscription.
func (h *Handler) WorkerAction(w http.ResponseWriter, r *http.Request) {
	var req WorkerActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LoopID == "" || req.Action == "" {
		writeError(w, http.StatusBadRequest, "identifiant de boucle et action requis")
		return
	}
	ownerID, _, isAdmin := callerIdentity(r)
	loop, err := h.mgr.WorkerAction(req, ownerID, isAdmin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loop)
}

// Chat opens an interactive tmux chat session for claude, codex, or agy.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "requête JSON invalide")
		return
	}
	req.OwnerID, req.OwnerName, _ = callerIdentity(r)
	resp, err := h.mgr.OpenChat(req)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Instruction queues a user instruction for the next pass.
func (h *Handler) Instruction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ID == "" {
		writeError(w, 400, "identifiant requis")
		return
	}
	owner, _, admin := callerIdentity(r)
	loop, err := h.mgr.SetInstruction(req.ID, req.Text, owner, admin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, 200, loop)
}

func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "requête invalide")
		return
	}
	owner, _, admin := callerIdentity(r)
	l, err := h.mgr.Review(req.ID, owner, admin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, 200, l)
}

func (h *Handler) Plan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string     `json:"id"`
		Plan []PlanStep `json:"plan"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, 400, "requête invalide")
		return
	}
	owner, _, admin := callerIdentity(r)
	l, err := h.mgr.SetPlan(req.ID, req.Plan, owner, admin)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, 200, l)
}
