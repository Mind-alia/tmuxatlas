package peer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/files"
	"github.com/LosFurina/tmuxatlas/pkg/identity"
)

func TestDispatcherServesOwnFilesWithinCeiling(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a.txt"), []byte("contenu"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := files.NewService(files.Config{Mode: files.ModeReadOnly, Root: home, Deny: files.DefaultDeny})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	executor := &recordingExecutor{}
	cache, _ := NewOutcomeCache(DefaultOutcomeCacheConfig(), nil)
	dispatcher := &RequestDispatcher{
		Generation: 1, HostID: "h", Executor: executor, Cache: cache, Files: service,
		Capabilities: map[string]struct{}{CapabilitySessionActions: {}, CapabilityFiles: {}},
	}
	request := func(id, op, payload string, session string) RuntimeRequest {
		return RuntimeRequest{RequestID: id, Generation: 1, Deadline: time.Now().Add(time.Minute),
			Operation: op, Target: SessionTarget{HostID: "h", Session: session}, Payload: json.RawMessage(payload)}
	}
	_, outcome := dispatcher.Dispatch(context.Background(), request("r1", "fs.read", `{"path":"/a.txt"}`, ""))
	if outcome.Error != nil || outcome.Result == nil {
		t.Fatalf("read: %+v", outcome.Error)
	}
	_, outcome = dispatcher.Dispatch(context.Background(), request("r2", "fs.delete", `{"path":"/a.txt"}`, ""))
	if outcome.Error == nil || decodeFileError(*outcome.Error).Code != files.CodeReadOnly {
		t.Fatalf("delete beyond the ceiling: %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(home, "a.txt")); err != nil {
		t.Fatal("file deleted despite ro")
	}
	_, outcome = dispatcher.Dispatch(context.Background(), request("r3", "fs.read", `{"path":"/a.txt"}`, "s"))
	if outcome.Error == nil || outcome.Error.Code != ErrorInvalidTarget {
		t.Fatalf("file operation with a session: %+v", outcome)
	}
	_, outcome = dispatcher.Dispatch(context.Background(), request("r4", "fs.read", `{"path":"/a.txt"}`, ""))
	if outcome.Error != nil {
		t.Fatalf("retried read must run again, not be cached: %+v", outcome.Error)
	}
	delete(dispatcher.Capabilities, CapabilityFiles)
	_, outcome = dispatcher.Dispatch(context.Background(), request("r5", "fs.read", `{"path":"/a.txt"}`, ""))
	if outcome.Error == nil || outcome.Error.Code != ErrorCapabilityUnsupported {
		t.Fatalf("without capability: %+v", outcome)
	}
	if executor.calls.Load() != 0 {
		t.Fatal("tmux executor ran a file operation")
	}
	dispatcher.Capabilities[CapabilityFiles] = struct{}{}
	dispatcher.Files = nil
	_, outcome = dispatcher.Dispatch(context.Background(), request("r6", "fs.list", `{"path":"/"}`, ""))
	if outcome.Error == nil || decodeFileError(*outcome.Error).Code != files.CodeDisabled {
		t.Fatalf("no service: %+v", outcome)
	}
}

func TestFileRouterNeverFallsBackForOfflineHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	local := t.TempDir()
	if err := os.WriteFile(filepath.Join(local, "hub.txt"), []byte("hub"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, _ := files.NewService(files.Config{Mode: files.ModeReadWrite, Root: local})
	defer service.Close()
	manager, _ := newNegotiationManager(t)
	manager.RegisterPeer("offline", "inv-alice@srv", "", nil)
	router := NewFileRouter(manager, service, time.Second)
	if _, err := router.Execute(context.Background(), "offline", "read", map[string]string{"path": "/hub.txt"}); decodeFileError(err).Code != files.CodeOffline {
		t.Fatalf("offline host: %v", err)
	}
	if _, err := router.Execute(context.Background(), "unknown", "list", map[string]string{"path": "/"}); decodeFileError(err).Code != files.CodeNotFound {
		t.Fatalf("unknown host: %v", err)
	}
	if _, err := router.Execute(context.Background(), manager.LocalID(), "read", map[string]string{"path": "/hub.txt"}); err != nil {
		t.Fatalf("local host: %v", err)
	}
}

func newNegotiationManager(t *testing.T) (*Manager, error) {
	t.Helper()
	hub, err := identity.Generate("hub")
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(hub, nil, nil), nil
}
