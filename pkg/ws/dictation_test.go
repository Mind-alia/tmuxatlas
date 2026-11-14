package ws

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeTake struct {
	text      string
	err       error
	cancelled chan struct{}
}

func (f *fakeTake) Finish(context.Context) (string, error) { return f.text, f.err }
func (f *fakeTake) Cancel()                                { close(f.cancelled) }

type syncBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func dictate(t *testing.T, take *fakeTake, submit bool) (*syncBuffer, []map[string]string) {
	t.Helper()
	handler := &PTYTerminalHandler{StartDictation: func(context.Context, string, string, func(string)) (DictationTake, error) {
		return take, nil
	}}
	replies := make(chan map[string]string, 4)
	send := func(value any) { replies <- value.(map[string]string) }
	pty := &syncBuffer{}
	state := &dictationState{}
	state.handle(handler, resizeMsg{Type: "dictation", Action: "start"}, "mcp", "tab-1", pty, send)
	got := []map[string]string{<-replies}
	state.handle(handler, resizeMsg{Type: "dictation", Action: "stop", Submit: submit}, "mcp", "tab-1", pty, send)
	got = append(got, <-replies)
	return pty, got
}

func TestDictationTypesTheTranscriptInThePane(t *testing.T) {
	pty, replies := dictate(t, &fakeTake{text: "Lance les tests\x1b[A"}, true)
	if replies[0]["error"] != "" || replies[1]["error"] != "" {
		t.Fatalf("replies: %v", replies)
	}
	if pty.String() != "Lance les tests[A\r" {
		t.Fatalf("pane got %q", pty.String())
	}
	pty, _ = dictate(t, &fakeTake{text: "À relire"}, false)
	if pty.String() != "À relire" {
		t.Fatalf("without submit, pane got %q", pty.String())
	}
}

func TestDictationTypesNothingWithoutATranscript(t *testing.T) {
	for _, take := range []*fakeTake{{}, {err: errors.New("Claude n’a pas pu transcrire.")}} {
		pty, replies := dictate(t, take, true)
		if pty.String() != "" || replies[1]["error"] == "" {
			t.Fatalf("pane got %q, replies %v", pty.String(), replies)
		}
	}
}

// A dictation that finishes starting after the Terminal closed must not keep recording.
func TestDictationStartedAfterCloseIsCancelled(t *testing.T) {
	release := make(chan struct{})
	take := &fakeTake{cancelled: make(chan struct{})}
	handler := &PTYTerminalHandler{StartDictation: func(context.Context, string, string, func(string)) (DictationTake, error) {
		<-release
		return take, nil
	}}
	replies := make(chan map[string]string, 1)
	state := &dictationState{}
	state.handle(handler, resizeMsg{Type: "dictation", Action: "start"}, "mcp", "tab-1", &syncBuffer{},
		func(value any) { replies <- value.(map[string]string) })
	state.cancel()
	close(release)
	select {
	case <-take.cancelled:
	case <-time.After(time.Second):
		t.Fatal("the late dictation kept recording")
	}
	if reply := <-replies; reply["error"] == "" {
		t.Fatalf("reply: %v", reply)
	}
}
