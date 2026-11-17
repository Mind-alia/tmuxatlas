package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// dockerSessionOption marks a container shell session with its container ID,
// so a session is only reused for the container it was opened for.
const dockerSessionOption = "@atlas-docker"

// dockerTmuxTimeout bounds each tmux command run for a container shell.
const dockerTmuxTimeout = 5 * time.Second

var tmuxPaneID = regexp.MustCompile(`^%[0-9]+$`)

// The Containers view is admin-only: dockerRouters requires an admin and
// none of its routes is in accountUserRoutes.
func init() {
	for _, route := range dockerui.Routes {
		method, path, _ := strings.Cut(route, " ")
		accountOtherRoutes[method+" /api"+path] = "admin"
	}
}

// newDockerUI builds the Containers view (TMUXATLAS_DOCKER, TMUXATLAS_DOCKER_SOCKET).
// Without authentication, any local account could drive Docker through it,
// so it then needs TMUXATLAS_DOCKER=on.
func newDockerUI(opts *Options, router *peer.ActionRouter) *dockerui.Service {
	cfg := dockerui.ConfigFromEnv()
	if !opts.AuthEnabled {
		cfg.Actor = "sans authentification"
		if cfg.Enabled && !cfg.Explicit {
			cfg.Problem = "vue Docker désactivée : ce serveur tourne sans authentification (TMUXATLAS_DOCKER=on pour l’activer quand même)"
		}
	}
	terminals := &dockerTerminals{opts: opts}
	if router != nil {
		terminals.router = router
	}
	return dockerui.New(cfg, terminals)
}

// dockerRouters gives the Containers view the authentication of the other
// routes and the Origin and CSRF checks of the other changes, but not the
// account policy: that policy holds one lock through every request, and a
// stop lasts the container's grace period. Admins only, in accounts mode.
func dockerRouters(r chi.Router, opts *Options, origin func(http.Handler) http.Handler) (read, mutate chi.Router) {
	switch {
	case opts.AccountsAuth != nil:
		read = r.With(opts.AccountsAuth.Middleware, accountAdminOnly)
		return read, read.With(origin, auth.CSRFMiddleware(opts.SessionMgr))
	case opts.AuthEnabled:
		read = r.With(auth.Middleware(opts.SessionMgr, opts.SecureCookies))
		return read, read.With(origin, auth.CSRFMiddleware(opts.SessionMgr))
	default:
		// Like the file explorer: without authentication, changes still
		// need the same Origin.
		return r, r.With(origin)
	}
}

// dockerTerminals opens container shells as local tmux sessions. The session
// is created by the same "new" runtime action as the "Nouvelle session"
// dialog (POST /api/session/new), so it gets the same validation and owner;
// then its shell is replaced by argv, run without a shell on this host.
type dockerTerminals struct {
	opts   *Options
	router runtimeActionExecutor
	mu     sync.Mutex
}

func (t *dockerTerminals) OpenTerminal(r *http.Request, name, marker string, argv []string) (dockerui.TerminalResponse, error) {
	if t.opts == nil || t.opts.Client == nil || t.opts.PeerMgr == nil || t.router == nil {
		return dockerui.TerminalResponse{}, dockerui.ErrTerminalUnavailable
	}
	if len(argv) < 2 {
		return dockerui.TerminalResponse{}, errors.New("commande de terminal invalide")
	}
	tmuxPath := t.opts.Client.TmuxPath()
	host := t.opts.PeerMgr.LocalID()
	t.mu.Lock()
	defer t.mu.Unlock()
	for attempt := 1; attempt <= 9; attempt++ {
		session := name
		if attempt > 1 {
			session = fmt.Sprintf("%s-%d", name, attempt)
		}
		if err := tmux.ValidateSessionName(session); err != nil {
			return dockerui.TerminalResponse{}, err
		}
		if _, err := runTmux(tmuxPath, "has-session", "-t", tmux.ExactSession(session)); err != nil {
			if err := t.create(r, tmuxPath, host, session, marker, argv); err != nil {
				return dockerui.TerminalResponse{}, err
			}
			return dockerui.TerminalResponse{HostID: host, Session: session, Created: true}, nil
		}
		// Option and pane commands take a pane target: "=name:" (with the
		// colon) is the session; "=name" alone would not be.
		target := tmux.ExactWindow(session, "")
		current, err := runTmux(tmuxPath, "show-options", "-qv", "-t", target, dockerSessionOption)
		if err != nil {
			continue
		}
		current = strings.TrimSpace(current)
		pane, dead := paneState(tmuxPath, target)
		switch {
		case current == marker && pane != "" && dead:
			// The shell of this container ended with an error: start it again.
			if err := respawnDockerPane(tmuxPath, pane, false, argv); err != nil {
				return dockerui.TerminalResponse{}, err
			}
			return dockerui.TerminalResponse{HostID: host, Session: session}, nil
		case current == marker:
			return dockerui.TerminalResponse{HostID: host, Session: session}, nil
		case current != "" && pane != "" && dead:
			// The dead shell of another container of this name, such as one
			// Compose recreated: the name is free again.
			if _, err := runTmux(tmuxPath, "set-option", "-q", "-t", pane, dockerSessionOption, marker); err != nil {
				return dockerui.TerminalResponse{}, err
			}
			if err := respawnDockerPane(tmuxPath, pane, false, argv); err != nil {
				return dockerui.TerminalResponse{}, err
			}
			return dockerui.TerminalResponse{HostID: host, Session: session, Created: true}, nil
		}
		// A live session of another container, or one opened by hand, is
		// never typed into: try the next name.
	}
	return dockerui.TerminalResponse{}, errors.New("trop de sessions portent déjà le nom de ce conteneur")
}

func (t *dockerTerminals) create(r *http.Request, tmuxPath, host, session, marker string, argv []string) error {
	params := map[string]string{}
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		params["owner"] = identity.User.ID
		params["owner_name"] = identity.User.Username
	}
	payload, _ := json.Marshal(params)
	if _, err := t.router.Execute(r.Context(), "new", peer.SessionTarget{HostID: host, Session: session}, payload); err != nil {
		return err
	}
	// From here on the pane is named by its ID, which tmux never reuses: a
	// session killed and recreated meanwhile cannot be hit by mistake.
	pane, _ := paneState(tmuxPath, tmux.ExactWindow(session, ""))
	if pane == "" {
		return errors.New("session tmux introuvable après sa création")
	}
	fail := func(err error) error {
		_, _ = runTmux(tmuxPath, "kill-pane", "-t", pane)
		return err
	}
	if _, err := runTmux(tmuxPath, "set-option", "-q", "-t", pane, dockerSessionOption, marker); err != nil {
		return fail(err)
	}
	// A shell that fails to start (no sh in the image, container gone) leaves
	// its message on screen instead of closing the session at once. tmux
	// before 3.3 has no "failed": the session then closes on any exit.
	_, _ = runTmux(tmuxPath, "set-option", "-w", "-q", "-t", pane, "remain-on-exit", "failed")
	if err := respawnDockerPane(tmuxPath, pane, true, argv); err != nil {
		return fail(err)
	}
	refreshLocalSessionState(t.opts, host)
	return nil
}

// paneState returns the ID of the target's active pane and whether its
// process has exited.
func paneState(tmuxPath, target string) (string, bool) {
	out, err := runTmux(tmuxPath, "display-message", "-p", "-t", target, "#{pane_id} #{pane_dead}")
	if err != nil {
		return "", false
	}
	pane, dead, _ := strings.Cut(strings.TrimSpace(out), " ")
	if !tmuxPaneID.MatchString(pane) {
		return "", false
	}
	return pane, dead == "1"
}

// respawnDockerPane replaces the pane's process by argv. tmux runs a command
// of several arguments with execvp, without a shell, so no argument is ever
// parsed by a shell on this host. Without kill, tmux refuses a live pane.
func respawnDockerPane(tmuxPath, pane string, kill bool, argv []string) error {
	args := []string{"respawn-pane", "-t", pane}
	if kill {
		args = append(args, "-k")
	}
	args = append(append(args, "--"), argv...)
	_, err := runTmux(tmuxPath, args...)
	return err
}

// runTmux runs one tmux command with a deadline.
func runTmux(tmuxPath string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTmuxTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tmuxPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
