package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// dockerSessionOption marks a container shell session with its container ID,
// so a session is only reused for the container it was opened for.
const dockerSessionOption = "@atlas-docker"

// The Containers view is admin-only: dockerui.Register wraps every route in
// dockerui.AdminOnly and none of them is in accountUserRoutes.
func init() {
	for _, route := range dockerui.Routes {
		method, path, _ := strings.Cut(route, " ")
		accountOtherRoutes[method+" /api"+path] = "admin"
	}
}

// newDockerUI builds the Containers view (TMUXATLAS_DOCKER, TMUXATLAS_DOCKER_SOCKET).
func newDockerUI(opts *Options, router *peer.ActionRouter) *dockerui.Service {
	terminals := &dockerTerminals{opts: opts}
	if router != nil {
		terminals.router = router
	}
	return dockerui.New(dockerui.ConfigFromEnv(), terminals)
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
	client := t.opts.Client
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
		if !client.HasSession(session) {
			if err := t.create(r, host, session, marker, argv); err != nil {
				return dockerui.TerminalResponse{}, err
			}
			return dockerui.TerminalResponse{HostID: host, Session: session, Created: true}, nil
		}
		// Another session with this name (an older container of the same
		// name, or one made by hand) is never typed into: try the next name.
		// Option commands take a pane target: "=name:" (with the colon) is
		// the session; "=name" alone would not be.
		current, err := client.Exec("show-options", "-qv", "-t", tmux.ExactWindow(session, ""), dockerSessionOption)
		if err != nil || strings.TrimSpace(current) != marker {
			continue
		}
		dead, err := client.Exec("display-message", "-p", "-t", tmux.ExactWindow(session, ""), "#{pane_dead}")
		if err == nil && strings.TrimSpace(dead) == "1" {
			if err := respawnDockerPane(client, session, argv); err != nil {
				return dockerui.TerminalResponse{}, err
			}
		}
		return dockerui.TerminalResponse{HostID: host, Session: session}, nil
	}
	return dockerui.TerminalResponse{}, errors.New("trop de sessions portent déjà le nom de ce conteneur")
}

func (t *dockerTerminals) create(r *http.Request, host, session, marker string, argv []string) error {
	params := map[string]string{}
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		params["owner"] = identity.User.ID
		params["owner_name"] = identity.User.Username
	}
	payload, _ := json.Marshal(params)
	if _, err := t.router.Execute(r.Context(), "new", peer.SessionTarget{HostID: host, Session: session}, payload); err != nil {
		return err
	}
	client := t.opts.Client
	if _, err := client.Exec("set-option", "-q", "-t", tmux.ExactWindow(session, ""), dockerSessionOption, marker); err != nil {
		_ = client.KillSession(session)
		return err
	}
	// A shell that fails to start (no sh in the image, container gone) leaves
	// its message on screen instead of closing the session at once. tmux
	// before 3.3 has no "failed": the session then closes on any exit.
	_, _ = client.Exec("set-option", "-w", "-q", "-t", tmux.ExactWindow(session, ""), "remain-on-exit", "failed")
	if err := respawnDockerPane(client, session, argv); err != nil {
		_ = client.KillSession(session)
		return err
	}
	refreshLocalSessionState(t.opts, host)
	return nil
}

// respawnDockerPane replaces the pane's process by argv. tmux runs a command
// of several arguments with execvp, without a shell, so no argument is ever
// parsed by a shell on this host.
func respawnDockerPane(client *tmux.Client, session string, argv []string) error {
	args := append([]string{"respawn-pane", "-k", "-t", tmux.ExactWindow(session, ""), "--"}, argv...)
	_, err := client.Exec(args...)
	return err
}
