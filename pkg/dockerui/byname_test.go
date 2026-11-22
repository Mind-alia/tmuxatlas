package dockerui

import (
	"context"
	"errors"
	"testing"
)

func TestRestartByName(t *testing.T) {
	f := newFakeDocker(t)
	s := newTestService(t, f, nil)
	result, err := s.RestartByName(context.Background(), "root", "shop-db-1")
	if err != nil || result.ID != dbID || result.Action != "restart" {
		t.Fatalf("restart by name: %+v %v", result, err)
	}
	if calls := f.mutations(); len(calls) != 1 || calls[0] != "POST /containers/"+dbID+"/restart" {
		t.Fatalf("docker calls: %v", calls)
	}
	// A prefix or an unknown name restarts nothing.
	for _, name := range []string{"shop", "shop-db", "missing", "", "../web", "web;rm", webID[:12]} {
		if _, err := s.RestartByName(context.Background(), "root", name); err == nil {
			t.Fatalf("name %q must be refused", name)
		}
	}
	if calls := f.mutations(); len(calls) != 1 {
		t.Fatalf("a refused name reached Docker: %v", calls)
	}
}

func TestRestartByNameKeepsTheViewSwitches(t *testing.T) {
	f := newFakeDocker(t)
	off := New(Config{Enabled: false, Socket: f.socket}, nil)
	if _, err := off.RestartByName(context.Background(), "root", "web"); !errors.Is(err, errDisabled) {
		t.Fatalf("disabled view: %v", err)
	}
	gated := New(Config{Enabled: true, Socket: f.socket, Gate: func() string { return "refusé" }}, nil)
	if _, err := gated.RestartByName(context.Background(), "root", "web"); err == nil || ErrorMessage(err) != "refusé" {
		t.Fatalf("gated view: %v", err)
	}
	if calls := f.mutations(); len(calls) != 0 {
		t.Fatalf("a refused restart reached Docker: %v", calls)
	}
}
