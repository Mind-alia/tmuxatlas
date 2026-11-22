package deploy

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The real runner is only ever tried with harmless stand-ins for git: no
// test starts git nor docker.
func TestExecRunnerPassesArgumentsWithoutAShell(t *testing.T) {
	echo, err := exec.LookPath("echo")
	if err != nil {
		t.Skip("echo introuvable")
	}
	runner := &ExecRunner{Git: echo}
	var out bytes.Buffer
	args := []string{"a b", "$(id)", ";", "`id`", "--flag=x y"}
	if err := runner.Run(context.Background(), Command{Tool: ToolGit, Args: args, Dir: t.TempDir()}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != strings.Join(args, " ") {
		t.Fatalf("arguments interprétés : %q", got)
	}
	// A tool without a path, or an unknown one, runs nothing.
	for _, tool := range []string{ToolDocker, "sh", ""} {
		if err := runner.Run(context.Background(), Command{Tool: tool, Args: []string{"x"}}, &out); err == nil {
			t.Errorf("outil %q accepté", tool)
		}
	}
	if (&ExecRunner{Git: echo}).Missing() == "" || (&ExecRunner{Docker: echo}).Missing() == "" || (&ExecRunner{Git: echo, Docker: echo}).Missing() != "" {
		t.Fatal("Missing")
	}
}

func TestExecRunnerStopsOnCancellation(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep introuvable")
	}
	runner := &ExecRunner{Git: sleep}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = runner.Run(ctx, Command{Tool: ToolGit, Args: []string{"30"}}, &bytes.Buffer{})
	if err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("commande non arrêtée : %v après %s", err, time.Since(started))
	}
}
