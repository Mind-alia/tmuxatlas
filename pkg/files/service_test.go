package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func newTestService(t *testing.T, mode Mode) (*Service, string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for _, dir := range []string{"projets/app", ".ssh", ".config/tmuxatlas", "vide"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"projets/app/main.go": "package main\n", ".ssh/id_ed25519": "SECRET", ".config/tmuxatlas/.env": "TOKEN",
		"projets/app/.env.local": "SECRET", "projets/app/server.key": "SECRET", "notes.txt": "bonjour",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "outside.txt"), []byte("OUTSIDE"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Config{Mode: mode, Root: home, Deny: DefaultDeny})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, home
}

func call(t *testing.T, s *Service, op string, payload map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	result, err := s.Execute(context.Background(), op, raw)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, nil
}

func code(err error) string {
	var fileError *Error
	if errors.As(err, &fileError) {
		return fileError.Code
	}
	if err != nil {
		return "other:" + err.Error()
	}
	return ""
}

func readAll(t *testing.T, s *Service, p string) (string, error) {
	t.Helper()
	result, err := call(t, s, "read", map[string]any{"path": p})
	if err != nil {
		return "", err
	}
	data, _ := base64.StdEncoding.DecodeString(result["data"].(string))
	return string(data), nil
}

func TestJailRefusesTraversalLinksSecretsAndSpecialFiles(t *testing.T) {
	s, home := newTestService(t, ModeReadOnly)
	if got, err := readAll(t, s, "/../outside.txt"); err == nil && strings.Contains(got, "OUTSIDE") {
		t.Fatal("path traversal left the jail")
	}
	if got, err := readAll(t, s, "/notes.txt"); err != nil || got != "bonjour" {
		t.Fatalf("plain read: %q %v", got, err)
	}
	_ = os.Symlink(filepath.Join(home, "..", "outside.txt"), filepath.Join(home, "escape"))
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "keys"))
	_ = os.Symlink("/etc", filepath.Join(home, "etc"))
	for _, p := range []string{"/escape", "/keys/id_ed25519", "/etc/passwd"} {
		if _, err := readAll(t, s, p); code(err) != CodeDenied {
			t.Errorf("%s through a link: %v", p, err)
		}
	}
	if _, err := call(t, s, "list", map[string]any{"path": "/keys"}); code(err) != CodeDenied {
		t.Errorf("listing through a link: %v", err)
	}
	for _, p := range []string{"/.ssh/id_ed25519", "/.config/tmuxatlas/.env", "/projets/app/.env.local", "/projets/app/server.key", "/.ssh"} {
		if _, err := readAll(t, s, p); code(err) != CodeDenied {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(home, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, s, "/pipe"); code(err) != CodeDenied {
		t.Errorf("fifo: %v", err)
	}
	listing, err := call(t, s, "list", map[string]any{"path": "/"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(listing)
	if strings.Contains(string(raw), ".ssh") {
		t.Fatalf("listing shows a protected entry: %s", raw)
	}
	sub, _ := call(t, s, "list", map[string]any{"path": "/projets/app"})
	raw, _ = json.Marshal(sub)
	if strings.Contains(string(raw), ".env.local") || strings.Contains(string(raw), "server.key") || !strings.Contains(string(raw), "main.go") {
		t.Fatalf("listing: %s", raw)
	}
}

func TestCeilingIsEnforcedLocally(t *testing.T) {
	off, _ := newTestService(t, ModeOff)
	if _, err := call(t, off, "list", map[string]any{"path": "/"}); code(err) != CodeDisabled {
		t.Fatalf("off: %v", err)
	}
	if info, err := call(t, off, "info", nil); err != nil || info["mode"] != "off" {
		t.Fatalf("info: %v %v", info, err)
	}
	ro, _ := newTestService(t, ModeReadOnly)
	for op, payload := range map[string]map[string]any{
		"mkdir":  {"path": "/x"},
		"delete": {"path": "/notes.txt"},
		"rename": {"path": "/notes.txt", "to": "/n.txt"},
		"write":  {"path": "/n.txt", "upload_id": "0123456789abcdef", "data": "", "final": true},
	} {
		if _, err := call(t, ro, op, payload); code(err) != CodeReadOnly {
			t.Errorf("ro %s: %v", op, err)
		}
	}
}

func TestChunkedWriteIsAtomicAndDetectsConflicts(t *testing.T) {
	s, home := newTestService(t, ModeReadWrite)
	upload := func(id, p string, parts []string, replace bool, version string) (map[string]any, error) {
		var offset int
		var result map[string]any
		var err error
		for i, part := range parts {
			result, err = call(t, s, "write", map[string]any{
				"path": p, "upload_id": id, "offset": offset, "data": base64.StdEncoding.EncodeToString([]byte(part)),
				"final": i == len(parts)-1, "replace": replace, "version": version, "actor": "alice",
			})
			if err != nil {
				return nil, err
			}
			offset += len(part)
		}
		return result, nil
	}
	result, err := upload("aaaaaaaaaaaaaaaa", "/projets/new.txt", []string{"un ", "deux"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "projets/new.txt")); string(got) != "un deux" {
		t.Fatalf("content %q", got)
	}
	version := result["version"].(string)
	if _, err := upload("bbbbbbbbbbbbbbbb", "/projets/new.txt", []string{"x"}, false, ""); code(err) != CodeConflict {
		t.Fatalf("overwrite without replace: %v", err)
	}
	if _, err := upload("cccccccccccccccc", "/projets/new.txt", []string{"trois"}, true, "0-0"); code(err) != CodeConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := upload("dddddddddddddddd", "/projets/new.txt", []string{"trois"}, true, version); err != nil {
		t.Fatal(err)
	}
	if _, err := upload("eeeeeeeeeeeeeeee", "/.ssh/authorized_keys", []string{"ssh-ed25519 AAAA"}, true, ""); code(err) != CodeDenied {
		t.Fatalf("write into .ssh: %v", err)
	}
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "keys"))
	if _, err := upload("ffffffffffffffff", "/keys/authorized_keys", []string{"x"}, true, ""); code(err) != CodeDenied {
		t.Fatalf("write through a link: %v", err)
	}
	// Out-of-order chunks and leftovers.
	if _, err := call(t, s, "write", map[string]any{"path": "/a.txt", "upload_id": "1111111111111111", "offset": 5, "data": ""}); err == nil {
		t.Fatal("chunk without a start accepted")
	}
	entries, _ := os.ReadDir(filepath.Join(home, "projets"))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), uploadPrefix) {
			t.Fatalf("temporary file left: %s", entry.Name())
		}
	}
}

func TestRenameMkdirDelete(t *testing.T) {
	s, home := newTestService(t, ModeReadWrite)
	if _, err := call(t, s, "mkdir", map[string]any{"path": "/projets/docs"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, s, "rename", map[string]any{"path": "/notes.txt", "to": "/projets/docs/notes.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, s, "rename", map[string]any{"path": "/projets/docs/notes.txt", "to": "/.ssh/notes.txt"}); code(err) != CodeDenied {
		t.Fatalf("rename into .ssh: %v", err)
	}
	if _, err := call(t, s, "delete", map[string]any{"path": "/projets/docs"}); code(err) != CodeConflict {
		t.Fatalf("non-empty folder deleted: %v", err)
	}
	if _, err := call(t, s, "delete", map[string]any{"path": "/projets/docs/notes.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, s, "delete", map[string]any{"path": "/"}); code(err) != CodeInvalid {
		t.Fatalf("root deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "projets/docs/notes.txt")); !os.IsNotExist(err) {
		t.Fatal("file not deleted")
	}
}

func TestLargeFilesAreRefused(t *testing.T) {
	s, home := newTestService(t, ModeReadOnly)
	big, _ := os.Create(filepath.Join(home, "big.bin"))
	_ = big.Truncate(MaxFileSize + 1)
	_ = big.Close()
	if _, err := readAll(t, s, "/big.bin"); code(err) != CodeTooLarge {
		t.Fatalf("big file: %v", err)
	}
}

func TestRenameCannotExposeAnchoredSecrets(t *testing.T) {
	s, home := newTestService(t, ModeReadWrite)
	for _, dir := range []string{".config/rclone", ".local/share/tmuxatlas", ".local/share/docs"} {
		_ = os.MkdirAll(filepath.Join(home, dir), 0o700)
	}
	_ = os.WriteFile(filepath.Join(home, ".config/rclone/rclone.conf"), []byte("R2 SECRET"), 0o600)
	for _, source := range []string{"/.config", "/.local", "/.local/share"} {
		if _, err := call(t, s, "rename", map[string]any{"path": source, "to": "/c"}); code(err) != CodeDenied {
			t.Fatalf("rename %s: %v", source, err)
		}
	}
	if _, err := readAll(t, s, "/c/rclone/rclone.conf"); err == nil {
		t.Fatal("secret exposed under a new name")
	}
	// A parent holding no protected path can still move.
	if _, err := call(t, s, "rename", map[string]any{"path": "/.local/share/docs", "to": "/docs"}); err != nil {
		t.Fatal(err)
	}
}

func TestHubRefusesReadWrite(t *testing.T) {
	if err := (Config{Mode: ModeReadWrite}).CheckHub(); !errors.Is(err, ErrHubReadWrite) {
		t.Fatalf("rw on the hub: %v", err)
	}
	for _, mode := range []Mode{ModeOff, ModeReadOnly} {
		if err := (Config{Mode: mode}).CheckHub(); err != nil {
			t.Fatalf("%s on the hub: %v", mode, err)
		}
	}
}

func TestDenyMatching(t *testing.T) {
	for p, want := range map[string]bool{
		".ssh": true, "a/.ssh/x": true, ".config/tmuxatlas/x": true, ".config/other": false,
		"app/.env": true, "app/.envrc": true, "cert.pem": true, "environment.txt": false, ".": false,
		".config/rclone/rclone.conf": true, ".claude/.credentials.json": true, ".bash_history": true,
		".config/gh/hosts.yml": true, ".npmrc": true, ".local/share/tmuxatlas/x": true, ".local/state/x": true,
		".local/share/docs": false,
	} {
		if got := denied(p, DefaultDeny); got != want {
			t.Errorf("denied(%q)=%v", p, got)
		}
	}
}
