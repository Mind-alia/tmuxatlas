// Package sudotermtest provides a fake sudo for tests. It never grants
// privileges: it reads a line on /dev/tty without echo, like sudo does, then
// runs the command as the test user.
package sudotermtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Script is the fake sudo. FAKE_SUDO_NOPASSWD=1 makes "sudo -n" succeed.
const Script = `#!/bin/sh
prompt="[sudo] password: "
nonint=0
while [ $# -gt 0 ]; do
  case "$1" in
    -k) shift ;;
    -n) nonint=1; shift ;;
    -p) prompt="$2"; shift 2 ;;
    --) shift; break ;;
    *) break ;;
  esac
done
[ $# -eq 0 ] && exit 0
if [ "$nonint" = 1 ]; then
  [ "${FAKE_SUDO_NOPASSWD:-0}" = 1 ] && exec "$@"
  echo "sudo: a password is required" >&2
  exit 1
fi
printf '%s' "$prompt" > /dev/tty
stty -echo < /dev/tty
IFS= read -r typed < /dev/tty
stty echo < /dev/tty
printf '\n' > /dev/tty
[ -n "$typed" ] || { echo "sudo: no input" >&2; exit 1; }
exec "$@"
`

// TypedLine stands for what a human types at the prompt. It is a fixed test
// string, not a credential.
const TypedLine = "frappe-humaine-1234"

// Install puts the fake first in PATH and fails the test unless "sudo"
// resolves to it: tests must never reach the real binary.
func Install(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sudo")
	if err := os.WriteFile(path, []byte(Script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	resolved, err := exec.LookPath("sudo")
	if err != nil || resolved != path {
		t.Fatalf("sudo resolves to %q, not the fake %q: refusing to run", resolved, path)
	}
	return path
}
