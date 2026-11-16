package server

import (
	"context"
	"errors"
	"testing"

	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

func sudoEvents(tracker *toolevents.Tracker) []*toolevents.Event {
	var events []*toolevents.Event
	for _, event := range tracker.GetAll() {
		if event.Tool == toolevents.ToolSudo {
			events = append(events, event)
		}
	}
	return events
}

func TestSudoAnnounceFollowsOpenRequests(t *testing.T) {
	tracker := toolevents.NewTracker()
	service := &sudoService{opts: &Options{Tracker: tracker}, store: sudo.NewStore(sudo.DefaultLimits(), nil)}
	service.store.OnChange(service.announce)
	origin := sudo.Origin{HostID: "host-agents", HostName: "agents", Session: "claude", Window: 1, Pane: "%3"}
	first, _ := service.store.Create(sudo.CreateInput{Origin: origin, Command: "id"})
	second, _ := service.store.Create(sudo.CreateInput{Origin: origin, Command: "id -u"})

	events := sudoEvents(tracker)
	if len(events) != 1 || events[0].Status != toolevents.StatusWaiting || events[0].Host != "host-agents" || events[0].Pane != "%3" {
		t.Fatalf("events after create = %+v", events)
	}
	if events[0].Message != "Demande sudo en attente" {
		t.Fatalf("message must stay generic, got %q", events[0].Message)
	}
	admin := sudo.Actor{ID: "a", Name: "admin"}
	service.store.Refuse(first.ID, admin, "")
	if events := sudoEvents(tracker); len(events) != 1 {
		t.Fatalf("event dropped while a request is still open: %+v", events)
	}
	service.store.Refuse(second.ID, admin, "")
	if events := sudoEvents(tracker); len(events) != 0 {
		t.Fatalf("event kept after the last request ended: %+v", events)
	}
}

func TestSudoRequestsOnlyFromListedRequesters(t *testing.T) {
	hub := mustIdentity(t)
	create := func(service *sudoService, host string) {
		message, err := peer.NewMessage(peer.MsgSudoCreate, peer.SudoCreatePayload{Ref: "r1", Command: "id", Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		service.HandlePeerSudo(host, nil, message)
	}
	for _, tt := range []struct {
		name       string
		requesters string
		host       string
		want       int
	}{
		{"empty list refuses everyone", "", "host-agents", 0},
		{"unlisted host", "host-other", "host-agents", 0},
		{"listed host", "host-other, host-agents", "host-agents", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := newSudoService(&Options{AuthEnabled: true, PeerMgr: peer.NewHubManager(hub, nil), SudoRequesters: tt.requesters})
			create(service, tt.host)
			if got := len(service.store.List()); got != tt.want {
				t.Fatalf("requests = %d, want %d", got, tt.want)
			}
		})
	}
	local := newSudoService(&Options{AuthEnabled: true, PeerMgr: peer.NewHubManager(hub, nil)})
	if _, err := local.LocalBroker().Create(context.Background(), sudo.CreateInput{Command: "id"}); !errors.Is(err, sudo.ErrForbidden) {
		t.Fatalf("local request with an empty list: %v", err)
	}
	local = newSudoService(&Options{AuthEnabled: true, PeerMgr: peer.NewHubManager(hub, nil), SudoRequesters: hub.Fingerprint()})
	if _, err := local.LocalBroker().Create(context.Background(), sudo.CreateInput{Command: "id"}); err != nil {
		t.Fatalf("local request from a listed hub: %v", err)
	}
}

func TestDecisionClosesAnOpenTerminal(t *testing.T) {
	service := &sudoService{opts: &Options{}, store: sudo.NewStore(sudo.DefaultLimits(), nil)}
	service.store.OnChange(service.onChange)
	request, _ := service.store.Create(sudo.CreateInput{Origin: sudo.Origin{HostID: "host-agents"}, Command: "id"})
	admin := sudo.Actor{ID: "a", Name: "admin"}
	_, grant, _ := service.store.Approve(request.ID, 1, "peer-admin", admin)
	if _, err := service.store.Redeem(grant.Token, admin); err != nil {
		t.Fatal(err)
	}
	closed := 0
	untrack := service.track(request.ID, func() { closed++ })
	defer untrack()
	if _, err := service.store.Done(request.ID, admin, "fait"); err != nil {
		t.Fatal(err)
	}
	if closed != 1 {
		t.Fatalf("terminal closed %d times after the decision", closed)
	}
}
