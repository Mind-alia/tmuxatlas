// Package dockerui serves the live Containers view: list, stats, logs,
// inspect, start/stop/restart/delete and a shell in a container, by talking to
// the Docker Engine API over its unix socket. Every route is admin-only.
package dockerui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// DefaultSocket is used when TMUXATLAS_DOCKER_SOCKET is unset.
const DefaultSocket = "/var/run/docker.sock"

// Timeouts of the Engine API calls.
const (
	listTimeout    = 5 * time.Second
	inspectTimeout = 5 * time.Second
	infoTimeout    = 5 * time.Second
	startTimeout   = 30 * time.Second
	removeTimeout  = 30 * time.Second
	logsTimeout    = 10 * time.Second
	// A stop waits for the container's grace period, then kills it.
	stopMargin     = 30 * time.Second
	defaultStopSec = 10
	maxStopSec     = 300
)

const (
	inspectTTL     = 15 * time.Second
	inspectWorkers = 6
	infoTTL        = time.Minute
)

// shellProbe runs inside the container: bash when it exists, sh otherwise.
// It is a constant: nothing from a request or from Docker is ever added.
const shellProbe = `command -v bash >/dev/null 2>&1 && exec bash || exec sh`

// sessionPrefix starts the name of every container shell session.
const sessionPrefix = "docker-"

// Config is read from the environment by ConfigFromEnv.
type Config struct {
	// Enabled is false with TMUXATLAS_DOCKER=off.
	Enabled bool
	// Socket is the Engine API socket (TMUXATLAS_DOCKER_SOCKET).
	Socket string
	// DockerCLI is the absolute path of the docker client used for shells.
	DockerCLI string
	// Problem explains a configuration that cannot work.
	Problem string
}

// Enabled reads TMUXATLAS_DOCKER: the view is on unless explicitly disabled.
func Enabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "0", "no":
		return false
	default:
		return true
	}
}

// safePath is the shape required of the socket and docker CLI paths: both
// become arguments of a tmux command, where for instance a trailing ";"
// would end the command.
var safePath = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]+$`)

// ConfigFromEnv reads TMUXATLAS_DOCKER and TMUXATLAS_DOCKER_SOCKET.
func ConfigFromEnv() Config {
	cfg := Config{Enabled: Enabled(os.Getenv("TMUXATLAS_DOCKER")), Socket: DefaultSocket}
	if socket := strings.TrimSpace(os.Getenv("TMUXATLAS_DOCKER_SOCKET")); socket != "" {
		socket = filepath.Clean(strings.TrimPrefix(socket, "unix://"))
		if !filepath.IsAbs(socket) || !safePath.MatchString(socket) {
			cfg.Problem = "TMUXATLAS_DOCKER_SOCKET doit être un chemin absolu vers le socket Docker (lettres, chiffres, . _ - / @ +)"
		}
		cfg.Socket = socket
	}
	if path, err := exec.LookPath("docker"); err == nil && filepath.IsAbs(path) && safePath.MatchString(path) {
		cfg.DockerCLI = path
	}
	return cfg
}

// TerminalOpener opens, or reuses, the tmux session whose only process is
// argv. marker identifies the container, so a session is only reused for the
// container it was opened for.
type TerminalOpener interface {
	OpenTerminal(r *http.Request, name, marker string, argv []string) (TerminalResponse, error)
}

// ErrTerminalUnavailable is returned by an opener without local tmux.
var ErrTerminalUnavailable = errors.New("terminal indisponible : pas de tmux local sur ce serveur")

// Service holds the Engine API client and the caches shared by requests.
type Service struct {
	cfg    Config
	client *apiClient
	opener TerminalOpener
	logger *logrus.Entry
	now    func() time.Time

	infoMu sync.Mutex
	info   *HostInfo
	infoAt time.Time

	inspectMu sync.Mutex
	inspects  map[string]inspectEntry

	stats statsCache
}

type inspectEntry struct {
	state   string
	fetched time.Time
	info    apiInspect
}

// New returns the service. opener may be nil: shells are then unavailable.
func New(cfg Config, opener TerminalOpener) *Service {
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket
	}
	return &Service{
		cfg:      cfg,
		client:   newAPIClient(cfg.Socket),
		opener:   opener,
		logger:   logrus.WithField("component", "dockerui"),
		now:      time.Now,
		inspects: make(map[string]inspectEntry),
	}
}

func (s *Service) usable() error {
	if !s.cfg.Enabled {
		return errDisabled
	}
	if s.cfg.Problem != "" {
		return &requestError{status: http.StatusServiceUnavailable, message: s.cfg.Problem}
	}
	return nil
}

var errDisabled = errors.New("vue Docker désactivée sur ce serveur (TMUXATLAS_DOCKER=off)")

func (s *Service) terminalSupported() bool {
	return s.opener != nil && s.cfg.DockerCLI != ""
}

// List returns every container with the host summary and the latest stats.
func (s *Service) List(ctx context.Context) ListResponse {
	response := ListResponse{Containers: []Container{}, Terminal: s.terminalSupported(), CollectedAt: s.now().UnixMilli()}
	if err := s.usable(); err != nil {
		response.Error = err.Error()
		return response
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	raw, err := s.client.listContainers(listCtx)
	cancel()
	if err != nil {
		response.Error = err.Error()
		return response
	}
	response.Available = true
	host := s.hostInfo(ctx)
	response.Host = host

	details := s.inspectAll(ctx, raw)
	running := make([]string, 0, len(raw))
	for _, c := range raw {
		if c.State == "running" {
			running = append(running, c.ID)
		}
	}
	fallbackCPUs := 0
	if host != nil {
		fallbackCPUs = host.NCPU
	}
	stats := s.containerStats(ctx, running, fallbackCPUs)

	response.Summary.Total = len(raw)
	for _, c := range raw {
		view := containerView(c, details[c.ID])
		if sample, ok := stats[c.ID]; ok && c.State == "running" {
			view.Stats = &sample
			if sample.CPUPercent != nil {
				response.Summary.CPUPercent += *sample.CPUPercent
			} else {
				response.Summary.StatsPending = true
			}
			response.Summary.MemoryUsage += sample.MemoryUsage
		} else if c.State == "running" {
			response.Summary.StatsPending = true
		}
		if c.State == "running" {
			response.Summary.Running++
		}
		response.Containers = append(response.Containers, view)
	}
	sort.SliceStable(response.Containers, func(i, j int) bool {
		return response.Containers[i].Name < response.Containers[j].Name
	})
	return response
}

func (s *Service) hostInfo(ctx context.Context) *HostInfo {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	if s.info != nil && s.now().Sub(s.infoAt) < infoTTL {
		return s.info
	}
	infoCtx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	raw, err := s.client.info(infoCtx)
	if err != nil {
		return s.info // the last known value, or nil
	}
	s.info = &HostInfo{NCPU: raw.NCPU, MemTotal: raw.MemTotal, Version: raw.ServerVersion}
	s.infoAt = s.now()
	return s.info
}

// inspectAll returns the cached inspect summaries, refreshing those whose
// state changed or that are older than inspectTTL.
func (s *Service) inspectAll(ctx context.Context, list []apiContainer) map[string]apiInspect {
	s.inspectMu.Lock()
	out := make(map[string]apiInspect, len(list))
	var stale []apiContainer
	seen := make(map[string]bool, len(list))
	for _, c := range list {
		seen[c.ID] = true
		entry, ok := s.inspects[c.ID]
		if ok {
			out[c.ID] = entry.info
		}
		if !ok || entry.state != c.State || s.now().Sub(entry.fetched) >= inspectTTL {
			stale = append(stale, c)
		}
	}
	for id := range s.inspects {
		if !seen[id] {
			delete(s.inspects, id)
		}
	}
	s.inspectMu.Unlock()
	if len(stale) == 0 {
		return out
	}

	type result struct {
		c    apiContainer
		info apiInspect
	}
	jobs := make(chan apiContainer)
	results := make(chan result, len(stale))
	var workers sync.WaitGroup
	for range min(inspectWorkers, len(stale)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for c := range jobs {
				info, err := s.inspectSummary(ctx, c.ID)
				if err == nil {
					results <- result{c: c, info: info}
				}
			}
		}()
	}
	for _, c := range stale {
		jobs <- c
	}
	close(jobs)
	workers.Wait()
	close(results)

	s.inspectMu.Lock()
	defer s.inspectMu.Unlock()
	for r := range results {
		s.inspects[r.c.ID] = inspectEntry{state: r.c.State, fetched: s.now(), info: r.info}
		out[r.c.ID] = r.info
	}
	return out
}

func (s *Service) inspectSummary(ctx context.Context, id string) (apiInspect, error) {
	callCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	raw, err := s.client.inspect(callCtx, id)
	if err != nil {
		return apiInspect{}, err
	}
	var info apiInspect
	if err := json.Unmarshal(raw, &info); err != nil {
		return apiInspect{}, err
	}
	return info, nil
}

// forget drops what the caches know about a container after an action.
func (s *Service) forget(id string) {
	s.inspectMu.Lock()
	delete(s.inspects, id)
	s.inspectMu.Unlock()
	s.stats.forget(id)
}

var exitedPattern = regexp.MustCompile(`^Exited \((-?\d+)\)`)

func containerView(c apiContainer, info apiInspect) Container {
	view := Container{
		ID:           c.ID,
		ShortID:      shortID(c.ID),
		Name:         containerName(c.Names, c.ID),
		Image:        c.Image,
		State:        c.State,
		Status:       c.Status,
		Health:       health(c, info),
		Project:      c.Labels["com.docker.compose.project"],
		Service:      c.Labels["com.docker.compose.service"],
		Ports:        ports(c.Ports),
		Created:      c.Created,
		StartedAt:    dockerTime(info.State.StartedAt),
		FinishedAt:   dockerTime(info.State.FinishedAt),
		RestartCount: info.RestartCount,
		OOMKilled:    info.State.OOMKilled,
	}
	if c.State == "exited" || c.State == "dead" {
		// The status text is fresh on every listing; the cached inspect may
		// describe the previous exit.
		code, known := info.State.ExitCode, info.State.Status != ""
		if match := exitedPattern.FindStringSubmatch(c.Status); match != nil {
			code, _ = strconv.Atoi(match[1])
			known = true
		}
		if known {
			view.ExitCode = &code
		}
	}
	return view
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// containerName picks the container's own name: link aliases have a second
// slash ("/web/db").
func containerName(names []string, id string) string {
	for _, name := range names {
		trimmed := strings.TrimPrefix(name, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			return trimmed
		}
	}
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	return shortID(id)
}

func health(c apiContainer, info apiInspect) string {
	value := ""
	if c.Health != nil {
		value = c.Health.Status
	} else if strings.Contains(c.Status, "(healthy)") {
		value = "healthy"
	} else if strings.Contains(c.Status, "(unhealthy)") {
		value = "unhealthy"
	} else if strings.Contains(c.Status, "(health: starting)") {
		value = "starting"
	} else if info.State.Health != nil {
		value = info.State.Health.Status
	}
	switch value {
	case "healthy", "unhealthy", "starting":
		return value
	default:
		return ""
	}
}

// dockerTime drops Docker's zero time ("0001-01-01T00:00:00Z": never).
func dockerTime(value string) string {
	if value == "" || strings.HasPrefix(value, "0001-") {
		return ""
	}
	return value
}

// ports merges the IPv4 and IPv6 bindings of one port.
func ports(list []apiPort) []Port {
	type key struct {
		private, public int
		kind            string
	}
	index := make(map[key]int)
	out := make([]Port, 0, len(list))
	for _, p := range list {
		k := key{p.PrivatePort, p.PublicPort, p.Type}
		if i, ok := index[k]; ok {
			if strings.Contains(out[i].IP, ":") && !strings.Contains(p.IP, ":") {
				out[i].IP = p.IP
			}
			continue
		}
		index[k] = len(out)
		out = append(out, Port{IP: p.IP, Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Public == 0) != (out[j].Public == 0) {
			return out[i].Public != 0
		}
		if out[i].Public != out[j].Public {
			return out[i].Public < out[j].Public
		}
		if out[i].Private != out[j].Private {
			return out[i].Private < out[j].Private
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Errors of the per-container routes, mapped to HTTP statuses.
var (
	errInvalidID   = errors.New("identifiant de conteneur invalide")
	errNotFound    = errors.New("conteneur introuvable")
	errAmbiguousID = errors.New("identifiant de conteneur ambigu")
)

type conflictError struct{ message string }

func (e *conflictError) Error() string { return e.message }

var idPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// resolve validates an ID from a URL and finds its container in a fresh
// listing. Only the ID returned by Docker is used afterwards.
func (s *Service) resolve(ctx context.Context, id string) (apiContainer, error) {
	if !idPattern.MatchString(id) {
		return apiContainer{}, errInvalidID
	}
	if err := s.usable(); err != nil {
		return apiContainer{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := s.client.listContainers(listCtx)
	if err != nil {
		return apiContainer{}, err
	}
	var found *apiContainer
	for i := range list {
		if strings.HasPrefix(list[i].ID, id) {
			if found != nil {
				return apiContainer{}, errAmbiguousID
			}
			found = &list[i]
		}
	}
	if found == nil || !idPattern.MatchString(found.ID) {
		return apiContainer{}, errNotFound
	}
	return *found, nil
}

// Action starts, stops or restarts a container.
func (s *Service) Action(ctx context.Context, action, id string) (ActionResponse, error) {
	switch action {
	case "start", "stop", "restart":
	default:
		return ActionResponse{}, errBadRequest("action inconnue")
	}
	c, err := s.resolve(ctx, id)
	if err != nil {
		return ActionResponse{}, err
	}
	timeout := startTimeout
	if action == "stop" || action == "restart" {
		timeout = s.stopTimeout(ctx, c.ID) + stopMargin
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	changed, err := s.client.post(callCtx, "/containers/"+c.ID+"/"+action)
	s.forget(c.ID)
	return ActionResponse{ID: c.ID, Name: containerName(c.Names, c.ID), Action: action, Changed: changed}, err
}

// stopTimeout is the grace period Docker waits before killing the container.
func (s *Service) stopTimeout(ctx context.Context, id string) time.Duration {
	seconds := defaultStopSec
	if info, err := s.inspectSummary(ctx, id); err == nil && info.Config.StopTimeout != nil {
		seconds = *info.Config.StopTimeout
	}
	if seconds < 0 || seconds > maxStopSec {
		seconds = maxStopSec
	}
	return time.Duration(seconds) * time.Second
}

// Remove deletes a stopped container, without force and keeping its volumes.
func (s *Service) Remove(ctx context.Context, id string) (ActionResponse, error) {
	c, err := s.resolve(ctx, id)
	if err != nil {
		return ActionResponse{}, err
	}
	result := ActionResponse{ID: c.ID, Name: containerName(c.Names, c.ID), Action: "delete"}
	switch c.State {
	case "running", "paused", "restarting", "removing":
		return result, &conflictError{message: "le conteneur doit être arrêté avant d’être supprimé"}
	}
	callCtx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	err = s.client.remove(callCtx, c.ID)
	s.forget(c.ID)
	if err != nil {
		return result, err
	}
	result.Changed = true
	return result, nil
}

// Logs returns the last lines (tail, at most MaxLogLines), or the lines
// written since a previous poll.
func (s *Service) Logs(ctx context.Context, id, tail, since string) (LogsResponse, error) {
	lines := DefaultLogTail
	if tail != "" {
		parsed, err := strconv.Atoi(tail)
		if err != nil || parsed < 0 {
			return LogsResponse{}, errBadRequest("tail doit être un entier positif")
		}
		lines = min(parsed, MaxLogLines)
	}
	if since != "" && !sincePattern.MatchString(since) {
		return LogsResponse{}, errBadRequest("since doit être un horodatage unix")
	}
	c, err := s.resolve(ctx, id)
	if err != nil {
		return LogsResponse{}, err
	}
	query := url.Values{
		"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"},
		"tail": {strconv.Itoa(lines)},
	}
	if since != "" {
		query.Set("since", since)
	}
	callCtx, cancel := context.WithTimeout(ctx, logsTimeout)
	defer cancel()
	resp, err := s.client.logs(callCtx, c.ID, query)
	if err != nil {
		return LogsResponse{}, err
	}
	defer resp.Body.Close()
	multiplexed, err := s.isMultiplexed(ctx, c.ID, resp.Header.Get("Content-Type"))
	if err != nil {
		return LogsResponse{}, err
	}
	parsed, truncated, err := parseLogStream(resp.Body, multiplexed)
	if err != nil && len(parsed) == 0 {
		return LogsResponse{}, err
	}
	if since != "" && lines > 0 && len(parsed) >= lines {
		truncated = true
	}
	return LogsResponse{Lines: parsed, NextSince: nextSince(parsed, since), Truncated: truncated}, nil
}

// isMultiplexed tells how to read a logs body: Docker names the format in
// Content-Type since API 1.42; older engines only tell through Config.Tty.
func (s *Service) isMultiplexed(ctx context.Context, id, contentType string) (bool, error) {
	switch {
	case strings.HasPrefix(contentType, "application/vnd.docker.multiplexed-stream"):
		return true, nil
	case strings.HasPrefix(contentType, "application/vnd.docker.raw-stream"):
		return false, nil
	}
	info, err := s.inspectSummary(ctx, id)
	if err != nil {
		return false, err
	}
	return !info.Config.Tty, nil
}

// Inspect returns the inspect document with its secrets masked.
func (s *Service) Inspect(ctx context.Context, id string) (json.RawMessage, error) {
	c, err := s.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	raw, err := s.client.inspect(callCtx, c.ID)
	if err != nil {
		return nil, err
	}
	return sanitizeInspect(raw)
}

// Terminal opens a shell in a running container, in a tmux session.
func (s *Service) Terminal(r *http.Request, id string) (TerminalResponse, apiContainer, error) {
	if !idPattern.MatchString(id) {
		return TerminalResponse{}, apiContainer{}, errInvalidID
	}
	if !s.terminalSupported() {
		if s.cfg.DockerCLI == "" {
			return TerminalResponse{}, apiContainer{}, errUnsupported("terminal indisponible : la commande docker est introuvable sur ce serveur")
		}
		return TerminalResponse{}, apiContainer{}, errUnsupported(ErrTerminalUnavailable.Error())
	}
	c, err := s.resolve(r.Context(), id)
	if err != nil {
		return TerminalResponse{}, apiContainer{}, err
	}
	if c.State != "running" {
		return TerminalResponse{}, c, &conflictError{message: "le conteneur doit être en marche pour ouvrir un terminal"}
	}
	argv := []string{s.cfg.DockerCLI, "--host", "unix://" + s.cfg.Socket, "exec", "-it", c.ID, "sh", "-c", shellProbe}
	session, err := s.opener.OpenTerminal(r, SessionName(containerName(c.Names, c.ID)), c.ID, argv)
	if err != nil {
		if errors.Is(err, ErrTerminalUnavailable) {
			return TerminalResponse{}, c, errUnsupported(err.Error())
		}
		return TerminalResponse{}, c, err
	}
	return session, c, nil
}

// SessionName derives the tmux session name from a container name: only
// letters, digits, "_" and "-" survive, so the name is always a valid and
// exact tmux target. Suffixes such as "-2" still fit in 64 characters.
func SessionName(container string) string {
	var b strings.Builder
	dash := false
	for _, r := range container {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "conteneur"
	}
	if limit := 64 - len(sessionPrefix) - 3; len(name) > limit {
		name = strings.TrimRight(name[:limit], "-")
	}
	return sessionPrefix + name
}

type requestError struct {
	status  int
	message string
}

func (e *requestError) Error() string { return e.message }

func errBadRequest(message string) error {
	return &requestError{status: http.StatusBadRequest, message: message}
}
func errUnsupported(message string) error {
	return &requestError{status: http.StatusNotImplemented, message: message}
}
