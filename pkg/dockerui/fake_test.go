package dockerui

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Full IDs of the fake containers.
const (
	webID    = "1111111111111111111111111111111111111111111111111111111111111111"
	dbID     = "2222222222222222222222222222222222222222222222222222222222222222"
	workerID = "3333333333333333333333333333333333333333333333333333333333333333"
	toolID   = "4444444444444444444444444444444444444444444444444444444444444444"
)

// fakeDocker serves the subset of the Engine API used by the package.
type fakeDocker struct {
	t      *testing.T
	socket string
	server *httptest.Server

	mu         sync.Mutex
	containers map[string]map[string]any // list entries, by ID
	inspects   map[string]map[string]any
	logs       map[string][]byte
	logsType   map[string]string
	calls      []string // "METHOD path?query" of every request but GET
	inspectN   map[string]int
	statsQuery []string
	logsQuery  []string
	cpuTotal   uint64
	system     uint64
	// hangInspect makes inspect requests hang, as with a stuck containerd.
	hangInspect atomic.Bool
}

func newFakeDocker(t *testing.T) *fakeDocker {
	t.Helper()
	f := &fakeDocker{
		t:          t,
		socket:     filepath.Join(t.TempDir(), "docker.sock"),
		containers: map[string]map[string]any{},
		inspects:   map[string]map[string]any{},
		logs:       map[string][]byte{},
		logsType:   map[string]string{},
		inspectN:   map[string]int{},
		cpuTotal:   4_000_000_000,
		system:     100_000_000_000,
	}
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.server.Listener = listener
	f.server.Start()
	t.Cleanup(f.server.Close)

	f.add(webID, "/web", "nginx:1.27", "running", "Up 2 hours (healthy)", map[string]string{
		"com.docker.compose.project": "shop", "com.docker.compose.service": "web",
	}, []map[string]any{
		{"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
		{"IP": "::", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
		{"PrivatePort": 443, "Type": "tcp"},
	})
	f.add(dbID, "/shop-db-1", "postgres:16", "running", "Up 3 days", map[string]string{
		"com.docker.compose.project": "shop", "com.docker.compose.service": "db",
	}, nil)
	f.add(workerID, "/worker", "busybox", "exited", "Exited (3) 5 minutes ago", nil, nil)
	f.add(toolID, "/tool", "alpine", "created", "Created", nil, nil)
	f.mu.Lock()
	f.containers[webID]["Health"] = map[string]any{"Status": "healthy", "FailingStreak": 0}
	f.mu.Unlock()
	return f
}

func (f *fakeDocker) setLogs(id string, body []byte, contentType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[id], f.logsType[id] = body, contentType
}

func (f *fakeDocker) inspectCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspectN[id]
}

func (f *fakeDocker) statsQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statsQuery...)
}

func (f *fakeDocker) logQueries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logsQuery...)
}

func (f *fakeDocker) add(id, name, image, state, status string, labels map[string]string, ports []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ports == nil {
		ports = []map[string]any{}
	}
	f.containers[id] = map[string]any{
		"Id": id, "Names": []string{name}, "Image": image, "State": state, "Status": status,
		"Created": 1700000000, "Labels": labels, "Ports": ports,
	}
	f.inspects[id] = map[string]any{
		"Id": id, "Name": name, "RestartCount": 1, "Path": "/entrypoint",
		"Args": []any{"--log-level", "info"},
		"State": map[string]any{
			"Status": state, "ExitCode": 0, "StartedAt": "2026-11-17T08:00:00.5Z", "FinishedAt": "0001-01-01T00:00:00Z",
		},
		"Config": map[string]any{"Tty": false, "Env": []any{"PATH=/usr/bin", "API_TOKEN=s3cr3t-token"}},
	}
}

func (f *fakeDocker) setState(id, state, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[id]["State"] = state
	f.containers[id]["Status"] = status
	f.inspects[id]["State"].(map[string]any)["Status"] = state
}

func (f *fakeDocker) mutations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if f.hangInspect.Load() && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json") && path != "/containers/json" {
		<-r.Context().Done()
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodGet {
		call := r.Method + " " + path
		if r.URL.RawQuery != "" {
			call += "?" + r.URL.RawQuery
		}
		f.calls = append(f.calls, call)
	}
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container"})
	}
	if path == "/containers/json" && r.Method == http.MethodGet {
		if r.URL.Query().Get("all") != "1" {
			f.t.Errorf("listing without all=1: %s", r.URL.RawQuery)
		}
		list := make([]map[string]any, 0, len(f.containers))
		for _, c := range f.containers {
			list = append(list, c)
		}
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	if path == "/info" {
		_ = json.NewEncoder(w).Encode(map[string]any{"NCPU": 4, "MemTotal": 8 << 30, "ServerVersion": "29.2.1"})
		return
	}
	rest, ok := strings.CutPrefix(path, "/containers/")
	if !ok {
		notFound()
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	c, exists := f.containers[id]
	if !exists {
		notFound()
		return
	}
	state, _ := c["State"].(string)
	switch {
	case r.Method == http.MethodGet && action == "json":
		f.inspectN[id]++
		_ = json.NewEncoder(w).Encode(f.inspects[id])
	case r.Method == http.MethodGet && action == "stats":
		f.statsQuery = append(f.statsQuery, id[:4]+"?"+r.URL.RawQuery)
		f.cpuTotal += 1_000_000_000
		f.system += 10_000_000_000
		sample := map[string]any{
			"read":         time.Now().UTC().Format(time.RFC3339Nano),
			"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": f.cpuTotal}, "system_cpu_usage": f.system, "online_cpus": 4},
			"precpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 0}},
			"memory_stats": map[string]any{"usage": 300 << 20, "limit": 1 << 30, "stats": map[string]any{"inactive_file": 100 << 20}},
			"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": 1000, "tx_bytes": 2000}, "eth1": map[string]any{"rx_bytes": 1, "tx_bytes": 2}},
			"blkio_stats":  map[string]any{"io_service_bytes_recursive": []any{map[string]any{"op": "read", "value": 4096}, map[string]any{"op": "write", "value": 8192}}},
			"pids_stats":   map[string]any{"current": 7},
		}
		if r.URL.Query().Get("one-shot") != "true" {
			// A two-cycle read carries Docker's previous sample.
			sample["precpu_stats"] = map[string]any{"cpu_usage": map[string]any{"total_usage": f.cpuTotal - 500_000_000}, "system_cpu_usage": f.system - 10_000_000_000, "online_cpus": 4}
		}
		_ = json.NewEncoder(w).Encode(sample)
	case r.Method == http.MethodGet && action == "logs":
		f.logsQuery = append(f.logsQuery, r.URL.RawQuery)
		contentType := f.logsType[id]
		if contentType == "" {
			contentType = "application/vnd.docker.multiplexed-stream"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(f.logs[id])
	case r.Method == http.MethodPost && (action == "start" || action == "stop" || action == "restart"):
		running := state == "running"
		if (action == "start" && running) || (action == "stop" && !running) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if action == "stop" {
			c["State"], c["Status"] = "exited", "Exited (0) 1 second ago"
		} else {
			c["State"], c["Status"] = "running", "Up 1 second"
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && action == "":
		if state == "running" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "You cannot remove a running container"})
			return
		}
		delete(f.containers, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		notFound()
	}
}

// frame builds one multiplexed log frame.
func frame(stream byte, payload string) []byte {
	header := make([]byte, 8, 8+len(payload))
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

func newTestService(t *testing.T, f *fakeDocker, opener TerminalOpener) *Service {
	t.Helper()
	return New(Config{Enabled: true, Socket: f.socket, DockerCLI: "/usr/bin/docker"}, opener)
}
