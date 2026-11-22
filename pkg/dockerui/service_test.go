package dockerui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

func byName(list []Container) map[string]Container {
	out := make(map[string]Container, len(list))
	for _, c := range list {
		out[c.Name] = c
	}
	return out
}

func TestListMapsContainers(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	got := s.List(context.Background())
	if !got.Available || got.Error != "" {
		t.Fatalf("available=%v error=%q", got.Available, got.Error)
	}
	if got.Host == nil || got.Host.NCPU != 4 || got.Host.MemTotal != 8<<30 || got.Host.Version != "29.2.1" {
		t.Fatalf("host: %+v", got.Host)
	}
	if len(got.Containers) != 4 || got.Containers[0].Name != "shop-db-1" || got.Containers[3].Name != "worker" {
		t.Fatalf("containers are not sorted by name: %+v", got.Containers)
	}
	all := byName(got.Containers)

	web := all["web"]
	if web.ID != webID || web.ShortID != webID[:12] || web.Image != "nginx:1.27" || web.State != "running" ||
		web.Status != "Up 2 hours (healthy)" || web.Health != "healthy" {
		t.Fatalf("web: %+v", web)
	}
	if web.Project != "shop" || web.Service != "web" || all["shop-db-1"].Project != "shop" || all["shop-db-1"].Service != "db" {
		t.Fatalf("compose grouping fields: %+v / %+v", web, all["shop-db-1"])
	}
	if all["worker"].Project != "" || all["tool"].Project != "" {
		t.Fatal("standalone containers must not get a project")
	}
	wantPorts := []Port{{IP: "0.0.0.0", Private: 80, Public: 8080, Type: "tcp"}, {Private: 443, Type: "tcp"}}
	if len(web.Ports) != 2 || web.Ports[0] != wantPorts[0] || web.Ports[1] != wantPorts[1] {
		t.Fatalf("ports (IPv4 and IPv6 merged, published first): %+v", web.Ports)
	}
	if web.StartedAt != "2026-11-17T08:00:00.5Z" || web.FinishedAt != "" || web.RestartCount != 1 || web.Created != 1700000000 {
		t.Fatalf("inspect fields: %+v", web)
	}
	if web.Stats == nil || web.Stats.MemoryUsage != 200<<20 || web.Stats.MemoryLimit != 1<<30 ||
		web.Stats.NetRx != 1001 || web.Stats.NetTx != 2002 || web.Stats.BlockRead != 4096 ||
		web.Stats.BlockWrite != 8192 || web.Stats.PIDs != 7 {
		t.Fatalf("web stats: %+v", web.Stats)
	}
	// A first one-shot sample has nothing to compare with yet.
	if web.Stats.CPUPercent != nil {
		t.Fatalf("cpu without a previous sample: %v", *web.Stats.CPUPercent)
	}
	worker := all["worker"]
	if worker.Stats != nil || worker.ExitCode == nil || *worker.ExitCode != 3 {
		t.Fatalf("worker (exited, code from the status text): %+v", worker)
	}
	if all["tool"].ExitCode != nil || all["tool"].Stats != nil {
		t.Fatalf("created container: %+v", all["tool"])
	}
	if got.Summary.Total != 4 || got.Summary.Running != 2 || got.Summary.MemoryUsage != 400<<20 ||
		!got.Summary.StatsPending || got.Summary.CPUPercent != 0 {
		t.Fatalf("summary: %+v", got.Summary)
	}
}

func TestListCachesInspectAndReusesSamples(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	now := time.Now()
	s.now = func() time.Time { return now }

	s.List(context.Background())
	s.List(context.Background())
	if f.inspectCount(webID) != 1 || len(f.statsQueries()) != 2 {
		t.Fatalf("second listing within the TTLs: inspect=%d stats=%v", f.inspectCount(webID), f.statsQueries())
	}

	now = now.Add(statsTTL + time.Second)
	list := s.List(context.Background())
	got := byName(list.Containers)
	queries := f.statsQueries()
	if len(queries) != 4 {
		t.Fatalf("refresh after the TTL: %v", queries)
	}
	for _, query := range queries {
		if !strings.Contains(query, "one-shot=true") || !strings.Contains(query, "stream=false") {
			t.Fatalf("every sample must be a one-shot read: %s", query)
		}
	}
	// 1 s of CPU between this hub's two samples, over 10 s of host time × 4 CPUs.
	if cpu := got["web"].Stats.CPUPercent; cpu == nil || *cpu < 39.9 || *cpu > 40.1 {
		t.Fatalf("cpu from the kept sample: %v", cpu)
	}
	if list.Summary.StatsPending || list.Summary.CPUPercent < 79.9 || list.Summary.CPUPercent > 80.1 {
		t.Fatalf("summary once CPU is known: %+v", list.Summary)
	}

	f.setState(webID, "exited", "Exited (0) 1 second ago")
	s.List(context.Background())
	if f.inspectCount(webID) != 2 || f.inspectCount(dbID) != 1 {
		t.Fatalf("a state change must refresh inspect: %d %d", f.inspectCount(webID), f.inspectCount(dbID))
	}
	now = now.Add(inspectTTL)
	s.List(context.Background())
	if f.inspectCount(dbID) != 2 {
		t.Fatalf("inspect older than its TTL: %d", f.inspectCount(dbID))
	}
}

func TestListStaysQuickWhenInspectHangs(t *testing.T) {
	f := newFakeDocker(t)
	f.hangInspect.Store(true)
	s := newTestService(t, f, nil)
	start := time.Now()
	got := s.List(context.Background())
	if elapsed := time.Since(start); elapsed > inspectBatch+2*time.Second {
		t.Fatalf("a hung inspect held the listing %s", elapsed)
	}
	if !got.Available || len(got.Containers) != 4 || byName(got.Containers)["web"].StartedAt != "" {
		t.Fatalf("listing without inspect: %+v", got)
	}
	start = time.Now()
	if got := s.List(context.Background()); !got.Available {
		t.Fatalf("second listing: %+v", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("failed inspects must wait before a retry, the listing took %s", elapsed)
	}
}

func TestUnavailableAndDisabled(t *testing.T) {
	missing := New(Config{Enabled: true, Socket: filepath.Join(t.TempDir(), "absent.sock")}, nil)
	got := missing.List(context.Background())
	if got.Available || !strings.Contains(got.Error, "Docker injoignable") || got.Containers == nil {
		t.Fatalf("unreachable socket: %+v", got)
	}
	disabled := New(Config{Enabled: false}, nil)
	if got := disabled.List(context.Background()); got.Available || !strings.Contains(got.Error, "TMUXATLAS_DOCKER=off") {
		t.Fatalf("disabled: %+v", got)
	}
	for _, s := range []*Service{missing, disabled} {
		router := chi.NewRouter()
		s.Register(router, router)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/docker/containers/"+webID+"/stop", nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("action without Docker: %d %s", response.Code, response.Body)
		}
	}
	if Enabled("off") || Enabled(" FALSE ") || !Enabled("") || !Enabled("on") {
		t.Fatal("TMUXATLAS_DOCKER parsing")
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("TMUXATLAS_DOCKER", "")
	t.Setenv("TMUXATLAS_DOCKER_SOCKET", "unix:///run/user/1000/docker.sock")
	if cfg := ConfigFromEnv(); !cfg.Enabled || cfg.Explicit || cfg.Socket != "/run/user/1000/docker.sock" || cfg.Problem != "" || cfg.Actor != "passkey" {
		t.Fatalf("socket override: %+v", cfg)
	}
	t.Setenv("TMUXATLAS_DOCKER", "on")
	if cfg := ConfigFromEnv(); !cfg.Enabled || !cfg.Explicit {
		t.Fatalf("TMUXATLAS_DOCKER=on: %+v", cfg)
	}
	for _, socket := range []string{"docker.sock", "/run/docker.sock;kill-server", "/run/my docker.sock", "/run/\"x"} {
		t.Setenv("TMUXATLAS_DOCKER_SOCKET", socket)
		if cfg := ConfigFromEnv(); cfg.Problem == "" {
			t.Fatalf("socket %q must be refused", socket)
		}
	}
	t.Setenv("TMUXATLAS_DOCKER", "off")
	if ConfigFromEnv().Enabled {
		t.Fatal("TMUXATLAS_DOCKER=off")
	}
}

// serve runs one request through the routes, as the account in ctx if any.
func serve(s *Service, method, target string, identity *auth.AccountIdentity) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	s.Register(router, router)
	request := httptest.NewRequest(method, target, nil)
	if identity != nil {
		request = request.WithContext(auth.WithAccount(request.Context(), *identity))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestActionRoutes(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	for _, test := range []struct {
		action, id string
		changed    bool
		call       string
	}{
		{"stop", webID[:12], true, "POST /containers/" + webID + "/stop"},
		{"stop", webID, false, "POST /containers/" + webID + "/stop"},
		{"start", webID[:20], true, "POST /containers/" + webID + "/start"},
		{"restart", workerID[:12], true, "POST /containers/" + workerID + "/restart"},
	} {
		before := len(f.mutations())
		response := serve(s, http.MethodPost, "/docker/containers/"+test.id+"/"+test.action, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", test.action, response.Code, response.Body)
		}
		var body ActionResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Changed != test.changed || body.Action != test.action {
			t.Fatalf("%s: %s", test.action, response.Body)
		}
		calls := f.mutations()
		if len(calls) != before+1 || calls[before] != test.call {
			t.Fatalf("%s: Docker received %v, want %s with the full ID", test.action, calls[before:], test.call)
		}
	}
}

func TestInvalidIDsAreRejected(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	for _, id := range []string{
		"web", "11111111111", strings.Repeat("1", 65), "ABCDEFABCDEF", "111111111111;rm", "%2e%2e%2finfo",
		"..", "111111111111%20", "1111111111111111111111111111111111111111111111111111111111111111x",
	} {
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/docker/containers/" + id + "/logs"},
			{http.MethodGet, "/docker/containers/" + id + "/inspect"},
			{http.MethodPost, "/docker/containers/" + id + "/stop"},
			{http.MethodPost, "/docker/containers/" + id + "/terminal"},
			{http.MethodDelete, "/docker/containers/" + id},
		} {
			response := serve(s, route.method, route.path, nil)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
				t.Fatalf("%s %s: %d %s", route.method, route.path, response.Code, response.Body)
			}
		}
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("an invalid ID reached Docker: %v", calls)
	}
	if response := serve(s, http.MethodPost, "/docker/containers/abcdefabcdef/stop", nil); response.Code != http.StatusNotFound {
		t.Fatalf("unknown container: %d", response.Code)
	}
	f.add("1111111111110000000000000000000000000000000000000000000000000000", "/twin", "x", "running", "Up", nil, nil)
	if response := serve(s, http.MethodPost, "/docker/containers/111111111111/stop", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous prefix: %d", response.Code)
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("Docker received %v", calls)
	}
}

func TestDeleteIsRefusedWhileRunning(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	response := serve(s, http.MethodDelete, "/docker/containers/"+webID, nil)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "arrêté") {
		t.Fatalf("running: %d %s", response.Code, response.Body)
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("delete of a running container reached Docker: %v", calls)
	}
	response = serve(s, http.MethodDelete, "/docker/containers/"+workerID[:12], nil)
	if response.Code != http.StatusOK {
		t.Fatalf("exited: %d %s", response.Code, response.Body)
	}
	if calls := f.mutations(); len(calls) != 1 || calls[0] != "DELETE /containers/"+workerID {
		t.Fatalf("delete must never pass force nor v: %v", calls)
	}
	if response := serve(s, http.MethodDelete, "/docker/containers/"+workerID, nil); response.Code != http.StatusNotFound {
		t.Fatalf("deleted twice: %d", response.Code)
	}
}

func TestRoutesAreAdminOnly(t *testing.T) {
	f := newFakeDocker(t)
	opener := &fakeOpener{}
	s := newTestService(t, f, opener)
	user := &auth.AccountIdentity{User: accounts.User{ID: "u1", Username: "alice", Role: "user"}}
	admin := &auth.AccountIdentity{User: accounts.User{ID: "a1", Username: "root", Role: "admin"}}
	routes := []struct{ method, path string }{
		{http.MethodGet, "/docker/containers"},
		{http.MethodGet, "/docker/containers/" + webID + "/logs"},
		{http.MethodGet, "/docker/containers/" + webID + "/inspect"},
		{http.MethodPost, "/docker/containers/" + webID + "/start"},
		{http.MethodPost, "/docker/containers/" + webID + "/stop"},
		{http.MethodPost, "/docker/containers/" + webID + "/restart"},
		{http.MethodPost, "/docker/containers/" + webID + "/terminal"},
		{http.MethodDelete, "/docker/containers/" + workerID},
		{http.MethodPost, "/docker/containers"},
		{http.MethodGet, "/docker/images"},
		{http.MethodPost, "/docker/images/pull"},
		{http.MethodPost, "/docker/images/prune"},
		{http.MethodDelete, "/docker/images/" + strings.TrimPrefix(oldImage, "sha256:")},
		{http.MethodGet, "/docker/volumes"},
		{http.MethodPost, "/docker/volumes"},
		{http.MethodDelete, "/docker/volumes/spare"},
		{http.MethodGet, "/docker/stacks"},
		{http.MethodPost, "/docker/stacks/shop/start"},
		{http.MethodPost, "/docker/stacks/shop/stop"},
		{http.MethodPost, "/docker/stacks/shop/restart"},
	}
	if len(routes) != len(Routes) {
		t.Fatalf("Routes lists %d routes, the test %d", len(Routes), len(routes))
	}
	for _, route := range routes {
		if response := serve(s, route.method, route.path, user); response.Code != http.StatusForbidden {
			t.Fatalf("user %s %s: %d", route.method, route.path, response.Code)
		}
	}
	if calls := f.mutations(); len(calls) != 0 || len(opener.argv) != 0 {
		t.Fatalf("a user account reached Docker: %v %v", calls, opener.argv)
	}
	for _, who := range []*auth.AccountIdentity{admin, nil} {
		if response := serve(s, http.MethodGet, "/docker/containers", who); response.Code != http.StatusOK {
			t.Fatalf("admin listing: %d", response.Code)
		}
	}
	if response := serve(s, http.MethodPost, "/docker/containers/"+webID+"/restart", admin); response.Code != http.StatusOK {
		t.Fatalf("admin restart: %d %s", response.Code, response.Body)
	}
}

type fakeOpener struct {
	name, marker string
	argv         []string
	err          error
}

func (o *fakeOpener) OpenTerminal(_ *http.Request, name, marker string, argv []string) (TerminalResponse, error) {
	o.name, o.marker, o.argv = name, marker, argv
	if o.err != nil {
		return TerminalResponse{}, o.err
	}
	return TerminalResponse{HostID: "local", Session: name, Created: true}, nil
}

func TestTerminalRunsOnlyTheValidatedID(t *testing.T) {
	f := newFakeDocker(t)
	f.add("5555555555555555555555555555555555555555555555555555555555555555", "/shop.web_1;rm -rf", "x", "running", "Up", nil, nil)
	opener := &fakeOpener{}
	s := newTestService(t, f, opener)
	response := serve(s, http.MethodPost, "/docker/containers/555555555555/terminal", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("terminal: %d %s", response.Code, response.Body)
	}
	fullID := strings.Repeat("5", 64)
	want := []string{"/usr/bin/docker", "--host", "unix://" + f.socket, "exec", "-it", fullID, "sh", "-c", shellProbe}
	if strings.Join(opener.argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv: %q", opener.argv)
	}
	if opener.marker != fullID || opener.name != "docker-shop-web_1-rm-rf" {
		t.Fatalf("session %q marker %q", opener.name, opener.marker)
	}
	var body TerminalResponse
	if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Session != "docker-shop-web_1-rm-rf" || body.HostID != "local" {
		t.Fatalf("response: %s", response.Body)
	}

	opener.argv = nil
	if response := serve(s, http.MethodPost, "/docker/containers/"+workerID+"/terminal", nil); response.Code != http.StatusConflict || opener.argv != nil {
		t.Fatalf("stopped container: %d %s", response.Code, response.Body)
	}
	opener.err = ErrTerminalUnavailable
	if response := serve(s, http.MethodPost, "/docker/containers/"+webID+"/terminal", nil); response.Code != http.StatusNotImplemented {
		t.Fatalf("no local tmux: %d", response.Code)
	}
	noCLI := New(Config{Enabled: true, Socket: f.socket}, opener)
	if response := serve(noCLI, http.MethodPost, "/docker/containers/"+webID+"/terminal", nil); response.Code != http.StatusNotImplemented ||
		!strings.Contains(response.Body.String(), "docker est introuvable") {
		t.Fatalf("no docker CLI: %d %s", response.Code, response.Body)
	}
	if noCLI.List(context.Background()).Terminal || !s.List(context.Background()).Terminal {
		t.Fatal("terminal flag of the listing")
	}
}

func TestSessionName(t *testing.T) {
	for in, want := range map[string]string{
		"web":                    "docker-web",
		"backend-postgres-1":     "docker-backend-postgres-1",
		"shop.web.1":             "docker-shop-web-1",
		"-.--":                   "docker-conteneur",
		"été":                    "docker-t",
		strings.Repeat("a", 100): "docker-" + strings.Repeat("a", 54),
	} {
		if got := SessionName(in); got != want {
			t.Errorf("SessionName(%q) = %q, want %q", in, got, want)
		}
		if got := SessionName(in); len(got)+3 > 64 {
			t.Errorf("%q leaves no room for a suffix", got)
		}
	}
}

func TestInspectRouteMasksEnvironment(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	response := serve(s, http.MethodGet, "/docker/containers/"+webID+"/inspect", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", response.Code, response.Body)
	}
	body := response.Body.String()
	if strings.Contains(body, "s3cr3t") || !strings.Contains(body, `"API_TOKEN=••••"`) || !strings.Contains(body, `"PATH=••••"`) {
		t.Fatalf("environment not masked: %s", body)
	}
	if !strings.Contains(body, `"RestartCount":1`) {
		t.Fatalf("inspect document altered: %s", body)
	}
}

func TestLogsRoute(t *testing.T) {
	f := newFakeDocker(t)
	f.setLogs(webID, append(frame(1, "2026-11-17T08:00:00.000000001Z GET / 200\n"),
		frame(2, "2026-11-17T08:00:01.5Z \x1b[31merror\x1b[0m: boom\n")...), "")
	f.setLogs(dbID, []byte("2026-11-17T08:00:02Z ready\r\n2026-11-17T08:00:03Z \x1b[1mbold\x1b[0m\r\n"), "application/vnd.docker.raw-stream")
	s := newTestService(t, f, nil)

	response := serve(s, http.MethodGet, "/docker/containers/"+webID+"/logs?tail=50", nil)
	var logs LogsResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &logs) != nil {
		t.Fatalf("logs: %d %s", response.Code, response.Body)
	}
	want := []LogLine{{T: "2026-11-17T08:00:00.000000001Z", S: "out", M: "GET / 200"}, {T: "2026-11-17T08:00:01.5Z", S: "err", M: "error: boom"}}
	if len(logs.Lines) != 2 || logs.Lines[0] != want[0] || logs.Lines[1] != want[1] || logs.Truncated {
		t.Fatalf("demultiplexed lines: %+v", logs)
	}
	if logs.NextSince != "1794902401.500000001" {
		t.Fatalf("next since: %q", logs.NextSince)
	}
	if got := f.logQueries()[0]; !strings.Contains(got, "stderr=1") || !strings.Contains(got, "stdout=1") ||
		!strings.Contains(got, "timestamps=1") || !strings.Contains(got, "tail=50") || strings.Contains(got, "since") {
		t.Fatalf("Docker query: %s", got)
	}

	response = serve(s, http.MethodGet, "/docker/containers/"+dbID+"/logs?tail=9999&since=1794902401.500000001", nil)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &logs) != nil {
		t.Fatalf("tty logs: %d %s", response.Code, response.Body)
	}
	if len(logs.Lines) != 2 || logs.Lines[0].M != "ready" || logs.Lines[1].M != "bold" || logs.Lines[1].S != "out" {
		t.Fatalf("raw stream lines: %+v", logs.Lines)
	}
	if got := f.logQueries()[1]; !strings.Contains(got, "tail=2000") || !strings.Contains(got, "since=1794902401.500000001") {
		t.Fatalf("tail must be capped and since passed on: %s", got)
	}

	for _, query := range []string{"tail=-1", "tail=abc", "since=yesterday", "since=1.2.3", "since=1%3Brm"} {
		if response := serve(s, http.MethodGet, "/docker/containers/"+webID+"/logs?"+query, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", query, response.Code)
		}
	}
}
