package dockerui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const execTestID = "abcdefabcdef0123456789abcdefabcdef0123456789abcdefabcdef01234567"

// execDocker is a fake Engine API that only knows exec: create, a start
// that takes over the connection as Docker does, and the exit code.
type execDocker struct {
	mu       sync.Mutex
	created  map[string]any
	stdin    []byte
	exitCode int
	// hang keeps the stream open until the client goes away.
	hang bool
}

func startExecDocker(t *testing.T, fake *execDocker) *Service {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
			fmt.Fprintf(w, `[{"Id":%q,"Names":["/shop-db-1"],"Image":"postgres:16","State":"running"},{"Id":%q,"Names":["/old"],"Image":"mysql:8","State":"exited"}]`, dbID, webID)
		case r.Method == http.MethodGet && r.URL.Path == "/containers/"+dbID+"/json":
			fmt.Fprint(w, `{"Id":"x","Config":{"Env":["POSTGRES_USER=shop","POSTGRES_PASSWORD=invented"]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/"+dbID+"/exec":
			fake.mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&fake.created)
			fake.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"Id":%q}`, execTestID)
		case r.Method == http.MethodPost && r.URL.Path == "/exec/"+execTestID+"/start":
			if r.Header.Get("Upgrade") != "tcp" {
				http.Error(w, `{"message":"no upgrade"}`, http.StatusBadRequest)
				return
			}
			if options, _ := io.ReadAll(r.Body); string(options) != `{"Detach":false,"Tty":false}` {
				http.Error(w, `{"message":"bad start options"}`, http.StatusBadRequest)
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			fake.mu.Lock()
			attach, _ := fake.created["AttachStdin"].(bool)
			hang := fake.hang
			fake.mu.Unlock()
			if hang {
				_, _ = rw.Write(frame(1, "partial"))
				_ = rw.Flush()
				_, _ = io.Copy(io.Discard, rw)
				return
			}
			if attach {
				data, _ := io.ReadAll(rw)
				fake.mu.Lock()
				fake.stdin = data
				fake.mu.Unlock()
			}
			_, _ = rw.Write(frame(1, "first "))
			_, _ = rw.Write(frame(2, "warning\n"))
			_, _ = rw.Write(frame(1, "second"))
			_ = rw.Flush()
		case r.Method == http.MethodGet && r.URL.Path == "/exec/"+execTestID+"/json":
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, fake.exitCode)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"No such container"}`)
		}
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return New(Config{Enabled: true, Socket: socket}, nil)
}

func TestExecStreamsOutputAndReturnsTheExitCode(t *testing.T) {
	fake := &execDocker{exitCode: 3}
	service := startExecDocker(t, fake)
	var stdout, stderr bytes.Buffer
	code, err := service.Exec(context.Background(), dbID, ExecSpec{
		Cmd: []string{"pg_dumpall", "--username=shop; rm -rf /"}, Env: []string{"PGPASSWORD=invented"},
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil || code != 3 {
		t.Fatalf("exec: %d, %v", code, err)
	}
	if stdout.String() != "first second" || stderr.String() != "warning\n" {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	// The command is an argument vector, never a shell line, and no TTY.
	cmd, _ := fake.created["Cmd"].([]any)
	if len(cmd) != 2 || cmd[0] != "pg_dumpall" || cmd[1] != "--username=shop; rm -rf /" {
		t.Errorf("Cmd sent: %v", fake.created["Cmd"])
	}
	if fake.created["Tty"] != false || fake.created["AttachStdin"] != false || fake.created["AttachStdout"] != true {
		t.Errorf("exec options: %v", fake.created)
	}
	if env, _ := fake.created["Env"].([]any); len(env) != 1 || env[0] != "PGPASSWORD=invented" {
		t.Errorf("Env sent has %d entries", len(env))
	}
}

func TestExecFeedsStandardInput(t *testing.T) {
	fake := &execDocker{}
	service := startExecDocker(t, fake)
	input := strings.Repeat("INSERT INTO orders VALUES (1);\n", 5000)
	var stdout bytes.Buffer
	code, err := service.Exec(context.Background(), dbID, ExecSpec{Cmd: []string{"psql"}, Stdin: strings.NewReader(input), Stdout: &stdout})
	if err != nil || code != 0 {
		t.Fatalf("exec: %d, %v", code, err)
	}
	if string(fake.stdin) != input {
		t.Errorf("the process read %d bytes of %d", len(fake.stdin), len(input))
	}
	if stdout.String() != "first second" {
		t.Errorf("stdout %q", stdout.String())
	}
	// A source that fails is reported, not taken for the end of the dump.
	broken := io.MultiReader(strings.NewReader("BEGIN;\n"), errReader{})
	if _, err := service.Exec(context.Background(), dbID, ExecSpec{Cmd: []string{"psql"}, Stdin: broken}); err == nil || !strings.Contains(err.Error(), "lecture de l’entrée") {
		t.Errorf("broken input: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("corrupt gzip") }

func TestExecIsBoundedAndGuarded(t *testing.T) {
	fake := &execDocker{hang: true}
	service := startExecDocker(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := service.Exec(ctx, dbID, ExecSpec{Cmd: []string{"pg_dumpall"}}); !errors.Is(err, errTimeout) {
		t.Errorf("exec past its deadline: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Error("the deadline did not end the exec")
	}
	for name, call := range map[string]func() error{
		"short ID": func() error {
			_, err := service.Exec(context.Background(), dbID[:12], ExecSpec{Cmd: []string{"true"}})
			return err
		},
		"no command": func() error { _, err := service.Exec(context.Background(), dbID, ExecSpec{}); return err },
		"NUL in an argument": func() error {
			_, err := service.Exec(context.Background(), dbID, ExecSpec{Cmd: []string{"a\x00b"}})
			return err
		},
		"unknown container": func() error {
			_, err := service.Exec(context.Background(), workerID, ExecSpec{Cmd: []string{"true"}})
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// TMUXATLAS_DOCKER=off and the gate close these entries like the routes.
	for _, cfg := range []Config{
		{Enabled: false, Socket: service.cfg.Socket},
		{Enabled: true, Socket: service.cfg.Socket, Gate: func() string { return "un compte non administrateur est actif" }},
	} {
		closed := New(cfg, nil)
		if closed.Problem() == "" {
			t.Error("Problem is empty for a closed view")
		}
		if _, err := closed.Exec(context.Background(), dbID, ExecSpec{Cmd: []string{"true"}}); err == nil {
			t.Error("exec with the view closed")
		}
		if _, err := closed.RunningContainers(context.Background()); err == nil {
			t.Error("listing with the view closed")
		}
		if _, err := closed.ContainerEnv(context.Background(), dbID); err == nil {
			t.Error("environment with the view closed")
		}
	}
}

func TestRunningContainersAndEnv(t *testing.T) {
	service := startExecDocker(t, &execDocker{})
	if service.Problem() != "" {
		t.Fatalf("problem: %s", service.Problem())
	}
	running, err := service.RunningContainers(context.Background())
	if err != nil || len(running) != 1 || running[0] != (RunningContainer{ID: dbID, Name: "shop-db-1", Image: "postgres:16"}) {
		t.Fatalf("running: %+v, %v", running, err)
	}
	env, err := service.ContainerEnv(context.Background(), dbID)
	if err != nil || len(env) != 2 || env[0] != "POSTGRES_USER=shop" {
		t.Fatalf("env of %d entries, %v", len(env), err)
	}
}
