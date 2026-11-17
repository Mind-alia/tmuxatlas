// Package dictation lends Claude Code's dictation to agents without one, such as
// Codex. For the person of the session being dictated to, a hidden tmux session
// runs Claude; the browser tab's microphone is routed to it, its hold-to-talk
// space bar is held, and the transcript it writes in its prompt is read, cleared
// and handed back. Nothing is ever submitted there, so Claude runs no turn.
package dictation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// Errors shown to the person dictating.
var (
	ErrBusy          = errors.New("Une dictée est déjà en cours pour ce compte.")
	ErrStartup       = errors.New("Claude n’a pas démarré pour la dictée.")
	ErrChooseAccount = errors.New("Choisissez un compte pour cette session : lancez claude une fois dans son terminal.")
	ErrFailed        = errors.New("Claude n’a pas pu transcrire.")
	ErrTimeout       = errors.New("Claude n’a pas terminé la transcription.")
	ErrEnded         = errors.New("Cette dictée est déjà terminée.")
)

// Runner runs a command and returns its standard output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// Relay keeps one hidden Claude per person and runs their dictations.
type Relay struct {
	// Person, when set, is the account of every dictation, whatever the
	// session's own person.
	Person string

	tmux       string
	dir        string
	command    string
	useSession func(session, micID string)
	run        Runner
	sleep      func(time.Duration)
	now        func() time.Time

	poll          time.Duration
	partialEvery  time.Duration
	holdEvery     time.Duration
	holdLimit     time.Duration
	settle        time.Duration
	startTimeout  time.Duration
	finishTimeout time.Duration
	emptyGrace    time.Duration
	idleTimeout   time.Duration

	mu        sync.Mutex
	instances map[string]*instance
}

type instance struct {
	name string
	busy sync.Mutex
	idle *time.Timer
}

// New creates a Relay. dir is the working directory of the hidden sessions, and
// useSession routes a tab's microphone to a tmux session.
func New(tmuxPath, dir string, useSession func(session, micID string)) *Relay {
	return &Relay{
		tmux: tmuxPath, dir: dir, command: "claude", useSession: useSession,
		run: execRunner, sleep: time.Sleep, now: time.Now,
		poll: 100 * time.Millisecond, partialEvery: 150 * time.Millisecond, holdEvery: 40 * time.Millisecond, holdLimit: 10 * time.Minute,
		settle: 300 * time.Millisecond, startTimeout: time.Minute, finishTimeout: 30 * time.Second,
		emptyGrace: 3 * time.Second, idleTimeout: 30 * time.Minute,
		instances: make(map[string]*instance),
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// sessionName names the hidden session of a person: readable, and unique
// through a hash of the exact name.
func sessionName(person string) string {
	if person == "" {
		return tmux.DictationSessionPrefix + "default"
	}
	sum := sha256.Sum256([]byte(person))
	slug := strings.Trim(unsafeName.ReplaceAllString(person, "-"), "-")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return tmux.DictationSessionPrefix + slug + "-" + hex.EncodeToString(sum[:])[:8]
}

func (r *Relay) tmuxRun(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, r.tmux, args...)
}

func (r *Relay) capture(ctx context.Context, name string) screen {
	out, _ := r.tmuxRun(ctx, "capture-pane", "-p", "-e", "-t", "="+name+":")
	return parseScreen(out)
}

func (r *Relay) keys(ctx context.Context, name string, keys ...string) {
	_, _ = r.tmuxRun(ctx, append([]string{"send-keys", "-t", "=" + name + ":"}, keys...)...)
}

// Warm starts the fixed Person's hidden Claude ahead of the first dictation,
// so that it starts at once. It does nothing without a fixed Person.
func (r *Relay) Warm(ctx context.Context) {
	if r.Person == "" {
		return
	}
	inst := r.instance(sessionName(r.Person))
	if !inst.busy.TryLock() {
		return
	}
	if err := r.ensure(ctx, inst.name, r.Person); err != nil {
		logrus.WithError(err).Warn("dictation relay not warmed")
	}
	r.release(inst)
}

// Prune ends the hidden sessions left by a previous run, except the fixed
// Person's, which a restart reuses as it is.
func (r *Relay) Prune(ctx context.Context) {
	out, err := r.tmuxRun(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(out) {
		if strings.HasPrefix(name, tmux.DictationSessionPrefix) && (r.Person == "" || name != sessionName(r.Person)) {
			_, _ = r.tmuxRun(ctx, "kill-session", "-t", "="+name)
		}
	}
}

// Start begins a dictation for a tab working in session: the transcript will be
// Claude's, with the account of that session's person. partial, when set,
// receives the transcript as Claude writes it while the person speaks.
func (r *Relay) Start(ctx context.Context, session, micID string, partial func(string)) (*Take, error) {
	person := r.Person
	if person == "" {
		for _, opt := range []string{"@ai-person", "@ai-person-claude", "@ai-person-codex", "@ai-person-agy"} {
			val, _ := r.tmuxRun(ctx, "show-options", "-t", "="+session+":", "-qv", opt)
			if val = strings.TrimSpace(val); val != "" {
				person = val
				break
			}
		}
	}
	inst := r.instance(sessionName(person))
	if !inst.busy.TryLock() {
		return nil, ErrBusy
	}
	if err := r.ensure(ctx, inst.name, person); err != nil {
		r.release(inst)
		return nil, err
	}
	r.clear(ctx, inst.name)
	if r.useSession != nil {
		r.useSession(inst.name, micID)
	}
	take := &Take{relay: r, inst: inst, stop: make(chan struct{}), held: make(chan struct{}), watched: make(chan struct{})}
	go take.hold()
	go take.watch(partial)
	return take, nil
}

func (r *Relay) instance(name string) *instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.instances[name]
	if inst == nil {
		inst = &instance{name: name}
		r.instances[name] = inst
	}
	if inst.idle != nil {
		inst.idle.Stop()
	}
	return inst
}

// release lets the next dictation of this person run, and ends their hidden
// Claude once nobody has dictated for a while. The fixed Person's stays ready.
func (r *Relay) release(inst *instance) {
	r.mu.Lock()
	if inst.idle != nil {
		inst.idle.Stop()
		inst.idle = nil
	}
	if r.Person != "" && inst.name == sessionName(r.Person) {
		r.mu.Unlock()
		inst.busy.Unlock()
		return
	}
	inst.idle = time.AfterFunc(r.idleTimeout, func() {
		if !inst.busy.TryLock() {
			return
		}
		defer inst.busy.Unlock()
		_, _ = r.tmuxRun(context.Background(), "kill-session", "-t", "="+inst.name)
	})
	r.mu.Unlock()
	inst.busy.Unlock()
}

// ensure starts the person's hidden Claude if needed and waits for its prompt,
// answering the launcher's account menu and Claude's folder trust dialog.
func (r *Relay) ensure(ctx context.Context, name, person string) error {
	if _, err := r.tmuxRun(ctx, "has-session", "-t", "="+name); err != nil {
		if err := os.MkdirAll(r.dir, 0o700); err != nil {
			return err
		}
		if _, err := r.tmuxRun(ctx, "new-session", "-d", "-s", name, "-x", "250", "-y", "40", "-c", r.dir); err != nil {
			return fmt.Errorf("%w (%v)", ErrStartup, err)
		}
		if person != "" {
			_, _ = r.tmuxRun(ctx, "set-option", "-t", "="+name+":", "@ai-person", person)
		}
		if _, err := r.tmuxRun(ctx, "respawn-pane", "-k", "-t", "="+name+":", "-c", r.dir, r.command); err != nil {
			r.kill(ctx, name)
			return fmt.Errorf("%w (%v)", ErrStartup, err)
		}
	}
	deadline := r.now().Add(r.startTimeout)
	var menuAnswered, trustAnswered bool
	for {
		switch r.capture(ctx, name).startup() {
		case startupReady:
			return nil
		case startupMenu:
			if !menuAnswered {
				menuAnswered = true
				r.keys(ctx, name, "Enter") // the session's person
			}
		case startupMenuWithoutDefault:
			r.kill(ctx, name)
			return ErrChooseAccount
		case startupNoAccount:
			r.kill(ctx, name)
			if person == "" {
				return ErrChooseAccount
			}
			return fmt.Errorf("%s n’a pas de compte Claude pour la dictée.", person)
		case startupTrust:
			if !trustAnswered {
				// The folder is TmuxAtlas's own, empty one.
				trustAnswered = true
				r.keys(ctx, name, "Down")
				r.sleep(200 * time.Millisecond)
				r.keys(ctx, name, "Enter")
			}
		}
		if r.now().After(deadline) || ctx.Err() != nil {
			r.kill(ctx, name)
			return ErrStartup
		}
		r.sleep(3 * r.poll)
	}
}

func (r *Relay) kill(ctx context.Context, name string) {
	_, _ = r.tmuxRun(context.WithoutCancel(ctx), "kill-session", "-t", "="+name)
}

// clear empties the hidden prompt, so no transcript reaches the next dictation.
func (r *Relay) clear(ctx context.Context, name string) {
	for range 5 {
		if r.capture(ctx, name).prompt() == "" {
			return
		}
		r.keys(ctx, name, "C-u")
		r.sleep(50 * time.Millisecond)
	}
	logrus.WithField("session", name).Warn("dictation prompt not cleared")
}

// Take is one dictation in progress.
type Take struct {
	relay   *Relay
	inst    *instance
	once    sync.Once
	stop    chan struct{}
	held    chan struct{}
	watched chan struct{}
	ended   sync.Once
}

// watch reports the transcript Claude shows in its prompt while it listens.
func (t *Take) watch(partial func(string)) {
	defer close(t.watched)
	if partial == nil {
		return
	}
	ticker := time.NewTicker(t.relay.partialEvery)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
		}
		if text := t.relay.capture(context.Background(), t.inst.name).prompt(); text != last {
			last = text
			partial(text)
		}
	}
}

// hold repeats the space bar, which Claude treats as held while it repeats.
func (t *Take) hold() {
	defer close(t.held)
	ticker := time.NewTicker(t.relay.holdEvery)
	defer ticker.Stop()
	limit := time.After(t.relay.holdLimit)
	for {
		t.relay.keys(context.Background(), t.inst.name, "Space")
		select {
		case <-t.stop:
			return
		case <-limit:
			return
		case <-ticker.C:
		}
	}
}

func (t *Take) release() {
	t.once.Do(func() { close(t.stop) })
	<-t.held
	<-t.watched
}

// Finish stops recording and returns the transcript, or "" when Claude heard
// nothing. The hidden prompt is cleared either way. Only the first call to
// Finish or Cancel ends the dictation; later ones return ErrEnded.
func (t *Take) Finish(ctx context.Context) (text string, err error) {
	err = ErrEnded
	t.ended.Do(func() { text, err = t.finish(ctx) })
	return text, err
}

func (t *Take) finish(ctx context.Context) (string, error) {
	t.release()
	r, name := t.relay, t.inst.name
	defer r.release(t.inst)
	defer r.clear(context.WithoutCancel(ctx), name)
	r.sleep(r.settle)
	deadline := r.now().Add(r.finishTimeout)
	emptySince := r.now()
	last, stable := "", 0
	for {
		current := r.capture(ctx, name)
		text := current.prompt()
		switch status := current.voice(); {
		case status == voiceFailed:
			return "", ErrFailed
		case status != voiceIdle:
			emptySince, stable = r.now(), 0
		case text == "":
			if r.now().Sub(emptySince) >= r.emptyGrace {
				return "", nil
			}
		case text == last:
			// The transcript is complete once it stops changing.
			if stable++; stable >= 2 {
				return text, nil
			}
		default:
			last, stable = text, 0
		}
		if r.now().After(deadline) || ctx.Err() != nil {
			return "", ErrTimeout
		}
		r.sleep(r.poll)
	}
}

// Cancel stops recording and drops the transcript.
func (t *Take) Cancel() {
	go func() { _, _ = t.Finish(context.Background()) }()
}
