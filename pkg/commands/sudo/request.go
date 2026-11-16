// Package sudocmd holds the agent-facing sudo commands: sudo-request, which
// asks a human to run a command with sudo, and sudo-hook, the Claude Code
// PreToolUse hook that points agents to it.
package sudocmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/LosFurina/tmuxatlas/pkg/commands/notify"
	"github.com/LosFurina/tmuxatlas/pkg/common"
	"github.com/LosFurina/tmuxatlas/pkg/socket"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// Exit codes, documented in docs/sudo-terminal.md.
const (
	exitDelivered = 0
	exitNotRun    = 1
	exitUsage     = 2
	exitPending   = 75 // EX_TEMPFAIL: still waiting, resume with --wait
)

type exitError struct {
	code    int
	message string
}

func (e exitError) Error() string { return e.message }

type socketClient struct {
	http *http.Client
}

func newSocketClient(path string) socketClient {
	return socketClient{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", path)
		},
	}}}
}

func (c socketClient) do(ctx context.Context, method, path string, body any) (sudo.AgentView, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return sudo.AgentView{}, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reader)
	if err != nil {
		return sudo.AgentView{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return sudo.AgentView{}, fmt.Errorf("socket TmuxAtlas injoignable (le service agent tourne-t-il ?) : %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return sudo.AgentView{}, err
	}
	if response.StatusCode == http.StatusNotFound && !bytes.Contains(data, []byte(`"code"`)) {
		return sudo.AgentView{}, sudo.ErrUnavailable
	}
	if response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Code == "" {
			return sudo.AgentView{}, fmt.Errorf("TmuxAtlas a répondu %d", response.StatusCode)
		}
		return sudo.AgentView{}, fmt.Errorf("%w (%s)", sudo.ErrorFromCode(failure.Code), failure.Error)
	}
	var view sudo.AgentView
	if err := json.Unmarshal(data, &view); err != nil {
		return sudo.AgentView{}, fmt.Errorf("réponse TmuxAtlas illisible : %w", err)
	}
	return view, nil
}

func detectOrigin() sudo.Origin {
	session, window, pane := notify.DetectTmuxContext()
	origin := sudo.Origin{Session: session, Window: window, Pane: pane, PID: os.Getppid()}
	if cwd, err := os.Getwd(); err == nil {
		origin.Cwd = cwd
	}
	if current, err := user.Current(); err == nil {
		origin.UnixUser = current.Username
	}
	for _, name := range []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
		if value := os.Getenv(name); value != "" && len(value) <= 128 {
			origin.ClaudeSessionID = value
			break
		}
	}
	return origin
}

func printView(view sudo.AgentView) {
	data, _ := json.MarshalIndent(view, "", "  ")
	fmt.Println(string(data))
}

// finish turns a view into the documented exit code.
func finish(view sudo.AgentView) error {
	printView(view)
	switch {
	case view.Status == sudo.StatusDelivered:
		return nil
	case view.Status.Final():
		return exitError{code: exitNotRun, message: "demande sudo terminée sans exécution livrée : " + string(view.Status)}
	default:
		return exitError{code: exitPending, message: fmt.Sprintf(
			"toujours en attente d'un administrateur ; relance : tmuxatlas sudo-request --wait %s", view.ID)}
	}
}

func wait(ctx context.Context, client socketClient, id string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(context.Background(), timeout+30*time.Second)
	defer cancel()
	type result struct {
		view sudo.AgentView
		err  error
	}
	done := make(chan result, 1)
	go func() {
		view, err := client.do(waitCtx, http.MethodGet,
			"/api/sudo/requests/"+url.PathEscape(id)+"/wait?timeout="+url.QueryEscape(timeout.String()), nil)
		done <- result{view, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return exitError{code: exitUsage, message: r.err.Error()}
		}
		return finish(r.view)
	case <-ctx.Done():
		// Interrupted: give up the request if it has not started.
		cancelCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		view, err := client.do(cancelCtx, http.MethodPost, "/api/sudo/requests/"+url.PathEscape(id)+"/cancel", nil)
		if err == nil {
			printView(view)
		}
		return exitError{code: 130, message: "interrompu ; demande annulée si elle n'avait pas démarré"}
	}
}

func run(ctx context.Context, c *cli.Command) error {
	socketPath := c.String("socket")
	if socketPath == "" {
		socketPath = socket.DefaultPath()
	}
	client := newSocketClient(socketPath)
	timeout := c.Duration("timeout")
	if timeout <= 0 || timeout > 9*time.Minute {
		return exitError{code: exitUsage, message: "--timeout doit être entre 1s et 9m (le Bash tool coupe à 10 min)"}
	}
	if id := c.String("cancel"); id != "" {
		cancelCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		view, err := client.do(cancelCtx, http.MethodPost, "/api/sudo/requests/"+url.PathEscape(id)+"/cancel", nil)
		if err != nil {
			return exitError{code: exitUsage, message: err.Error()}
		}
		printView(view)
		return nil
	}
	if id := c.String("wait"); id != "" {
		return wait(ctx, client, id, timeout)
	}
	args := c.Args().Slice()
	if len(args) == 0 {
		return exitError{code: exitUsage, message: `usage : tmuxatlas sudo-request [--reason "…"] -- "<commande>"`}
	}
	command := strings.Join(args, " ")
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "note : plusieurs arguments joints par des espaces ; passe la commande en une seule chaîne pour garder les guillemets.")
	}
	command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), "sudo "))
	if err := sudo.ValidateCommand(command); err != nil {
		return exitError{code: exitUsage, message: "commande refusée : une seule ligne, sans caractères de contrôle, 4 Kio au plus"}
	}
	createCtx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	view, err := client.do(createCtx, http.MethodPost, "/api/sudo/requests", map[string]any{
		"command": command, "reason": c.String("reason"), "origin": detectOrigin(),
	})
	if err != nil {
		if errors.Is(err, sudo.ErrUnavailable) {
			return exitError{code: exitUsage, message: "terminal sudo indisponible sur ce TmuxAtlas (hub sans authentification ou version ancienne)"}
		}
		return exitError{code: exitUsage, message: err.Error()}
	}
	fmt.Fprintf(os.Stderr, "Demande sudo %s envoyée : un administrateur doit l'approuver et taper le mot de passe dans TmuxAtlas.\n", view.ID)
	return wait(ctx, client, view.ID, timeout)
}

func init() {
	common.RegisterCommand(&cli.Command{
		Name:      "sudo-request",
		Usage:     "ask a human to run a command with sudo in TmuxAtlas",
		ArgsUsage: `-- "<command>"`,
		Description: `For agents without a TTY. The request opens a Sudo panel in the agent's
TmuxAtlas session: an administrator may edit the command, approves it, types
the password in an ephemeral terminal and reviews the output before it is
sent back. The password never reaches the agent.

Exit codes: 0 delivered (the command's own code is in the JSON), 1 not run
(refused, withheld, expired, cancelled, failed), 75 still pending (resume with
--wait <id>), 2 usage or transport error.

Example:
  tmuxatlas sudo-request --reason "install jq" -- "apt-get install -y jq"`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "reason", Usage: "why the command is needed, shown to the administrator"},
			&cli.DurationFlag{Name: "timeout", Value: 8 * time.Minute, Usage: "how long to wait before exiting with 75"},
			&cli.StringFlag{Name: "wait", Usage: "resume waiting for an existing request id"},
			&cli.StringFlag{Name: "cancel", Usage: "cancel a request that has not started"},
			&cli.StringFlag{Name: "socket", Usage: "TmuxAtlas Unix socket", Sources: cli.EnvVars("TMUXATLAS_SOCKET")},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			err := run(ctx, c)
			var exit exitError
			if errors.As(err, &exit) {
				fmt.Fprintln(os.Stderr, exit.message)
				os.Exit(exit.code)
			}
			return err
		},
	})
}
