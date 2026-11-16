package webpush

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	wp "github.com/SherClockHolmes/webpush-go"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/preferences"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

type fakePreferences struct {
	value *preferences.Preferences
}

func (f fakePreferences) Get() *preferences.Preferences { return f.value }

func TestSenderFiltersCurrentPreferences(t *testing.T) {
	prefs := preferences.Default()
	prefs.Notifications.Statuses = []string{"completed"}
	sender := &Sender{prefs: fakePreferences{value: prefs}}
	if sender.statusEnabled(toolevents.StatusWaiting) {
		t.Fatal("waiting should be filtered")
	}
	if !sender.statusEnabled(toolevents.StatusCompleted) {
		t.Fatal("completed should be enabled")
	}
	prefs.Notifications.Statuses = []string{"error"}
	if !sender.statusEnabled(toolevents.StatusError) {
		t.Fatal("sender did not read updated preferences")
	}
}

func TestBuildPayloadUsesStableRemoteIdentity(t *testing.T) {
	payload, ok := buildPayload(&toolevents.Event{
		Tool: toolevents.ToolCodex, Status: toolevents.StatusCompleted,
		Host: "host-a", HostName: "duplicate", Session: "work",
		Window: 2, Pane: "%3",
	})
	if !ok {
		t.Fatal("payload rejected")
	}
	if payload.HostID != "host-a" || payload.HostName != "duplicate" ||
		payload.Session != "work" || payload.Pane != "%3" {
		t.Fatalf("payload = %+v", payload)
	}
	if _, ok := buildPayload(&toolevents.Event{
		Tool: toolevents.ToolCodex, Status: toolevents.StatusWaiting, Session: "work",
	}); ok {
		t.Fatal("session payload without stable host identity should fall back, not send an ambiguous target")
	}
}

func TestSenderPersistsExpiredEndpointRemoval(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), subscriptionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	subscription := &wp.Subscription{Endpoint: "https://push.example/expired"}
	if err := store.Add(subscription, "u-admin"); err != nil {
		t.Fatal(err)
	}
	sender := &Sender{
		keys: &VAPIDKeys{}, store: store,
		logger: logrus.New().WithField("test", true),
		send: func([]byte, *wp.Subscription, *wp.Options) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusGone}, errors.New("gone")
		},
	}
	sender.sendAll(&toolevents.Event{
		Tool: toolevents.ToolCodex, Status: toolevents.StatusWaiting,
		Host: "host-a", Session: "work",
	})
	if store.Count() != 0 {
		t.Fatal("expired endpoint was not removed")
	}
	reloaded, err := NewStoreAt(filepath.Join(filepath.Dir(store.path), subscriptionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Count() != 0 {
		t.Fatal("expired endpoint removal was not durable")
	}
}

func TestBroadcastUsesLongTTLAndUrgency(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), subscriptionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var options *wp.Options
	sender := &Sender{
		keys: &VAPIDKeys{}, store: store, logger: logrus.New().WithField("test", true),
		send: func(_ []byte, _ *wp.Subscription, o *wp.Options) (*http.Response, error) {
			options = o
			return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
		},
	}
	if err := sender.Broadcast(context.Background(), map[string]string{"title": "x"}, time.Hour, "high"); err == nil {
		t.Fatal("envoi sans abonnement accepté")
	}
	if err := store.Add(&wp.Subscription{Endpoint: "https://push.example/a"}, "u-admin"); err != nil {
		t.Fatal(err)
	}
	if err := sender.Broadcast(context.Background(), map[string]string{"title": "x"}, time.Hour, "high"); err != nil {
		t.Fatal(err)
	}
	if options == nil || options.TTL != 3600 || options.Urgency != wp.UrgencyHigh {
		t.Fatalf("options : %+v", options)
	}
}

type fakeAudience struct {
	admins map[string]bool
	hosts  map[string][]string
}

func (f fakeAudience) IsAdmin(id string) bool { return f.admins[id] }
func (f fakeAudience) CanSeeHost(id, host string) bool {
	if f.admins[id] {
		return true
	}
	for _, granted := range f.hosts[id] {
		if granted == host {
			return true
		}
	}
	return false
}

func TestSenderFiltersRecipientsByAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), subscriptionsFileName)
	// A subscription stored before accounts were bound has no account.
	legacy := `{"version":1,"subscriptions":[{"endpoint":"https://push.example/legacy"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreAt(path)
	if err != nil {
		t.Fatal(err)
	}
	for endpoint, account := range map[string]string{
		"https://push.example/admin": "u-admin", "https://push.example/alice": "u-alice", "https://push.example/old-admin": "u-old",
	} {
		if err := store.Add(&wp.Subscription{Endpoint: endpoint}, account); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	sender := &Sender{
		keys: &VAPIDKeys{}, store: store, logger: logrus.New().WithField("test", true),
		send: func(_ []byte, sub *wp.Subscription, _ *wp.Options) (*http.Response, error) {
			got = append(got, sub.Endpoint)
			return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
		},
	}
	sender.SetAudience(fakeAudience{
		admins: map[string]bool{"u-admin": true}, // u-old was demoted
		hosts:  map[string][]string{"u-alice": {"host-alice"}, "u-old": {"host-old"}},
	})
	check := func(name string, send func(), want ...string) {
		t.Helper()
		got = nil
		send()
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: sent to %v, want %v", name, got, want)
		}
	}
	event := func(tool toolevents.Tool, host string) func() {
		return func() {
			sender.sendAll(&toolevents.Event{Tool: tool, Status: toolevents.StatusWaiting, Host: host, Session: "s"})
		}
	}
	check("ops", func() { _ = sender.Broadcast(context.Background(), map[string]string{"t": "x"}, time.Hour, "high") }, "https://push.example/admin")
	check("sudo", event(toolevents.ToolSudo, "host-alice"), "https://push.example/admin")
	check("tool on alice's host", event(toolevents.ToolClaude, "host-alice"), "https://push.example/admin", "https://push.example/alice")
	check("tool on the hub", event(toolevents.ToolClaude, "hub"), "https://push.example/admin")

	if err := store.RemoveAccount("u-alice"); err != nil {
		t.Fatal(err)
	}
	check("after purge", event(toolevents.ToolClaude, "host-alice"), "https://push.example/admin")
}
