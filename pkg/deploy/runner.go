package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Tools a Command may name. Nothing else is ever run.
const (
	ToolGit    = "git"
	ToolDocker = "docker"
)

// Command is one external command: a tool, its arguments as an array, and
// the directory it runs in. No shell ever parses it.
type Command struct {
	Tool string
	Args []string
	Dir  string
}

// Runner runs the external commands of a deployment. Output receives stdout
// and stderr as they come. The command stops when ctx ends.
type Runner interface {
	Run(ctx context.Context, cmd Command, output io.Writer) error
}

// ExecRunner runs the real git and docker clients.
type ExecRunner struct {
	// Git and Docker are absolute paths; an empty one refuses the tool.
	Git    string
	Docker string
	// Env is the environment of every command.
	Env []string
}

// NewExecRunner looks git and docker up in PATH. socket, when it is not
// Docker's default, is given to the docker client so that it talks to the
// same daemon as the Containers view.
func NewExecRunner(docker, socket, defaultSocket string) *ExecRunner {
	runner := &ExecRunner{Docker: docker}
	if path, err := exec.LookPath("git"); err == nil && filepath.IsAbs(path) {
		runner.Git = path
	}
	// Git never asks for a password on a terminal nobody sees.
	runner.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if socket != "" && socket != defaultSocket {
		runner.Env = append(runner.Env, "DOCKER_HOST=unix://"+socket)
	}
	return runner
}

// Missing says which tool the runner cannot run, or "".
func (r *ExecRunner) Missing() string {
	switch {
	case r.Git == "":
		return "git introuvable sur ce serveur"
	case r.Docker == "":
		return "client docker introuvable sur ce serveur"
	}
	return ""
}

// killGrace is how long a cancelled command has to end after SIGTERM.
const killGrace = 10 * time.Second

// Run starts the command in its own process group, so that a cancellation
// reaches the children too (docker starts its compose plugin as a child).
func (r *ExecRunner) Run(ctx context.Context, command Command, output io.Writer) error {
	path := ""
	switch command.Tool {
	case ToolGit:
		path = r.Git
	case ToolDocker:
		path = r.Docker
	}
	if path == "" {
		return errors.New("outil indisponible : " + command.Tool)
	}
	cmd := exec.CommandContext(ctx, path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = r.Env
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = killGrace
	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// Whatever outlived SIGTERM and the grace period.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return err
}
