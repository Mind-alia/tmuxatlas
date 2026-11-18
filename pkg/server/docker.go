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

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// dockerSessionOption marks a container shell session with its container ID,
// so a session is only reused for the container it was opened for. It also
// reserves the session to administrators (see accountSessions).
const dockerSessionOption = tmux.DockerSessionOption

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
func newDockerUI(opts *Options, router *peer.ActionRouter, policy *accountPolicy) *dockerui.Service {
	cfg := dockerui.ConfigFromEnv()
	if !opts.AuthEnabled {
		cfg.Actor = "sans authentification"
		if cfg.Enabled && !cfg.Explicit {
			cfg.Problem = "vue Docker désactivée : ce serveur tourne sans authentification (TMUXATLAS_DOCKER=on pour l’activer quand même)"
		}
	}
	if opts.AccountsAuth != nil && !cfg.Explicit {
		cfg.Gate = func() string { return sharedDockerAccountProblem(opts.AccountsAuth.Store) }
	}
	terminals := &dockerTerminals{opts: opts}
	if router != nil {
		terminals.router = router
	}
	if opts.AccountsAuth != nil && policy != nil {
		terminals.closeTerminals = policy.closeReservedTerminals
	}
	return dockerui.New(cfg, terminals)
}

// sharedDockerAccountProblem refuses the Containers view while an account
// other than an admin can sign in. Every account's shells run under the
// hub's Unix account, the one that reaches the Docker socket: such an
// account can run docker from its own session, so reserving the view to
// admins would promise a separation the system does not have. The operator
// who accepts that asks for the view with TMUXATLAS_DOCKER=on. A store that
// cannot be read refuses too.
func sharedDockerAccountProblem(store *accounts.Store) string {
	if store == nil {
		return ""
	}
	users, err := store.List()
	if err != nil {
		return "vue Docker désactivée : la liste des comptes est illisible"
	}
	for _, user := range users {
		if user.Role != "admin" && !user.Disabled {
			return "vue Docker désactivée : un compte non administrateur est actif, et ses terminaux tournent sous le compte Unix du hub, qui a accès à Docker (TMUXATLAS_DOCKER=on pour l’activer quand même)"
		}
	}
	return ""
}

// accountSessions keeps, from a listing of the local tmux server, the
// sessions that an account other than an admin may see and use: all but the
// container shells, whose docker exec is usually root in the container. A
// name listed twice is left out if either entry is a container shell.
func accountSessions(sessions []*tmux.Session) []*tmux.Session {
	reserved := make(map[string]bool)
	for _, session := range sessions {
		if session != nil && session.AdminOnly {
			reserved[session.Name] = true
		}
	}
	if len(reserved) == 0 {
		return sessions
	}
	kept := make([]*tmux.Session, 0, len(sessions))
	for _, session := range sessions {
		if session != nil && !reserved[session.Name] {
			kept = append(kept, session)
		}
	}
	return kept
}

// closeReservedTerminals ends the terminals that accounts other than admins
// hold on a local session they may no longer use, and waits for them to
// close. A container shell starts only after it, once its session carries
// the marker: a terminal opened in the instant between the creation of the
// session and its marker never sees the shell, and owns refuses new ones.
func (p *accountPolicy) closeReservedTerminals() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	local := func(connection accountConnection) bool {
		return connection.session != "" && p.opts.PeerMgr != nil && p.opts.PeerMgr.IsLocal(connection.host)
	}
	open := false
	for _, connection := range p.connections {
		open = open || local(connection)
	}
	if !open {
		return true
	}
	if p.opts.Client == nil {
		return false
	}
	current, err := p.opts.Client.ListSessions()
	if err != nil {
		return false
	}
	usable := make(map[string]bool)
	for _, session := range accountSessions(current) {
		usable[session.Name] = true
	}
	// A terminal stays followed until it has closed: one that outlives the
	// wait refuses the next attempt too, instead of being forgotten.
	closing := make(map[uint64]accountConnection)
	for id, connection := range p.connections {
		if local(connection) && !usable[connection.session] {
			connection.cancel()
			closing[id] = connection
		}
	}
	timer := time.NewTimer(reservedTerminalsGrace)
	defer timer.Stop()
	for id, connection := range closing {
		select {
		case <-connection.done:
			delete(p.connections, id)
		case <-timer.C:
			return false
		}
	}
	return true
}

// reservedTerminalsGrace is how long closeReservedTerminals waits for the
// terminals it cancelled.
var reservedTerminalsGrace = 5 * time.Second

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
	// closeTerminals, in accounts mode, runs before docker starts in a pane
	// (accountPolicy.closeReservedTerminals); false stops the shell.
	closeTerminals func() bool
	mu             sync.Mutex
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
			if err := t.startShell(tmuxPath, pane, false, argv); err != nil {
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
			if err := t.startShell(tmuxPath, pane, false, argv); err != nil {
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
	if err := t.startShell(tmuxPath, pane, true, argv); err != nil {
		return fail(err)
	}
	refreshLocalSessionState(t.opts, host)
	return nil
}

// startShell replaces the pane's process by the container shell, once the
// pane's session carries its marker: first the terminals of other accounts
// that may no longer use the session are closed.
func (t *dockerTerminals) startShell(tmuxPath, pane string, kill bool, argv []string) error {
	if t.closeTerminals != nil && !t.closeTerminals() {
		return errors.New("des terminaux d’autres comptes restent ouverts sur cette session : réessaie")
	}
	return respawnDockerPane(tmuxPath, pane, kill, argv)
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
