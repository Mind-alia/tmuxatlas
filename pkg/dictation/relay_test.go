package dictation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTmux plays a hidden Claude: launcher menu, trust dialog, then its prompt.
type fakeTmux struct {
	mu       sync.Mutex
	person   string
	exists   bool
	stage    string // menu, trust, ready, noaccount
	prompt   string
	spaces   int
	heard    string // the transcript Claude writes once the space bar is released
	commands []string
}

func (f *fakeTmux) run(_ context.Context, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if args[0] != "capture-pane" && !(args[0] == "send-keys" && args[len(args)-1] == "Space") {
		f.commands = append(f.commands, strings.Join(args, " "))
	}
	switch args[0] {
	case "show-options":
		return f.person + "\n", nil
	case "list-sessions":
		return sessionName("Aris") + "\ntmuxatlas-dictation-old-12345678\nwork\n", nil
	case "has-session":
		if !f.exists {
			return "", errors.New("no session")
		}
	case "new-session":
		f.exists = true
	case "kill-session":
		f.exists = false
	case "send-keys":
		switch key := args[len(args)-1]; {
		case key == "Enter" && f.stage == "menu":
			f.stage = "trust"
		case key == "Enter" && f.stage == "trust":
			f.stage = "ready"
		case key == "Space":
			f.spaces++
		case key == "C-u":
			f.prompt = ""
		}
	case "capture-pane":
		if f.spaces > 0 && f.heard != "" {
			f.prompt, f.heard = f.heard, "" // shown once the holds stop being counted
		}
		switch f.stage {
		case "menu":
			return "claude · Session : " + f.person + "\n  1. " + f.person + "  (Entrée)\nChoix : ", nil
		case "trust":
			return ruleRow + "\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel", nil
		case "noaccount":
			return f.person + " n'a pas encore de compte claude : connexion.", nil
		}
		if f.prompt == "" {
			return claudeScreen(placeholder), nil
		}
		return claudeScreen("\x1b[39m❯ " + f.prompt), nil
	}
	return "", nil
}

func (f *fakeTmux) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, command := range f.commands {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func newTestRelay(t *testing.T, fake *fakeTmux) (*Relay, *[]string) {
	t.Helper()
	var routed []string
	relay := New("tmux", t.TempDir(), func(session, mic string) { routed = append(routed, session+" "+mic) })
	relay.run = fake.run
	relay.poll, relay.holdEvery, relay.partialEvery, relay.settle = time.Millisecond, time.Millisecond, time.Millisecond, 0
	relay.emptyGrace, relay.startTimeout, relay.finishTimeout = 20*time.Millisecond, time.Second, time.Second
	return relay, &routed
}

func TestRelayTranscribesWithTheSessionPersonsClaude(t *testing.T) {
	fake := &fakeTmux{person: "Novanns2(Camille)", stage: "menu", heard: "Lance les tests du mur"}
	relay, routed := newTestRelay(t, fake)
	take, err := relay.Start(context.Background(), "mcp", "tab-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	name := sessionName("Novanns2(Camille)")
	// tmux 3.4 resolves an option's "=name" target only as "=name:".
	if !fake.ran("set-option -t ="+name+": @ai-person Novanns2(Camille)") || !fake.ran("show-options -t =mcp: -qv @ai-person") {
		t.Fatalf("the hidden Claude must run with the session's person: %v", fake.commands)
	}
	if len(*routed) != 1 || (*routed)[0] != name+" tab-1" {
		t.Fatalf("the tab's microphone must reach the hidden Claude: %v", *routed)
	}
	time.Sleep(10 * time.Millisecond)
	text, err := take.Finish(context.Background())
	if err != nil || text != "Lance les tests du mur" {
		t.Fatalf("transcript = %q, %v", text, err)
	}
	if fake.prompt != "" || fake.spaces == 0 {
		t.Fatalf("prompt %q left, %d spaces held", fake.prompt, fake.spaces)
	}
	if _, err := take.Finish(context.Background()); !errors.Is(err, ErrEnded) {
		t.Fatalf("second Finish: %v", err)
	}
}

func TestRelayHeardNothing(t *testing.T) {
	fake := &fakeTmux{person: "Aris2", stage: "ready", exists: true}
	relay, _ := newTestRelay(t, fake)
	take, err := relay.Start(context.Background(), "mcp", "tab-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fake.ran("new-session") {
		t.Fatal("a running hidden Claude is reused")
	}
	if text, err := take.Finish(context.Background()); text != "" || err != nil {
		t.Fatalf("got %q, %v", text, err)
	}
}

func TestRelayRunsOneDictationPerPerson(t *testing.T) {
	fake := &fakeTmux{person: "Aris2", stage: "ready", exists: true}
	relay, _ := newTestRelay(t, fake)
	first, err := relay.Start(context.Background(), "mcp", "tab-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Start(context.Background(), "other", "tab-2", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second dictation: %v", err)
	}
	if _, err := first.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := relay.Start(context.Background(), "other", "tab-2", nil)
	if err != nil {
		t.Fatal(err)
	}
	second.Cancel()
}

func TestRelayReportsAMissingAccount(t *testing.T) {
	fake := &fakeTmux{person: "Novanns", stage: "noaccount"}
	relay, _ := newTestRelay(t, fake)
	_, err := relay.Start(context.Background(), "mcp", "tab-1", nil)
	if err == nil || !strings.Contains(err.Error(), "Novanns n’a pas de compte Claude") {
		t.Fatalf("err = %v", err)
	}
	if fake.exists {
		t.Fatal("the failed hidden session must be removed")
	}
}

// A fixed person transcribes every dictation, whatever the session's person.
func TestRelayFixedPerson(t *testing.T) {
	fake := &fakeTmux{person: "Novanns2(Camille)", stage: "ready", exists: true}
	relay, routed := newTestRelay(t, fake)
	relay.Person = "Aris"
	take, err := relay.Start(context.Background(), "mcp", "tab-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer take.Cancel()
	if fake.ran("show-options") || (*routed)[0] != sessionName("Aris")+" tab-1" {
		t.Fatalf("commands %v, routed %v", fake.commands, *routed)
	}
}

// The fixed Person's hidden Claude starts ahead of time and survives a restart.
func TestRelayWarmsAndKeepsTheFixedPersonsClaude(t *testing.T) {
	fake := &fakeTmux{stage: "ready"}
	relay, _ := newTestRelay(t, fake)
	relay.Person = "Aris"
	relay.Warm(context.Background())
	if !fake.ran("new-session -d -s " + sessionName("Aris")) {
		t.Fatalf("not warmed: %v", fake.commands)
	}
	relay.Prune(context.Background())
	if fake.ran("kill-session -t ="+sessionName("Aris")) || !fake.ran("kill-session -t =tmuxatlas-dictation-old-12345678") || fake.ran("kill-session -t =work") {
		t.Fatalf("prune must end old hidden sessions only: %v", fake.commands)
	}
	relay.mu.Lock()
	idle := relay.instances[sessionName("Aris")].idle
	relay.mu.Unlock()
	if idle != nil {
		t.Fatal("the fixed Person's Claude must not stop when idle")
	}
}

// While the person speaks, the transcript Claude shows reaches the browser.
func TestRelayReportsTheTranscriptAsItComes(t *testing.T) {
	fake := &fakeTmux{person: "Aris", stage: "ready", exists: true, heard: "Lance les tests"}
	relay, _ := newTestRelay(t, fake)
	partials := make(chan string, 16)
	take, err := relay.Start(context.Background(), "mcp", "tab-1", func(text string) { partials <- text })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-partials:
		if text != "Lance les tests" {
			t.Fatalf("partial = %q", text)
		}
	case <-time.After(time.Second):
		t.Fatal("no partial transcript")
	}
	if _, err := take.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
}
