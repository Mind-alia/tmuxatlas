package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/httpguard"
)

// Routes lists every route of the package, relative to /api. The server's
// account policy classifies them all as admin-only.
var Routes = []string{
	"GET /secrets/status",
	"POST /secrets/init",
	"POST /secrets/unlock",
	"POST /secrets/lock",
	"GET /secrets/apps",
	"POST /secrets/apps",
	"DELETE /secrets/apps/{app}",
	"PUT /secrets/apps/{app}/target",
	"PUT /secrets/apps/{app}/vars/{name}",
	"DELETE /secrets/apps/{app}/vars/{name}",
	"POST /secrets/apps/{app}/vars/{name}/reveal",
	"POST /secrets/apps/{app}/import",
	"POST /secrets/apps/{app}/export",
	"POST /secrets/apps/{app}/apply",
	"GET /secrets/audit",
}

// Body limits: a value may be MaxValueBytes of text, which JSON escaping can
// at worst multiply by six.
const (
	smallBody  = httpguard.SmallJSONLimit
	valueBody  = 6*MaxValueBytes + 1024
	importBody = 2*MaxImportBytes + 1024
)

// Handler serves the vault over HTTP.
type Handler struct {
	vault *Vault
	// problem, when set, refuses every route with this message.
	problem string
	// actor names who acts when no account is signed in.
	actor  string
	logger *logrus.Entry
}

// NewHandler serves vault. A nil vault, or a non-empty problem, answers 503
// with the problem on every route. actor names sessions without an account
// ("passkey", for instance).
func NewHandler(vault *Vault, problem, actor string) *Handler {
	if vault == nil && problem == "" {
		problem = "coffre de secrets indisponible sur ce serveur"
	}
	if actor == "" {
		actor = "passkey"
	}
	return &Handler{vault: vault, problem: problem, actor: actor, logger: logrus.WithField("component", "secrets")}
}

// Register mounts the routes. read carries the server's authentication;
// mutate adds the Origin and CSRF checks of every other state change. A
// value is only ever returned by a route of mutate.
func (h *Handler) Register(read, mutate chi.Router) {
	read = read.With(adminOnly, h.available)
	mutate = mutate.With(adminOnly, h.available, httpguard.BodyReadDeadline(15*time.Second))
	small := mutate.With(httpguard.JSONBody(smallBody))

	read.Get("/secrets/status", h.status)
	small.Post("/secrets/init", h.init)
	small.Post("/secrets/unlock", h.unlock)
	mutate.Post("/secrets/lock", h.lock)
	read.Get("/secrets/apps", h.apps)
	small.Post("/secrets/apps", h.createApp)
	mutate.Delete("/secrets/apps/{app}", h.deleteApp)
	small.Put("/secrets/apps/{app}/target", h.setTarget)
	mutate.With(httpguard.JSONBody(valueBody)).Put("/secrets/apps/{app}/vars/{name}", h.setVar)
	mutate.Delete("/secrets/apps/{app}/vars/{name}", h.deleteVar)
	mutate.Post("/secrets/apps/{app}/vars/{name}/reveal", h.reveal)
	mutate.With(httpguard.JSONBody(importBody)).Post("/secrets/apps/{app}/import", h.importDotenv)
	mutate.Post("/secrets/apps/{app}/export", h.export)
	mutate.Post("/secrets/apps/{app}/apply", h.apply)
	read.Get("/secrets/audit", h.audit)
}

// adminOnly refuses signed-in accounts that are not admins. The account
// policy already does; this check stays if its allow-list ever changes.
// Passkey sessions have no account and are admins by design.
func adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if identity, ok := auth.AccountFromContext(r.Context()); ok && identity.User.Role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "accès interdit")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) available(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.problem != "" || h.vault == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", h.problem)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) who(r *http.Request) string {
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		return identity.User.Username
	}
	return h.actor
}

// session identifies the browser session of the request, for the unlock it
// may hold, with the channel closed when that session ends. The cookie is
// only kept as a hash. A server without authentication has no session: its
// unlock is shared.
func session(r *http.Request) (string, <-chan struct{}) {
	token := auth.SessionCookie(r)
	if token == "" {
		return "", nil
	}
	sum := sha256.Sum256([]byte(token))
	done, _ := auth.SessionDone(r.Context())
	return hex.EncodeToString(sum[:]), done
}

// decode reads the JSON body into out; unknown fields are refused. The
// error never quotes the body.
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "requête invalide")
		return false
	}
	return true
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	id, _ := session(r)
	status, err := h.vault.Status(id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type passphraseBody struct {
	Passphrase string `json:"passphrase"`
}

func (h *Handler) init(w http.ResponseWriter, r *http.Request) {
	var body passphraseBody
	if !decode(w, r, &body) {
		return
	}
	if err := h.vault.Init(h.who(r), body.Passphrase); err != nil {
		h.fail(w, r, err)
		return
	}
	h.status(w, r)
}

func (h *Handler) unlock(w http.ResponseWriter, r *http.Request) {
	var body passphraseBody
	if !decode(w, r, &body) {
		return
	}
	id, done := session(r)
	status, err := h.vault.Unlock(h.who(r), id, body.Passphrase, done)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) lock(w http.ResponseWriter, r *http.Request) {
	h.vault.Lock(h.who(r))
	h.status(w, r)
}

func (h *Handler) apps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.vault.Apps()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	app, err := h.vault.CreateApp(h.who(r), body.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) deleteApp(w http.ResponseWriter, r *http.Request) {
	if err := h.vault.DeleteApp(h.who(r), chi.URLParam(r, "app")); err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (h *Handler) setTarget(w http.ResponseWriter, r *http.Request) {
	var body Target
	if !decode(w, r, &body) {
		return
	}
	app, err := h.vault.SetTarget(h.who(r), chi.URLParam(r, "app"), body.EnvFile, body.Container)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) setVar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value *string `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Value == nil {
		writeError(w, http.StatusBadRequest, "invalid", "valeur manquante")
		return
	}
	app, err := h.vault.SetVar(h.who(r), chi.URLParam(r, "app"), chi.URLParam(r, "name"), *body.Value)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) deleteVar(w http.ResponseWriter, r *http.Request) {
	app, err := h.vault.DeleteVar(h.who(r), chi.URLParam(r, "app"), chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, app)
}

func (h *Handler) reveal(w http.ResponseWriter, r *http.Request) {
	id, _ := session(r)
	value, err := h.vault.Reveal(h.who(r), id, chi.URLParam(r, "app"), chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": value})
}

func (h *Handler) importDotenv(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.vault.Import(h.who(r), chi.URLParam(r, "app"), body.Text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	id, _ := session(r)
	text, err := h.vault.Export(h.who(r), id, chi.URLParam(r, "app"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"dotenv": text})
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	id, _ := session(r)
	result, err := h.vault.Apply(h.who(r), id, chi.URLParam(r, "app"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.vault.Audit(limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// fail maps an error to its status and a machine-readable code. An error
// this package did not write is logged and answered with a fixed message:
// its text could name a path of the server.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var validation *ValidationError
	var throttled *ThrottledError
	var failed *FailureError
	switch {
	case errors.As(err, &validation):
		writeError(w, http.StatusBadRequest, "invalid", validation.Message)
	case errors.As(err, &throttled):
		w.Header().Set("Retry-After", strconv.Itoa(int((throttled.RetryAfter+time.Second-1)/time.Second)))
		writeError(w, http.StatusTooManyRequests, "throttled", throttled.Error())
	case errors.Is(err, ErrLocked):
		writeError(w, http.StatusLocked, "locked", err.Error())
	// Not 401: the interface treats it as an expired session.
	case errors.Is(err, ErrWrongPassphrase):
		writeError(w, http.StatusForbidden, "wrong_passphrase", err.Error())
	case errors.Is(err, ErrNotInitialized):
		writeError(w, http.StatusConflict, "not_initialized", err.Error())
	case errors.Is(err, ErrAlreadyInitialized):
		writeError(w, http.StatusConflict, "already_initialized", err.Error())
	case errors.Is(err, ErrAppExists):
		writeError(w, http.StatusConflict, "exists", err.Error())
	case errors.Is(err, ErrNoTarget):
		writeError(w, http.StatusConflict, "no_target", err.Error())
	case errors.Is(err, ErrAppNotFound), errors.Is(err, ErrVarNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrCorrupt):
		h.logger.WithField("route", chi.RouteContext(r.Context()).RoutePattern()).Error("secrets vault unreadable or inconsistent")
		writeError(w, http.StatusInternalServerError, "corrupt", err.Error())
	case errors.As(err, &failed):
		writeError(w, http.StatusInternalServerError, "failed", failed.Message)
	default:
		h.logger.WithField("route", chi.RouteContext(r.Context()).RoutePattern()).WithError(err).Error("secrets operation failed")
		writeError(w, http.StatusInternalServerError, "failed", "erreur interne du coffre")
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}
