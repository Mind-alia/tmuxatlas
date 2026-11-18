package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/monitor"
)

type recorder struct {
	mu   sync.Mutex
	sent []Notification
}

func (r *recorder) Name() string { return "test" }
func (r *recorder) Notify(_ context.Context, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
	return nil
}
func (r *recorder) take() []Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestEngine(path string) (*Engine, *recorder, *clock) {
	rec := &recorder{}
	c := &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	e := NewEngine(path, DefaultPolicy, rec)
	e.now = c.now
	return e, rec, c
}

func kinds(ns []Notification) string {
	var out []string
	for _, n := range ns {
		out = append(out, n.Kind+":"+n.Key)
	}
	return strings.Join(out, ",")
}

var down = Signal{Key: "ops:container:web:down", Severity: "critical", Title: "Conteneur web arrêté", Pending: time.Minute}

func TestEngineLifecycle(t *testing.T) {
	e, rec, c := newTestEngine("")
	ctx := context.Background()
	round := func(signals ...Signal) string {
		e.Evaluate(ctx, signals)
		c.advance(30 * time.Second)
		return kinds(rec.take())
	}
	if got := round(down); got != "" {
		t.Fatalf("en attente : %s", got)
	}
	if got := round(down); got != "" {
		t.Fatalf("30 s : %s", got)
	}
	if got := round(down); got != "firing:ops:container:web:down" {
		t.Fatalf("60 s : %q", got)
	}
	for i := 0; i < 10; i++ {
		if got := round(down); got != "" {
			t.Fatalf("doublon : %s", got)
		}
	}
	if got := round(); got != "" {
		t.Fatalf("une seule vérification OK ne suffit pas : %s", got)
	}
	if got := round(down); got != "" {
		t.Fatalf("retour en panne pendant le rétablissement : %s", got)
	}
	round()
	if got := round(); got != "resolved:ops:container:web:down" {
		t.Fatalf("rétablissement : %q", got)
	}
	if v := e.View(); len(v.Alerts) != 0 || len(v.Recent) != 2 || v.Recent[0].Kind != "resolved" {
		t.Fatalf("vue : %+v", v)
	}
}

func TestEngineShortOutageIsSilent(t *testing.T) {
	e, rec, c := newTestEngine("")
	e.Evaluate(context.Background(), []Signal{down})
	c.advance(30 * time.Second)
	e.Evaluate(context.Background(), nil)
	c.advance(time.Minute)
	e.Evaluate(context.Background(), nil)
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("panne de 30 s notifiée : %s", kinds(got))
	}
}

func TestEngineReminderAndThrottle(t *testing.T) {
	e, rec, c := newTestEngine("")
	ctx := context.Background()
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	e.Evaluate(ctx, []Signal{failed})
	if got := kinds(rec.take()); got != "firing:ops:backup:failed" {
		t.Fatalf("immédiat : %q", got)
	}
	// resolved quickly, then failing again: the 5 min minimum holds the re-alert
	for _, signals := range [][]Signal{nil, nil, {failed}} {
		c.advance(30 * time.Second)
		e.Evaluate(ctx, signals)
	}
	if got := kinds(rec.take()); got != "" {
		t.Fatalf("intervalle minimal non respecté : %q", got)
	}
	c.advance(4 * time.Minute)
	e.Evaluate(ctx, []Signal{failed})
	if got := kinds(rec.take()); got != "" {
		t.Fatalf("le rétablissement n'a jamais été annoncé, la panne ne doit pas l'être deux fois : %q", got)
	}
	for i := 0; i < 6*120; i++ {
		c.advance(30 * time.Second)
		e.Evaluate(ctx, []Signal{failed})
	}
	got := rec.take()
	if len(got) < 1 || got[len(got)-1].Kind != "reminder" || !strings.HasPrefix(got[len(got)-1].Title, "Toujours en panne") {
		t.Fatalf("rappel après 6 h : %s", kinds(got))
	}
	if len(got) > 2 {
		t.Fatalf("trop de rappels : %s", kinds(got))
	}
}

func TestEngineGroupsAndCaps(t *testing.T) {
	e, rec, c := newTestEngine("")
	ctx := context.Background()
	var many []Signal
	for i := 0; i < 5; i++ {
		many = append(many, Signal{Key: fmt.Sprintf("ops:container:c%d:flapping", i), Severity: "warning", Title: fmt.Sprintf("c%d", i)})
	}
	e.Evaluate(ctx, many)
	got := rec.take()
	if len(got) != 1 || got[0].Kind != "group" || len(got[0].Keys) != 5 || got[0].Title != "5 alertes" {
		t.Fatalf("regroupement : %+v", got)
	}
	// the cap: 10 notifications per 10 minutes, whatever happens
	total := 1
	for i := 0; i < 39; i++ { // 39 × 15 s stays inside one 10 min window
		c.advance(15 * time.Second)
		key := fmt.Sprintf("ops:backup:x%d", i)
		e.Evaluate(ctx, append(many, Signal{Key: key, Severity: "critical", Title: key}))
		total += len(rec.take())
	}
	if total > 10 {
		t.Fatalf("%d notifications en 10 min", total)
	}
}

func TestEnginePersistsStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	e, rec, c := newTestEngine(path)
	e.Evaluate(context.Background(), []Signal{{Key: "ops:backup:late", Severity: "warning", Title: "en retard"}})
	if len(rec.take()) != 1 {
		t.Fatal("pas de notification")
	}
	restarted, rec2, _ := newTestEngine(path)
	restarted.now = func() time.Time { return c.t.Add(time.Minute) }
	restarted.Evaluate(context.Background(), []Signal{{Key: "ops:backup:late", Severity: "warning", Title: "en retard"}})
	if got := rec2.take(); len(got) != 0 {
		t.Fatalf("notification renvoyée après redémarrage : %s", kinds(got))
	}
}

// flaky is a channel that can be cut.
type flaky struct {
	recorder
	down     bool
	attempts int
}

func (f *flaky) Notify(ctx context.Context, n Notification) error {
	f.attempts++
	if f.down {
		return fmt.Errorf("réseau injoignable")
	}
	return f.recorder.Notify(ctx, n)
}

func newFlakyEngine() (*Engine, *flaky, *clock) {
	channel := &flaky{down: true}
	c := &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	e := NewEngine("", DefaultPolicy, channel)
	e.now = c.now
	return e, channel, c
}

func TestEngineRetriesUndeliveredNotification(t *testing.T) {
	e, channel, c := newFlakyEngine()
	ctx := context.Background()
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	e.Evaluate(ctx, []Signal{failed})
	if v := e.View(); channel.attempts != 1 || len(v.Alerts) != 1 || v.Alerts[0].Notified || v.Alerts[0].Due != "firing" || len(v.Recent) != 0 {
		t.Fatalf("un envoi refusé ne compte pas comme envoyé : %d essais, %+v", channel.attempts, v)
	}
	c.advance(30 * time.Second)
	e.Evaluate(ctx, []Signal{failed})
	if channel.attempts != 1 {
		t.Fatalf("nouvel essai avant le délai : %d essais", channel.attempts)
	}
	c.advance(10 * time.Minute)
	e.Evaluate(ctx, []Signal{failed})
	if channel.attempts != 2 {
		t.Fatalf("pas de nouvel essai dix minutes plus tard : %d essais", channel.attempts)
	}
	channel.down = false
	c.advance(2 * time.Minute)
	e.Evaluate(ctx, []Signal{failed})
	if got := kinds(channel.take()); got != "firing:ops:backup:failed" {
		t.Fatalf("après le retour du canal : %q", got)
	}
	if v := e.View(); !v.Alerts[0].Notified || v.Alerts[0].Due != "" || v.Alerts[0].Attempts != 0 || len(v.Recent) != 1 {
		t.Fatalf("état après livraison : %+v", v)
	}
	for i := 0; i < 20; i++ {
		c.advance(30 * time.Second)
		e.Evaluate(ctx, []Signal{failed})
	}
	if got := kinds(channel.take()); got != "" {
		t.Fatalf("doublon après livraison : %q", got)
	}
}

// The review case: the first delivery fails, a single clear evaluation
// follows, then the channel and the problem come back before the recovery is
// confirmed. The pending notification must keep its retry delay and count,
// go out exactly once, and no recovery may be announced meanwhile.
func TestEngineDeliversAlertThatReturnsBeforeRecoveryIsConfirmed(t *testing.T) {
	e, channel, c := newFlakyEngine()
	ctx := context.Background()
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	e.Evaluate(ctx, []Signal{failed}) // T+0: refused, next attempt at T+1 min
	if channel.attempts != 1 {
		t.Fatalf("%d essais au premier tour", channel.attempts)
	}
	c.advance(30 * time.Second)
	e.Evaluate(ctx, nil) // T+30 s: one clear evaluation, two are needed to confirm
	if v := e.View(); channel.attempts != 1 || len(v.Alerts) != 1 || v.Alerts[0].Due != "firing" || v.Alerts[0].Attempts != 1 {
		t.Fatalf("l'envoi en attente doit rester dû, avec son compte d'essais : %d essais, %+v", channel.attempts, v.Alerts)
	}
	channel.down = false
	c.advance(15 * time.Second)
	e.Evaluate(ctx, []Signal{failed}) // T+45 s: back, but the retry delay holds
	if channel.attempts != 1 {
		t.Fatalf("nouvel essai avant le délai : %d essais", channel.attempts)
	}
	c.advance(15 * time.Second)
	e.Evaluate(ctx, []Signal{failed}) // T+1 min: retry due
	// 24 h with the problem still there
	for i := 0; i < 2*24*60; i++ {
		c.advance(30 * time.Second)
		e.Evaluate(ctx, []Signal{failed})
	}
	sent := channel.take()
	if len(sent) == 0 || sent[0].Kind != "firing" {
		t.Fatalf("l'alerte initiale doit être livrée : %q", kinds(sent))
	}
	firing := 0
	for _, n := range sent {
		switch n.Kind {
		case "firing":
			firing++
		case "resolved":
			t.Fatalf("rétablissement annoncé alors que la panne est toujours là : %q", kinds(sent))
		}
	}
	if firing != 1 {
		t.Fatalf("l'alerte initiale doit partir une seule fois : %q", kinds(sent))
	}
	v := e.View()
	if !v.Alerts[0].Notified || v.Alerts[0].Phase != PhaseFiring {
		t.Fatalf("état après livraison : %+v", v.Alerts[0])
	}
	for _, n := range v.Recent {
		if n.Kind == "resolved" {
			t.Fatalf("l'historique contient un rétablissement prématuré : %+v", v.Recent)
		}
	}
}

// Still dropped when the recovery is confirmed before any delivery: a
// problem that was never announced has no recovery to announce.
func TestEngineDropsUndeliveredAlertOnceRecoveryIsConfirmed(t *testing.T) {
	e, channel, c := newFlakyEngine()
	ctx := context.Background()
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	e.Evaluate(ctx, []Signal{failed})
	channel.down = false
	for i := 0; i < 20; i++ {
		c.advance(30 * time.Second)
		e.Evaluate(ctx, nil)
	}
	if got := kinds(channel.take()); got != "" {
		t.Fatalf("rien ne doit partir pour une panne finie avant d'être annoncée : %q", got)
	}
	if v := e.View(); len(v.Alerts) != 0 {
		t.Fatalf("l'état doit disparaître : %+v", v.Alerts)
	}
}

func TestEngineGivesUpAfterBoundedRetries(t *testing.T) {
	e, channel, c := newFlakyEngine()
	ctx := context.Background()
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	for i := 0; i < 2*120; i++ { // two hours with the channel down
		e.Evaluate(ctx, []Signal{failed})
		c.advance(30 * time.Second)
	}
	if channel.attempts != DefaultPolicy.MaxAttempts {
		t.Fatalf("%d essais, %d attendus", channel.attempts, DefaultPolicy.MaxAttempts)
	}
	// still down and never announced: a new series starts one reminder later
	channel.down = false
	for i := 0; i < 5*120; i++ {
		e.Evaluate(ctx, []Signal{failed})
		c.advance(30 * time.Second)
	}
	if got := kinds(channel.take()); got != "firing:ops:backup:failed" {
		t.Fatalf("la panne jamais annoncée doit l'être au cycle suivant : %q", got)
	}
}

func TestEngineKeepsUndeliveredRecovery(t *testing.T) {
	e, channel, c := newFlakyEngine()
	ctx := context.Background()
	channel.down = false
	failed := Signal{Key: "ops:backup:failed", Severity: "critical", Title: "Sauvegarde en échec"}
	e.Evaluate(ctx, []Signal{failed})
	if got := kinds(channel.take()); got != "firing:ops:backup:failed" {
		t.Fatalf("panne : %q", got)
	}
	channel.down = true
	c.advance(6 * time.Minute)
	for i := 0; i < 3; i++ {
		e.Evaluate(ctx, nil)
		c.advance(30 * time.Second)
	}
	before := channel.attempts
	if v := e.View(); len(v.Alerts) != 1 || v.Alerts[0].Due != "resolved" || len(v.Recent) != 1 {
		t.Fatalf("le rétablissement non livré doit rester en attente : %+v", v)
	}
	channel.down = false
	for i := 0; i < 10 && len(e.View().Alerts) > 0; i++ {
		c.advance(time.Minute)
		e.Evaluate(ctx, nil)
	}
	if got := kinds(channel.take()); got != "resolved:ops:backup:failed" || channel.attempts <= before {
		t.Fatalf("rétablissement après le retour du canal : %q", got)
	}
	if v := e.View(); len(v.Alerts) != 0 || len(v.Recent) != 2 {
		t.Fatalf("vue finale : %+v", v)
	}
}

func TestSignals(t *testing.T) {
	resp := Response{Alerts: []Alert{
		{Key: "ops:containers:missing"}, {Key: "ops:backup:missing"},
		{Key: "ops:container:web:down"}, {Key: "ops:docker:down"}, {Key: "ops:container:db:flapping"}, {Key: "ops:backup:r2-failed"},
	}}
	got := map[string]time.Duration{}
	for _, s := range OpsSignals(resp) {
		got[s.Key] = s.Pending
	}
	want := map[string]time.Duration{"ops:container:web:down": time.Minute, "ops:docker:down": time.Minute, "ops:container:db:flapping": 0, "ops:backup:r2-failed": 0}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("signaux : %v", got)
	}
	point := func(up bool) monitor.ProbePoint { return monitor.ProbePoint{Up: up} }
	sites := SiteSignals([]monitor.Endpoint{
		{URL: "https://a.example/", Error: "timeout", History: []monitor.ProbePoint{point(true), point(false), point(false), point(false)}},
		{URL: "https://b.example/", History: []monitor.ProbePoint{point(false), point(false), point(true)}},
		{URL: "https://c.example/", History: []monitor.ProbePoint{point(false), point(false)}},
	})
	if len(sites) != 1 || sites[0].Key != "site:https://a.example/:down" || !strings.Contains(sites[0].Title, "a.example") || !strings.Contains(sites[0].Detail, "timeout") {
		t.Fatalf("sites : %+v", sites)
	}
}

func TestNtfyNotifier(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &got)
	}))
	defer srv.Close()
	for _, bad := range []string{"ftp://x/y", "https://ntfy.sh/", "https://ntfy.sh/a/b", "not a url"} {
		if _, err := NewNtfyNotifier(bad, "", ""); err == nil {
			t.Errorf("URL %q acceptée", bad)
		}
	}
	if _, err := NewNtfyNotifier("http://ntfy.local/topic", "secret", ""); err == nil {
		t.Error("jeton envoyé en clair accepté")
	}
	n, err := NewNtfyNotifier(srv.URL+"/atlas-alertes", "", "https://atlas.example/ops")
	if err != nil {
		t.Fatal(err)
	}
	n.Token = "tk_test" // httptest is http; the constructor refuses tokens there
	if err := n.Notify(context.Background(), Notification{Title: "Conteneur web arrêté", Body: "code 1", Severity: "critical"}); err != nil {
		t.Fatal(err)
	}
	if got["topic"] != "atlas-alertes" || got["title"] != "Conteneur web arrêté" || got["priority"] != float64(5) || got["click"] != "https://atlas.example/ops" || auth != "Bearer tk_test" {
		t.Fatalf("requête : %v %q", got, auth)
	}
}

func TestWebPushNotifierPayload(t *testing.T) {
	var payload webPushPayload
	var ttl time.Duration
	var urgency string
	w := WebPushNotifier{Push: func(_ context.Context, p any, d time.Duration, u string) error {
		payload, ttl, urgency = p.(webPushPayload), d, u
		return nil
	}}
	_ = w.Notify(context.Background(), Notification{Key: "ops:container:web:down", Kind: "resolved", Title: "Rétabli"})
	if payload.Tag != "ops:container:web:down" || payload.URL != "/ops" || ttl != time.Hour || urgency != "high" {
		t.Fatalf("payload : %+v %v %s", payload, ttl, urgency)
	}
}
