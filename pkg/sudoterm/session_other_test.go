//go:build !linux

package sudoterm

import (
	"errors"
	"testing"
)

// Without a way to list a session's processes, the runner refuses to start.
func TestRunnerRefusesWithoutSessionKill(t *testing.T) {
	if _, err := NewRunner(Config{}); !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrRoot) {
		t.Fatalf("NewRunner on this platform: %v, want ErrUnsupported", err)
	}
}
