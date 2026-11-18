package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	if err := store.Add(subscription); err != nil {
		t.Fatal(err)
	}
	sender := &Sender{
		keys: &VAPIDKeys{}, store: store,
		logger: logrus.New().WithField("test", true),
		send: func(context.Context, []byte, *wp.Subscription, *wp.Options) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusGone}, errors.New("gone")
		},
	}
	sender.sendAll(context.Background(), &toolevents.Event{
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

// pushSubscription returns a subscription to endpoint with valid keys, so
// the real webpush-go send gets as far as the HTTP request.
func pushSubscription(t *testing.T, endpoint string) *wp.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return &wp.Subscription{Endpoint: endpoint, Keys: wp.Keys{
		P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(secret),
	}}
}

// hangingSender is a real sender whose only subscription points to a push
// service that accepts the request and never answers, not even the headers.
// The channel gets one value per request that reached the service.
func hangingSender(t *testing.T) (*Sender, <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	reached := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	store, err := NewStoreAt(filepath.Join(t.TempDir(), subscriptionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(pushSubscription(t, srv.URL+"/push/abc")); err != nil {
		t.Fatal(err)
	}
	private, public, err := wp.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	return NewSender(&VAPIDKeys{PublicKey: public, PrivateKey: private}, store, nil, nil), reached
}

// broadcastWithin fails the test unless Broadcast reaches the service,
// returns an error, and does so within limit.
func broadcastWithin(t *testing.T, ctx context.Context, sender *Sender, reached <-chan struct{}, limit time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- sender.Broadcast(ctx, map[string]string{"title": "x"}, time.Hour, "high") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("un envoi sans réponse compte comme livré")
		}
		if elapsed := time.Since(start); elapsed > limit {
			t.Fatalf("Broadcast rendu après %s, %s au plus attendus", elapsed, limit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Broadcast toujours bloqué après 5 s par un service qui ne répond pas")
	}
	select {
	case <-reached:
	default:
		t.Fatal("la requête n'a jamais atteint le service : l'envoi a échoué avant")
	}
}

func TestBroadcastStopsAtTheCallerDeadline(t *testing.T) {
	sender, reached := hangingSender(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	broadcastWithin(t, ctx, sender, reached, 2*time.Second)
}

// Without a deadline from the caller (the tool-event pushes), the HTTP
// client still gives up.
func TestBroadcastHasAFallbackTimeout(t *testing.T) {
	sender, reached := hangingSender(t)
	if sender.client != nil || defaultClient.Timeout != sendTimeout || sendTimeout <= 0 {
		t.Fatalf("client par défaut sans délai : %+v", defaultClient)
	}
	sender.client = &http.Client{Timeout: 200 * time.Millisecond}
	broadcastWithin(t, context.Background(), sender, reached, 2*time.Second)
}

func TestBroadcastUsesLongTTLAndUrgency(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), subscriptionsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var options *wp.Options
	var sendCtx context.Context
	sender := &Sender{
		keys: &VAPIDKeys{}, store: store, logger: logrus.New().WithField("test", true),
		send: func(ctx context.Context, _ []byte, _ *wp.Subscription, o *wp.Options) (*http.Response, error) {
			options, sendCtx = o, ctx
			return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
		},
	}
	if err := sender.Broadcast(context.Background(), map[string]string{"title": "x"}, time.Hour, "high"); err == nil {
		t.Fatal("envoi sans abonnement accepté")
	}
	if err := store.Add(&wp.Subscription{Endpoint: "https://push.example/a"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := sender.Broadcast(ctx, map[string]string{"title": "x"}, time.Hour, "high"); err != nil {
		t.Fatal(err)
	}
	if options == nil || options.TTL != 3600 || options.Urgency != wp.UrgencyHigh || options.HTTPClient != defaultClient {
		t.Fatalf("options : %+v", options)
	}
	if sendCtx != ctx {
		t.Fatal("le contexte de l'appelant n'arrive pas jusqu'à l'envoi")
	}
}
