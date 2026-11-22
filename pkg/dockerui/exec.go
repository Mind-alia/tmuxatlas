package dockerui

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Exec, RunningContainers and ContainerEnv serve callers inside the hub,
// such as the database backups: they pass the same switches as the routes
// (TMUXATLAS_DOCKER, the shared-account gate) and are never mounted on a
// route themselves.

const (
	execCreateTimeout  = 15 * time.Second
	execInspectTimeout = 10 * time.Second
	execResponseLimit  = 64 << 10
	maxExecArgs        = 64
	maxExecEnv         = 16
)

var (
	fullIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	execIDPattern = regexp.MustCompile(`^[0-9a-f]{12,128}$`)
)

// RunningContainer names one running container.
type RunningContainer struct {
	ID    string
	Name  string
	Image string
}

// ExecSpec is one command run inside a container, without a shell: Cmd is
// the argument vector. Env adds variables to the process ("NAME=value");
// they are sent in the request body and never logged. Stdin may be nil.
type ExecSpec struct {
	Cmd    []string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Problem says why the Containers view is closed, or nothing when it is open.
func (s *Service) Problem() string {
	if err := s.usable(); err != nil {
		_, message := errorStatus(err)
		return message
	}
	return ""
}

// RunningContainers lists the running containers.
func (s *Service) RunningContainers(ctx context.Context) ([]RunningContainer, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := s.client.listContainers(listCtx)
	if err != nil {
		return nil, err
	}
	out := make([]RunningContainer, 0, len(list))
	for _, c := range list {
		if c.State != "running" || !fullIDPattern.MatchString(c.ID) {
			continue
		}
		out = append(out, RunningContainer{ID: c.ID, Name: containerName(c.Names, c.ID), Image: c.Image})
	}
	return out, nil
}

// ContainerEnv returns the environment of a container ("NAME=value"). The
// values are secrets: the caller keeps them in memory and never shows them.
func (s *Service) ContainerEnv(ctx context.Context, id string) ([]string, error) {
	if !fullIDPattern.MatchString(id) {
		return nil, errInvalidID
	}
	if err := s.usable(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	raw, err := s.client.inspect(callCtx, id)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Config struct {
			Env []string `json:"Env"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, errors.New("réponse Docker illisible")
	}
	return doc.Config.Env, nil
}

// Exec runs spec in the container and returns the exit code of its process.
// The output is streamed to spec.Stdout and spec.Stderr as it comes; ctx
// bounds the whole run, and ending it closes the streams of the process.
func (s *Service) Exec(ctx context.Context, id string, spec ExecSpec) (int, error) {
	if !fullIDPattern.MatchString(id) {
		return -1, errInvalidID
	}
	if len(spec.Cmd) == 0 || len(spec.Cmd) > maxExecArgs || len(spec.Env) > maxExecEnv {
		return -1, errBadRequest("commande invalide")
	}
	for _, value := range append(append([]string{}, spec.Cmd...), spec.Env...) {
		if strings.ContainsRune(value, 0) {
			return -1, errBadRequest("commande invalide")
		}
	}
	if err := s.usable(); err != nil {
		return -1, err
	}
	createCtx, cancel := context.WithTimeout(ctx, execCreateTimeout)
	execID, err := s.client.execCreate(createCtx, id, spec)
	cancel()
	if err != nil {
		return -1, err
	}
	if err := s.client.execStart(ctx, execID, spec); err != nil {
		return -1, err
	}
	// The process has closed its streams; its exit code follows at once.
	inspectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execInspectTimeout)
	defer cancel()
	for {
		var state struct {
			Running  bool `json:"Running"`
			ExitCode *int `json:"ExitCode"`
		}
		if err := s.client.getJSON(inspectCtx, "/exec/"+execID+"/json", nil, execResponseLimit, &state); err != nil {
			return -1, err
		}
		if !state.Running && state.ExitCode != nil {
			return *state.ExitCode, nil
		}
		select {
		case <-inspectCtx.Done():
			return -1, errTimeout
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (c *apiClient) execCreate(ctx context.Context, id string, spec ExecSpec) (string, error) {
	body, err := json.Marshal(map[string]any{
		"AttachStdin": spec.Stdin != nil, "AttachStdout": true, "AttachStderr": true,
		"Tty": false, "Cmd": spec.Cmd, "Env": spec.Env,
	})
	if err != nil {
		return "", err
	}
	resp, err := c.doBody(ctx, http.MethodPost, "/containers/"+id+"/exec", nil, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", apiErrorFrom(resp)
	}
	raw, err := readLimited(resp.Body, execResponseLimit)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || !execIDPattern.MatchString(created.ID) {
		return "", errors.New("réponse Docker illisible")
	}
	return created.ID, nil
}

// execStart starts the exec and carries its streams until the process closes
// them. The Engine API hands over the raw connection for that, which
// net/http's client does not expose with a writable side: the request is
// written on a connection of its own.
func (c *apiClient) execStart(ctx context.Context, execID string, spec ExecSpec) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx)
		}
		return &unavailableError{cause: err}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	fail := func(err error) error {
		if ctx.Err() != nil {
			return contextError(ctx)
		}
		return err
	}

	const payload = `{"Detach":false,"Tty":false}`
	request := "POST /exec/" + execID + "/start HTTP/1.1\r\n" +
		"Host: docker\r\nUser-Agent: tmuxatlas-dockerui\r\nContent-Type: application/json\r\n" +
		"Connection: Upgrade\r\nUpgrade: tcp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload)) + payload
	if _, err := io.WriteString(conn, request); err != nil {
		return fail(&unavailableError{cause: err})
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		return fail(&unavailableError{cause: err})
	}
	var stream io.Reader = reader
	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
	case http.StatusOK:
		stream = resp.Body
	default:
		defer resp.Body.Close()
		return apiErrorFrom(resp)
	}

	stdinDone := make(chan error, 1)
	if spec.Stdin != nil {
		go func() { stdinDone <- copyStdin(conn, spec.Stdin) }()
	} else {
		stdinDone <- nil
	}
	err = demux(stream, spec.Stdout, spec.Stderr)
	// The process is done reading, or the output failed: stop feeding it.
	_ = conn.Close()
	stdinErr := <-stdinDone
	if ctx.Err() != nil {
		return contextError(ctx)
	}
	if stdinErr != nil {
		return stdinErr
	}
	return err
}

func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errTimeout
	}
	return ctx.Err()
}

// copyStdin feeds the process and then closes its input. Only a failure to
// read the source is reported: a process that exits early simply stops
// reading, and its exit code tells the rest.
func copyStdin(conn net.Conn, source io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
				return nil
			}
		}
		if err == io.EOF {
			if half, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = half.CloseWrite()
			}
			return nil
		}
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("lecture de l’entrée : %w", err)
		}
	}
}

// demux splits Docker's multiplexed stream: frames of an 8-byte header
// (stream, 0, 0, 0, size as a big-endian uint32) followed by size bytes.
func demux(r io.Reader, stdout, stderr io.Writer) error {
	var header [8]byte
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("flux Docker interrompu : %w", err)
		}
		target := stdout
		if header[0] == 2 {
			target = stderr
		}
		if target == nil {
			target = io.Discard
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if _, err := io.CopyN(target, r, size); err != nil {
			return fmt.Errorf("flux Docker interrompu : %w", err)
		}
	}
}
