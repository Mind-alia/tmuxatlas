package dockerui

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestDeployTargetReadsTheLabels(t *testing.T) {
	f := newFakeDocker(t)
	addBlog(f)
	f.mu.Lock()
	f.containers[blogAppID]["ImageID"] = "sha256:" + blogAppID
	f.containers[blogAppID]["Labels"].(map[string]string)[labelEnvFiles] = "/srv/apps/blog/prod.env"
	f.mu.Unlock()
	f.setState(blogCacheID, "running", "Up 2 seconds (health: starting)")
	s := newTestService(t, f, nil)
	s.self = func() string { return "" }

	target, err := s.DeployTarget(context.Background(), "blog")
	if err != nil {
		t.Fatal(err)
	}
	if target.Project != "blog" || target.WorkingDir != "/srv/apps/blog" || target.Hub != "" ||
		!reflect.DeepEqual(target.ConfigFiles, []string{"/srv/apps/blog/compose.yaml", "/srv/apps/blog/compose.prod.yaml"}) ||
		!reflect.DeepEqual(target.EnvFiles, []string{"/srv/apps/blog/prod.env"}) {
		t.Fatalf("cible : %+v", target)
	}
	byService := make(map[string]DeployService)
	for _, service := range target.Services {
		byService[service.Service] = service
	}
	if app := byService["app"]; app.Container != "blog-app-1" || app.Image != "blog:2" || app.ImageID != "sha256:"+blogAppID || app.State != "running" {
		t.Fatalf("app : %+v", app)
	}
	if byService["db"].Health != "healthy" || byService["cache"].Health != "starting" || byService["app"].Health != "" {
		t.Fatalf("santé : %+v", byService)
	}
	if job := byService["migrate"]; job.State != "exited" || job.ExitCode == nil || *job.ExitCode != 0 {
		t.Fatalf("service ponctuel : %+v", job)
	}
	// Start order: the app comes after what it depends on.
	if last := target.Services[len(target.Services)-1].Service; last != "migrate" && last != "app" {
		t.Fatalf("ordre : %+v", target.Services)
	}

	s.self = func() string { return blogDBID }
	if target, _ := s.DeployTarget(context.Background(), "blog"); target.Hub != "blog-db-1" {
		t.Fatalf("hub : %q", target.Hub)
	}
	if len(f.mutations()) != 0 {
		t.Fatalf("Docker modifié : %v", f.mutations())
	}
}

func TestDeployTargetKeepsTheSwitchesOfTheView(t *testing.T) {
	f := newFakeDocker(t)
	addBlog(f)
	s := newTestService(t, f, nil)
	for name, want := range map[string]int{"unknown": http.StatusNotFound, "../blog": http.StatusBadRequest, "-p": http.StatusBadRequest} {
		if _, err := s.DeployTarget(context.Background(), name); err == nil {
			t.Errorf("%q accepté", name)
		} else if status, _ := ErrorStatus(err); status != want {
			t.Errorf("%q : %d", name, status)
		}
	}
	off := New(Config{Enabled: false, Socket: f.socket}, nil)
	if _, err := off.DeployTarget(context.Background(), "blog"); err == nil {
		t.Fatal("TMUXATLAS_DOCKER=off ignoré")
	}
	gated := New(Config{Enabled: true, Socket: f.socket, Gate: func() string { return "un compte non administrateur est actif" }}, nil)
	_, err := gated.DeployTarget(context.Background(), "blog")
	if status, message := ErrorStatus(err); err == nil || status != http.StatusServiceUnavailable || message != "un compte non administrateur est actif" {
		t.Fatalf("garde des comptes : %v", err)
	}
}
