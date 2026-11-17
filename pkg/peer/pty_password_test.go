package peer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty/v2"
	"golang.org/x/sys/unix"
)

func setEcho(t *testing.T, fd int, echo, canonical bool) {
	t.Helper()
	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	termios.Lflag &^= unix.ECHO | unix.ICANON
	if echo {
		termios.Lflag |= unix.ECHO
	}
	if canonical {
		termios.Lflag |= unix.ICANON
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, termios); err != nil {
		t.Fatal(err)
	}
}

func TestWatchPasswordMode(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skip("no PTY: ", err)
	}
	defer master.Close()
	defer slave.Close()
	manager := &PTYManager{paneTTY: func(string, string) (string, error) { return slave.Name(), nil }}

	var mu sync.Mutex
	var reports []bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go manager.watchPasswordMode(ctx, "claude", func(off bool) error {
		mu.Lock()
		reports = append(reports, off)
		mu.Unlock()
		return nil
	})
	waitReports := func(n int) []bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := append([]bool(nil), reports...)
			mu.Unlock()
			if len(got) >= n {
				return got
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("got %d reports, want %d", len(reports), n)
		return nil
	}

	fd := int(slave.Fd())
	setEcho(t, fd, false, false) // raw mode, like a TUI: not a password prompt
	time.Sleep(700 * time.Millisecond)
	mu.Lock()
	if len(reports) != 0 {
		t.Fatalf("raw mode reported as a password prompt: %v", reports)
	}
	mu.Unlock()
	setEcho(t, fd, false, true) // canonical without echo: password prompt
	if got := waitReports(1); !got[0] {
		t.Fatalf("reports = %v", got)
	}
	setEcho(t, fd, true, true)
	if got := waitReports(2); got[1] {
		t.Fatalf("reports = %v", got)
	}
}
