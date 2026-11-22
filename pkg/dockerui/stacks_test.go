package dockerui

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Full IDs of the containers of the fake "blog" stack.
const (
	blogAppID   = "aaaa000000000000000000000000000000000000000000000000000000000001"
	blogDBID    = "aaaa000000000000000000000000000000000000000000000000000000000002"
	blogCacheID = "aaaa000000000000000000000000000000000000000000000000000000000003"
	blogJobID   = "aaaa000000000000000000000000000000000000000000000000000000000004"
)

// addBlog adds a stack whose app depends on db and cache, plus a finished job.
func addBlog(f *fakeDocker) {
	labels := func(service, depends string) map[string]string {
		out := map[string]string{
			labelProject: "blog", labelService: service,
			labelWorkingDir: "/srv/apps/blog", labelConfigFiles: "/srv/apps/blog/compose.yaml,/srv/apps/blog/compose.prod.yaml",
		}
		if depends != "" {
			out[labelDependsOn] = depends
		}
		return out
	}
	f.add(blogAppID, "/blog-app-1", "blog:2", "running", "Up 1 hour", labels("app", "db:service_healthy:false,cache:service_started:true"), nil)
	f.add(blogDBID, "/blog-db-1", "postgres:16", "running", "Up 1 hour (healthy)", labels("db", ""), nil)
	f.add(blogCacheID, "/blog-cache-1", "redis:7", "running", "Up 1 hour", labels("cache", ""), nil)
	f.add(blogJobID, "/blog-migrate-1", "blog:2", "exited", "Exited (0) 1 hour ago", labels("migrate", "db:service_healthy:false"), nil)
}

func stackByName(list []Stack) map[string]Stack {
	out := make(map[string]Stack, len(list))
	for _, stack := range list {
		out[stack.Name] = stack
	}
	return out
}

func serviceNames(list []Container) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Service)
	}
	return out
}

func TestStacksGroupByComposeProject(t *testing.T) {
	f := newFakeDocker(t)
	addBlog(f)
	s := newTestService(t, f, nil)
	got := s.Stacks(context.Background())
	if !got.Available || got.Error != "" {
		t.Fatalf("available=%v error=%q", got.Available, got.Error)
	}
	if len(got.Stacks) != 2 || got.Stacks[0].Name != "blog" || got.Stacks[1].Name != "shop" {
		t.Fatalf("stacks must be sorted by name: %+v", got.Stacks)
	}
	blog := got.Stacks[0]
	if blog.WorkingDir != "/srv/apps/blog" ||
		!reflect.DeepEqual(blog.ConfigFiles, []string{"/srv/apps/blog/compose.yaml", "/srv/apps/blog/compose.prod.yaml"}) {
		t.Fatalf("working directory and compose files: %+v", blog)
	}
	if blog.State != stackPartial || blog.Running != 3 || blog.Total != 4 {
		t.Fatalf("blog has a stopped job: %+v", blog)
	}
	if names := serviceNames(blog.Services); !reflect.DeepEqual(names, []string{"cache", "db", "app", "migrate"}) {
		t.Fatalf("services must be in start order: %v", names)
	}
	db := blog.Services[1]
	if db.ID != blogDBID || db.Name != "blog-db-1" || db.Image != "postgres:16" || db.State != "running" || db.Health != "healthy" {
		t.Fatalf("service view: %+v", db)
	}
	if job := blog.Services[3]; job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("exit code of the stopped job: %+v", job)
	}
	shop := got.Stacks[1]
	if shop.State != stackRunning || shop.Running != 2 || shop.Total != 2 || shop.WorkingDir != "" || len(shop.ConfigFiles) != 0 || shop.Protected != "" {
		t.Fatalf("shop: %+v", shop)
	}
	if len(shop.Services[1].Ports) != 2 || shop.Services[1].Ports[0].Public != 8080 {
		t.Fatalf("ports of shop/web: %+v", shop.Services[1].Ports)
	}
	if len(got.Standalone) != 2 || got.Standalone[0].Name != "tool" || got.Standalone[1].Name != "worker" {
		t.Fatalf("containers outside Compose: %+v", got.Standalone)
	}
	if len(got.Routes) != 1 || got.Routes[0].Source != routePort || got.Routes[0].Host != "0.0.0.0:8080" ||
		got.Routes[0].Port != 80 || got.Routes[0].Container != "web" || got.Routes[0].Project != "shop" {
		t.Fatalf("routes: %+v", got.Routes)
	}
	if calls := f.mutations(); len(calls) != 0 || f.inspectCount(blogDBID) != 0 {
		t.Fatalf("the listing must only read the container list: %v", calls)
	}

	for _, id := range []string{blogAppID, blogDBID, blogCacheID} {
		f.setState(id, "exited", "Exited (0) 1 second ago")
	}
	if blog := stackByName(s.Stacks(context.Background()).Stacks)["blog"]; blog.State != stackStopped || blog.Running != 0 {
		t.Fatalf("stopped stack: %+v", blog)
	}
}

func TestStacksUnavailable(t *testing.T) {
	f := newFakeDocker(t)
	off := New(Config{Enabled: false, Socket: f.socket}, nil)
	if got := off.Stacks(context.Background()); got.Available || got.Error == "" || got.Stacks == nil || got.Routes == nil {
		t.Fatalf("disabled view: %+v", got)
	}
	if response := serve(off, http.MethodPost, "/docker/stacks/shop/stop", nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled view, action: %d", response.Code)
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("a disabled view reached Docker: %v", calls)
	}
}

func TestStackOrder(t *testing.T) {
	member := func(name, service, depends string) apiContainer {
		return apiContainer{ID: name, Names: []string{"/" + name}, Labels: map[string]string{labelService: service, labelDependsOn: depends}}
	}
	for _, test := range []struct {
		name    string
		members []apiContainer
		want    []string
	}{
		{"without labels, by service name", []apiContainer{member("c", "web", ""), member("a", "api", ""), member("b", "db", "")}, []string{"a", "b", "c"}},
		{"dependencies first", []apiContainer{
			member("web", "web", "api:service_started:false"), member("api", "api", "db:service_healthy:true,cache:service_started:false"),
			member("db", "db", ""), member("cache", "cache", ""),
		}, []string{"cache", "db", "api", "web"}},
		{"replicas stay together, by name", []apiContainer{
			member("w-2", "worker", "db"), member("db-1", "db", ""), member("w-1", "worker", "db"),
		}, []string{"db-1", "w-1", "w-2"}},
		{"unknown and self dependencies are ignored", []apiContainer{
			member("b", "b", "ghost:service_started:false, b:service_started:false"), member("a", "a", ""),
		}, []string{"a", "b"}},
		{"a cycle falls back to the name order", []apiContainer{
			member("y", "y", "x"), member("x", "x", "y"), member("z", "z", "x"), member("free", "free", ""),
		}, []string{"free", "x", "y", "z"}},
		{"malformed label", []apiContainer{member("b", "b", ",,:,a::"), member("a", "a", "")}, []string{"a", "b"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, c := range stackOrder(test.members) {
				got = append(got, c.ID)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("order = %v, want %v", got, test.want)
			}
		})
	}
}

func stackAction(t *testing.T, s *Service, project, action string) StackActionResponse {
	t.Helper()
	response := serve(s, http.MethodPost, "/docker/stacks/"+project+"/"+action, nil)
	var body StackActionResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("%s %s: %d %s", action, project, response.Code, response.Body)
	}
	return body
}

func statuses(results []StackActionResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Service+"="+r.Status)
	}
	return out
}

func TestStackActionsFollowDependencies(t *testing.T) {
	f := newFakeDocker(t)
	addBlog(f)
	s := newTestService(t, f, nil)
	post := func(id, action string) string { return "POST /containers/" + id + "/" + action }

	// Restart: the running containers, dependencies first; the finished job is left alone.
	got := stackAction(t, s, "blog", "restart")
	if !got.OK || got.Stack != "blog" || got.Action != "restart" ||
		!reflect.DeepEqual(statuses(got.Results), []string{"cache=done", "db=done", "app=done", "migrate=unchanged"}) {
		t.Fatalf("restart: %+v", got)
	}
	want := []string{post(blogCacheID, "restart"), post(blogDBID, "restart"), post(blogAppID, "restart")}
	if calls := f.mutations(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("restart calls: %v", calls)
	}

	// Stop: the reverse order, so nothing loses a dependency while it runs.
	got = stackAction(t, s, "blog", "stop")
	if !got.OK || !reflect.DeepEqual(statuses(got.Results), []string{"migrate=unchanged", "app=done", "db=done", "cache=done"}) {
		t.Fatalf("stop: %+v", got)
	}
	want = append(want, post(blogAppID, "stop"), post(blogDBID, "stop"), post(blogCacheID, "stop"))
	if calls := f.mutations(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("stop calls: %v", calls)
	}

	// Stopping again changes nothing and calls nothing.
	got = stackAction(t, s, "blog", "stop")
	if !got.OK || len(f.mutations()) != len(want) {
		t.Fatalf("second stop: %+v %v", got, f.mutations()[len(want):])
	}
	for _, result := range got.Results {
		if result.Status != resultUnchanged {
			t.Fatalf("second stop: %+v", got.Results)
		}
	}

	// Start: every stopped container, the job included, dependencies first.
	got = stackAction(t, s, "blog", "start")
	if !got.OK || !reflect.DeepEqual(statuses(got.Results), []string{"cache=done", "db=done", "app=done", "migrate=done"}) {
		t.Fatalf("start: %+v", got)
	}
	want = append(want, post(blogCacheID, "start"), post(blogDBID, "start"), post(blogAppID, "start"), post(blogJobID, "start"))
	if calls := f.mutations(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("start calls: %v", calls)
	}
	for _, call := range f.mutations() {
		if strings.Contains(call, "?") || strings.HasPrefix(call, "DELETE") || !strings.Contains(call, "/containers/aaaa") {
			t.Fatalf("a stack action must only start, stop or restart its own containers: %s", call)
		}
	}
}

func TestStackActionReportsPartialFailure(t *testing.T) {
	f := newFakeDocker(t)
	addBlog(f)
	f.mu.Lock()
	f.failing = map[string]bool{blogDBID: true}
	f.mu.Unlock()
	s := newTestService(t, f, nil)

	got := stackAction(t, s, "blog", "stop")
	if got.OK || !reflect.DeepEqual(statuses(got.Results), []string{"migrate=unchanged", "app=done", "db=failed", "cache=skipped"}) {
		t.Fatalf("partial stop: %+v", got)
	}
	failed := got.Results[2]
	if failed.ID != blogDBID || failed.Name != "blog-db-1" || !strings.Contains(failed.Error, "driver failed") {
		t.Fatalf("failed container: %+v", failed)
	}
	if got.Results[1].Error != "" || got.Results[3].Error != "" {
		t.Fatalf("only the failed container carries an error: %+v", got.Results)
	}
	want := []string{"POST /containers/" + blogAppID + "/stop", "POST /containers/" + blogDBID + "/stop"}
	if calls := f.mutations(); !reflect.DeepEqual(calls, want) {
		t.Fatalf("nothing may be attempted after a failure: %v", calls)
	}
	blog := stackByName(s.Stacks(context.Background()).Stacks)["blog"]
	if blog.State != stackPartial || blog.Running != 2 {
		t.Fatalf("stack after a partial stop: %+v", blog)
	}
}

func TestStackWithTheHubCannotBeStopped(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	s.self = func() string { return webID[:12] }

	shop := stackByName(s.Stacks(context.Background()).Stacks)["shop"]
	if !strings.Contains(shop.Protected, "« web »") {
		t.Fatalf("the listing must say why the stack is protected: %+v", shop)
	}
	for _, action := range []string{"stop", "restart"} {
		response := serve(s, http.MethodPost, "/docker/stacks/shop/"+action, nil)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Atlas lui-même") {
			t.Fatalf("%s of the hub's stack: %d %s", action, response.Code, response.Body)
		}
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("a refused stack action reached Docker: %v", calls)
	}
	f.setState(dbID, "exited", "Exited (0) 1 minute ago")
	if got := stackAction(t, s, "shop", "start"); !got.OK || !reflect.DeepEqual(statuses(got.Results), []string{"db=done", "web=unchanged"}) {
		t.Fatalf("start stays allowed: %+v", got)
	}

	// A hostname that is not a container ID protects nothing.
	for _, self := range []string{"", "atlas-host", "1111", "zzzzzzzzzzzz"} {
		s.self = func() string { return self }
		if shop := stackByName(s.Stacks(context.Background()).Stacks)["shop"]; shop.Protected != "" {
			t.Fatalf("self=%q: %+v", self, shop)
		}
	}
}

func TestStackActionRefusals(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	for path, want := range map[string]int{
		"/docker/stacks/ghost/stop":           http.StatusNotFound,
		"/docker/stacks/shop%3Brm/stop":       http.StatusBadRequest,
		"/docker/stacks/-shop/stop":           http.StatusBadRequest,
		"/docker/stacks/shop%20x/restart":     http.StatusBadRequest,
		"/docker/stacks/" + webID + "x/start": http.StatusNotFound,
		"/docker/stacks/shop/down":            http.StatusNotFound,
		"/docker/stacks/shop/pull":            http.StatusNotFound,
		"/docker/stacks/shop/rm":              http.StatusNotFound,
		"/docker/stacks/shop":                 http.StatusMethodNotAllowed,
	} {
		if response := serve(s, http.MethodPost, path, nil); response.Code != want && !(want == http.StatusMethodNotAllowed && response.Code == http.StatusNotFound) {
			t.Errorf("POST %s: %d, want %d", path, response.Code, want)
		}
	}
	if response := serve(s, http.MethodDelete, "/docker/stacks/shop", nil); response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE of a stack: %d", response.Code)
	}
	if _, err := s.StackAction(context.Background(), "down", "shop"); err == nil {
		t.Error("unknown action accepted")
	}

	// One action at a time per stack.
	if !s.lockStack("shop") {
		t.Fatal("lock")
	}
	if response := serve(s, http.MethodPost, "/docker/stacks/shop/stop", nil); response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "déjà en cours") {
		t.Fatalf("concurrent action: %d %s", response.Code, response.Body)
	}
	s.unlockStack("shop")
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("a refused stack action reached Docker: %v", calls)
	}
	if got := stackAction(t, s, "shop", "stop"); !got.OK || len(f.mutations()) != 2 {
		t.Fatalf("the lock must be released: %+v", got)
	}
}
