package server

import (
	"testing"

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
