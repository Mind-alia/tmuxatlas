package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Signal is one problem seen by an evaluation. The engine turns signals into
// notifications; a key absent from an evaluation means the problem is gone.
type Signal struct {
	Key      string
	Severity string // critical | warning
	Title    string
	Detail   string
	// Pending is how long the problem must last before anyone is told.
	Pending time.Duration
}

// Notification is what a Notifier sends.
type Notification struct {
	Key      string    `json:"key"`
	Kind     string    `json:"kind"` // firing | reminder | resolved | group | test
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
	// Keys lists the alerts a grouped notification stands for.
	Keys []string `json:"keys,omitempty"`
}

// Notifier delivers notifications to a phone.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, n Notification) error
}

// Policy holds the anti-spam settings.
type Policy struct {
	ResolveAfterOK int           // consecutive clear evaluations before "resolved"
	MinInterval    time.Duration // between two notifications of one alert
	Reminder       time.Duration // repeat while an alert keeps firing
	GroupAbove     int           // more due notifications than this in one round are grouped
	MaxPerWindow   int           // global cap...
	Window         time.Duration // ...over this window
}

// DefaultPolicy follows docs/ops.md.
var DefaultPolicy = Policy{
	ResolveAfterOK: 2,
	MinInterval:    5 * time.Minute,
	Reminder:       6 * time.Hour,
	GroupAbove:     3,
	MaxPerWindow:   10,
	Window:         10 * time.Minute,
}

// Alert phases: ok (no state) → pending → firing → resolving → ok.
const (
	PhasePending   = "pending"
	PhaseFiring    = "firing"
	PhaseResolving = "resolving"
)

// AlertState is the engine's memory of one alert, persisted in alerts.json.
type AlertState struct {
	Key      string    `json:"key"`
	Severity string    `json:"severity"`
	Title    string    `json:"title"`
	Detail   string    `json:"detail,omitempty"`
	Phase    string    `json:"phase"`
	Since    time.Time `json:"since"`
	FiredAt  time.Time `json:"fired_at,omitempty"`
	LastSent time.Time `json:"last_sent,omitempty"`
	// Notified is true once the firing notification went out, so a recovery
	// is only announced for problems that were announced.
	Notified bool   `json:"notified"`
	OKCount  int    `json:"ok_count,omitempty"`
	Due      string `json:"due,omitempty"` // firing | reminder | resolved, waiting for the throttle
}

type persistedAlerts struct {
	Version int                    `json:"version"`
	States  map[string]*AlertState `json:"states"`
	Sent    []time.Time            `json:"sent"`
	Recent  []Notification         `json:"recent"`
}

// Engine decides which notifications to send. It is safe for concurrent use.
type Engine struct {
	mu        sync.Mutex
	policy    Policy
	notifiers []Notifier
	now       func() time.Time
	path      string
	logger    *logrus.Entry

	states map[string]*AlertState
	sent   []time.Time
	recent []Notification
	// lastByKey keeps the throttle across a resolved-then-firing-again cycle,
	// when the state itself was deleted.
	lastByKey map[string]time.Time
}

// NewEngine returns an engine persisting to path ("" disables persistence).
func NewEngine(path string, policy Policy, notifiers ...Notifier) *Engine {
	e := &Engine{policy: policy, notifiers: notifiers, now: time.Now, path: path,
		logger: logrus.WithField("component", "ops-alerting"), states: map[string]*AlertState{}, lastByKey: map[string]time.Time{}}
	e.load()
	return e
}

// Notifiers lists the configured channels.
func (e *Engine) Notifiers() []string {
	names := make([]string, 0, len(e.notifiers))
	for _, n := range e.notifiers {
		names = append(names, n.Name())
	}
	return names
}

// Evaluate applies one round of signals and sends what is due.
func (e *Engine) Evaluate(ctx context.Context, signals []Signal) {
	e.mu.Lock()
	out := e.step(signals)
	e.save()
	e.mu.Unlock()
	for _, n := range out {
		e.deliver(ctx, n)
	}
}

// step updates the states and returns the notifications to send now. The
// caller holds e.mu.
func (e *Engine) step(signals []Signal) []Notification {
	now := e.now()
	p := e.policy
	active := make(map[string]Signal, len(signals))
	for _, s := range signals {
		active[s.Key] = s
	}

	for key, s := range active {
		st, ok := e.states[key]
		if !ok {
			st = &AlertState{Key: key, Phase: PhasePending, Since: now}
			e.states[key] = st
		}
		st.Severity, st.Title, st.Detail = s.Severity, s.Title, s.Detail
		st.OKCount = 0
		switch st.Phase {
		case PhasePending:
			if now.Sub(st.Since) >= s.Pending {
				st.Phase, st.FiredAt, st.Due = PhaseFiring, now, "firing"
			}
		case PhaseResolving:
			st.Phase = PhaseFiring // flapping back: no new notification
			if st.Due == "resolved" {
				st.Due = ""
			}
		case PhaseFiring:
			if st.Due == "" && st.Notified && now.Sub(st.LastSent) >= p.Reminder {
				st.Due = "reminder"
			}
		}
	}
	for key, st := range e.states {
		if _, ok := active[key]; ok {
			continue
		}
		switch st.Phase {
		case PhasePending:
			delete(e.states, key)
			continue
		case PhaseFiring:
			st.Phase, st.OKCount = PhaseResolving, 0
			if st.Due == "firing" || st.Due == "reminder" {
				st.Due = "" // never announced, or no need to remind
			}
		}
		if st.Phase == PhaseResolving && st.Due != "resolved" {
			st.OKCount++
			if st.OKCount >= p.ResolveAfterOK {
				if st.Notified {
					st.Due = "resolved"
				} else {
					delete(e.states, key)
				}
			}
		}
	}

	// Throttle per alert, then apply the global cap and grouping.
	for key, at := range e.lastByKey {
		if now.Sub(at) >= p.MinInterval {
			delete(e.lastByKey, key)
		}
	}
	var due []*AlertState
	for _, st := range e.states {
		last := st.LastSent
		if at, ok := e.lastByKey[st.Key]; ok && at.After(last) {
			last = at
		}
		if st.Due != "" && (last.IsZero() || now.Sub(last) >= p.MinInterval) {
			due = append(due, st)
		}
	}
	if len(due) == 0 {
		return nil
	}
	sort.Slice(due, func(i, j int) bool {
		if severityRank[due[i].Severity] != severityRank[due[j].Severity] {
			return severityRank[due[i].Severity] < severityRank[due[j].Severity]
		}
		return due[i].Key < due[j].Key
	})
	e.pruneSent(now)
	budget := p.MaxPerWindow - len(e.sent)
	if budget <= 0 {
		e.logger.WithField("due", len(due)).Warn("plafond de notifications atteint, envoi différé")
		return nil
	}
	var out []Notification
	if len(due) > p.GroupAbove || len(due) > budget {
		out = append(out, groupNotification(due, now))
	} else {
		for _, st := range due {
			out = append(out, notificationFor(st, now))
		}
	}
	for _, st := range due {
		e.markSent(st, now)
	}
	for range out {
		e.sent = append(e.sent, now)
	}
	e.recent = append(e.recent, out...)
	if len(e.recent) > 50 {
		e.recent = e.recent[len(e.recent)-50:]
	}
	return out
}

func (e *Engine) markSent(st *AlertState, now time.Time) {
	st.LastSent = now
	e.lastByKey[st.Key] = now
	switch st.Due {
	case "firing", "reminder":
		st.Notified = true
	case "resolved":
		delete(e.states, st.Key)
	}
	st.Due = ""
}

func (e *Engine) pruneSent(now time.Time) {
	kept := e.sent[:0]
	for _, at := range e.sent {
		if now.Sub(at) < e.policy.Window {
			kept = append(kept, at)
		}
	}
	e.sent = kept
}

func notificationFor(st *AlertState, now time.Time) Notification {
	n := Notification{Key: st.Key, Kind: st.Due, Severity: st.Severity, At: now, Title: st.Title, Body: st.Detail}
	switch st.Due {
	case "reminder":
		n.Title = "Toujours en panne : " + st.Title
		n.Body = fmt.Sprintf("Depuis %s. %s", human(int64(now.Sub(st.FiredAt).Seconds())), st.Detail)
	case "resolved":
		n.Severity = "info"
		n.Title = "Rétabli : " + st.Title
		n.Body = fmt.Sprintf("Après %s.", human(int64(now.Sub(st.FiredAt).Seconds())))
	}
	n.Body = strings.TrimSpace(n.Body)
	return n
}

func groupNotification(due []*AlertState, now time.Time) Notification {
	n := Notification{Key: "ops:group", Kind: "group", Severity: "info", At: now}
	var problems, resolved int
	var lines []string
	for _, st := range due {
		n.Keys = append(n.Keys, st.Key)
		prefix := "• "
		if st.Due == "resolved" {
			resolved++
			prefix = "✓ "
		} else {
			problems++
			if severityRank[st.Severity] < severityRank[n.Severity] {
				n.Severity = st.Severity
			}
		}
		if len(lines) < 8 {
			lines = append(lines, prefix+st.Title)
		}
	}
	switch {
	case problems > 0 && resolved > 0:
		n.Title = fmt.Sprintf("%d alertes, %d rétablissements", problems, resolved)
	case problems > 0:
		n.Title = fmt.Sprintf("%d alertes", problems)
	default:
		n.Title = fmt.Sprintf("%d alertes rétablies", resolved)
	}
	if len(due) > len(lines) {
		lines = append(lines, fmt.Sprintf("… et %d autres", len(due)-len(lines)))
	}
	n.Body = strings.Join(lines, "\n")
	return n
}

func (e *Engine) deliver(ctx context.Context, n Notification) {
	for _, notifier := range e.notifiers {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := notifier.Notify(ctx, n); err != nil {
			e.logger.WithError(err).WithField("channel", notifier.Name()).WithField("key", n.Key).Warn("envoi de notification")
		}
		cancel()
	}
}

// Test sends a test notification through every channel, outside the
// anti-spam rules, and returns one error per failed channel.
func (e *Engine) Test(ctx context.Context) map[string]string {
	n := Notification{Key: "ops:test", Kind: "test", Severity: "info", At: e.now(),
		Title: "Test des notifications Atlas", Body: "Si tu lis ceci, les alertes arriveront sur ce téléphone."}
	errs := map[string]string{}
	for _, notifier := range e.notifiers {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := notifier.Notify(ctx, n); err != nil {
			errs[notifier.Name()] = err.Error()
		}
		cancel()
	}
	return errs
}

// EngineView is the notifications part of GET /api/ops.
type EngineView struct {
	Channels []string       `json:"channels"`
	Alerts   []AlertState   `json:"alerts"`
	Recent   []Notification `json:"recent"`
}

// View returns the current states and the last notifications, newest first.
func (e *Engine) View() EngineView {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := EngineView{Channels: e.Notifiers(), Alerts: []AlertState{}, Recent: []Notification{}}
	for _, st := range e.states {
		v.Alerts = append(v.Alerts, *st)
	}
	sort.Slice(v.Alerts, func(i, j int) bool { return v.Alerts[i].Since.Before(v.Alerts[j].Since) })
	for i := len(e.recent) - 1; i >= 0 && len(v.Recent) < 20; i-- {
		v.Recent = append(v.Recent, e.recent[i])
	}
	return v
}

func (e *Engine) save() {
	if e.path == "" {
		return
	}
	data, err := json.Marshal(persistedAlerts{Version: 1, States: e.states, Sent: e.sent, Recent: e.recent})
	if err != nil {
		return
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		e.logger.WithError(err).Debug("persist alerts")
		return
	}
	_ = os.Rename(tmp, e.path)
}

func (e *Engine) load() {
	if e.path == "" {
		return
	}
	data, err := os.ReadFile(e.path)
	if err != nil {
		return
	}
	var p persistedAlerts
	if json.Unmarshal(data, &p) != nil || p.Version != 1 {
		return
	}
	for k, st := range p.States {
		if st != nil && st.Key == k {
			e.states[k] = st
		}
	}
	e.sent, e.recent = p.Sent, p.Recent
}

// DefaultAlertsPath is alerts.json in the data directory.
func DefaultAlertsPath(dataDir string) string { return filepath.Join(dataDir, "alerts.json") }
