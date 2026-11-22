package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTargetPathValidation(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := mkdir(t, v.home, "apps", "shop")
	mkdir(t, v.home, ".config", "tmuxatlas")
	mkdir(t, v.home, "apps", "a-directory.env")
	if err := os.Symlink(outside, filepath.Join(v.home, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v.dir, filepath.Join(v.home, "to-vault")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "real.env"), []byte("OUTSIDE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "real.env"), filepath.Join(app, "linked.env")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "inside.env"), []byte("INSIDE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(app, "inside.env"), filepath.Join(app, "inner-link.env")); err != nil {
		t.Fatal(err)
	}
	// A way back into the home through a link from outside is accepted: what
	// counts is where the file really is.
	if err := os.Symlink(app, filepath.Join(outside, "back")); err != nil {
		t.Fatal(err)
	}

	refused := map[string]string{
		"relative":                    "apps/shop/.env",
		"empty directory part":        ".env",
		"dot dot":                     filepath.Join(v.home, "apps") + "/../apps/shop/.env",
		"traversal out of home":       v.home + "/../.env",
		"dot segment":                 v.home + "/./apps/shop/.env",
		"double slash":                v.home + "//apps/shop/.env",
		"trailing slash":              app + "/.env/",
		"outside home":                filepath.Join(outside, ".env"),
		"system file":                 "/etc/environment.env",
		"home prefix sibling":         v.home + "-other/.env",
		"through a link to outside":   filepath.Join(v.home, "escape", ".env"),
		"file is a link to outside":   filepath.Join(app, "linked.env"),
		"file is a link, even inside": filepath.Join(app, "inner-link.env"),
		"in the vault":                filepath.Join(v.dir, ".env"),
		"in the vault's apps":         filepath.Join(v.dir, "apps", "x.env"),
		"in the vault through a link": filepath.Join(v.home, "to-vault", ".env"),
		"in the hub configuration":    filepath.Join(v.home, ".config", "tmuxatlas", ".env"),
		"missing directory":           filepath.Join(v.home, "apps", "missing", ".env"),
		"target is a directory":       filepath.Join(v.home, "apps", "a-directory.env"),
		"not an env file":             filepath.Join(app, "authorized_keys"),
		"shell profile":               filepath.Join(v.home, ".bashrc"),
		"env in the middle":           filepath.Join(app, ".env.sh;x"),
		"control character":           filepath.Join(app, "a\n.env"),
		"NUL":                         filepath.Join(app, ".env") + "\x00",
		"too long":                    filepath.Join(app, strings.Repeat("a", maxPathLength)+".env"),
	}
	for name, path := range refused {
		var validation *ValidationError
		if _, err := v.SetTarget("root", "shop", path, ""); !errors.As(err, &validation) {
			t.Errorf("%s (%q): %v", name, path, err)
		}
	}
	if apps, _ := v.Apps(); apps[0].Target != nil {
		t.Fatalf("a refused target was saved: %+v", apps[0].Target)
	}
	for _, path := range []string{
		filepath.Join(app, ".env"), filepath.Join(app, ".env.production"), filepath.Join(app, "shop.env"),
		filepath.Join(app, "inside.env"), filepath.Join(v.home, ".env"), filepath.Join(outside, "back", ".env"),
	} {
		if _, err := v.SetTarget("root", "shop", path, "shop-web.1"); err != nil {
			t.Errorf("%q: %v", path, err)
		}
	}
	var validation *ValidationError
	for _, container := range []string{"-web", "web shop", "web;reboot", "web/../x", strings.Repeat("a", 129), "wéb"} {
		if _, err := v.SetTarget("root", "shop", filepath.Join(app, ".env"), container); !errors.As(err, &validation) {
			t.Errorf("container %q: %v", container, err)
		}
	}
	if _, err := v.SetTarget("root", "shop", "", "web"); !errors.As(err, &validation) {
		t.Errorf("container without a file: %v", err)
	}
	// Two empty strings clear the target.
	if app, err := v.SetTarget("root", "shop", "", ""); err != nil || app.Target != nil {
		t.Fatalf("clear: %+v %v", app, err)
	}
}

func TestApply(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	dir := mkdir(t, v.home, "apps", "shop")
	target := filepath.Join(dir, ".env")
	if _, err := v.Import("root", "shop", "TOKEN="+testValue+"\nGREETING='hello world'\nKEY=\"a\\nb\"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Apply("root", "shop"); !errors.Is(err, ErrLocked) {
		t.Fatalf("apply while locked: %v", err)
	}
	v.unlock(t)
	if _, err := v.Apply("root", "shop"); !errors.Is(err, ErrNoTarget) {
		t.Fatalf("apply without a target: %v", err)
	}
	if _, err := v.Apply("root", "missing"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("apply of a missing app: %v", err)
	}
	if _, err := v.SetTarget("root", "shop", target, ""); err != nil {
		t.Fatal(err)
	}

	// First apply: no older file, no container.
	result, err := v.Apply("root", "shop")
	if err != nil || result.Backup || result.Restarted || result.Variables != 3 || result.EnvFile != target {
		t.Fatalf("first apply: %+v %v", result, err)
	}
	want := "TOKEN=" + testValue + "\nGREETING='hello world'\nKEY=\"a\\nb\"\n"
	if raw, _ := os.ReadFile(target); string(raw) != want {
		t.Fatalf("written file:\n%s", raw)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode of the written file: %o", info.Mode().Perm())
	}
	if _, err := os.Stat(target + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a backup was made of a file that did not exist")
	}
	if len(v.restarter.calls) != 0 {
		t.Fatalf("restart without a container: %v", v.restarter.calls)
	}

	// Second apply: the older file, here world-readable, is kept as .bak
	// 0600, and the container is restarted after the write.
	if err := os.WriteFile(target, []byte("OLD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetTarget("root", "shop", target, "shop-web"); err != nil {
		t.Fatal(err)
	}
	result, err = v.Apply("alice", "shop")
	if err != nil || !result.Backup || !result.Restarted || result.RestartError != "" || result.Container != "shop-web" {
		t.Fatalf("second apply: %+v %v", result, err)
	}
	if raw, _ := os.ReadFile(target + ".bak"); string(raw) != "OLD=1\n" {
		t.Fatalf("backup: %q", raw)
	}
	for _, path := range []string{target, target + ".bak"} {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %o", path, info.Mode().Perm())
		}
	}
	if raw, _ := os.ReadFile(target); string(raw) != want {
		t.Fatalf("written file:\n%s", raw)
	}
	if len(v.restarter.calls) != 1 || v.restarter.calls[0] != "alice:shop-web" {
		t.Fatalf("restarts: %v", v.restarter.calls)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}

	// A failed restart is reported; the file is written all the same.
	v.restarter.err = errors.New("conteneur introuvable")
	result, err = v.Apply("root", "shop")
	if err != nil || result.Restarted || result.RestartError != "conteneur introuvable" {
		t.Fatalf("failed restart: %+v %v", result, err)
	}
	entries, _ := v.Audit(2)
	if entries[0].Action != "restart_failed" || entries[0].Detail != "shop-web" || entries[1].Action != "apply" ||
		!strings.Contains(entries[1].Detail, target) || strings.Contains(entries[1].Detail, testValue) {
		t.Fatalf("audit of an apply: %+v", entries)
	}

	// Once locked again, nothing is written and nothing restarts.
	v.Lock("root")
	if err := os.WriteFile(target, []byte("UNTOUCHED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(v.restarter.calls)
	if _, err := v.Apply("root", "shop"); !errors.Is(err, ErrLocked) {
		t.Fatalf("apply after lock: %v", err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != "UNTOUCHED=1\n" || len(v.restarter.calls) != before {
		t.Fatal("a locked vault wrote or restarted")
	}
}

func TestApplyWithoutRestarter(t *testing.T) {
	v := newTestVault(t)
	v.Vault.restarter = nil
	v.ready(t, "shop")
	target := filepath.Join(mkdir(t, v.home, "shop"), ".env")
	if _, err := v.SetVar("root", "shop", "A", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetTarget("root", "shop", target, "web"); err != nil {
		t.Fatal(err)
	}
	v.unlock(t)
	result, err := v.Apply("root", "shop")
	if err != nil || result.Restarted || result.RestartError == "" {
		t.Fatalf("apply without a restarter: %+v %v", result, err)
	}
}

// The target is checked again at apply time: a directory or a file replaced
// by a link after the target was saved is refused, and nothing is written.
func TestApplyChecksTheTargetAgain(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := mkdir(t, v.home, "apps", "shop")
	target := filepath.Join(dir, ".env")
	if _, err := v.SetVar("root", "shop", "TOKEN", testValue); err != nil {
		t.Fatal(err)
	}
	if _, err := v.SetTarget("root", "shop", target, "web"); err != nil {
		t.Fatal(err)
	}
	v.unlock(t)
	victim := filepath.Join(outside, "victim.env")
	if err := os.WriteFile(victim, []byte("VICTIM=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var validation *ValidationError

	// The file becomes a link to a file outside the home.
	if err := os.Symlink(victim, target); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Apply("root", "shop"); !errors.As(err, &validation) {
		t.Fatalf("file swapped for a link: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	// The directory becomes a link to a directory outside the home.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Apply("root", "shop"); !errors.As(err, &validation) {
		t.Fatalf("directory swapped for a link: %v", err)
	}
	if raw, _ := os.ReadFile(victim); string(raw) != "VICTIM=1\n" {
		t.Fatalf("the file outside the home was written: %q", raw)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatalf("files were created outside the home: %v", entries)
	}
	if len(v.restarter.calls) != 0 {
		t.Fatalf("a refused apply restarted a container: %v", v.restarter.calls)
	}
}
