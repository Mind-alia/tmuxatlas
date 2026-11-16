package state

import (
	"encoding/json"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"strings"
	"testing"
)

func TestAccountProjectionAfterReassignment(t *testing.T) {
	p := NewProjection()
	key := NewSessionKey("host", "work")
	p.Sessions[key] = Session{Key: key, HostID: "host", Name: "work", Owner: "alice", TmuxID: "secret-id"}
	window := NewWindowKey("host", "work", "@1")
	p.Windows[window] = Window{Key: window, SessionKey: key, Name: "secret-window"}
	pane := NewPaneKey("host", "work", "@1", "%1")
	p.Panes[pane] = Pane{Key: pane, WindowKey: window, CurrentCommand: "secret-command"}
	p.ToolEvents["event"] = ToolEvent{HostID: "host", Session: "work", Message: "secret-event"}
	p.Activity["activity"] = Activity{HostID: "host", Session: "work", Data: map[string]any{"secret": "activity"}}
	p.Hosts[NewHostKey("host")] = Host{ID: "host", DisplayName: "secret-host"}
	check := func(owner string, want bool) {
		t.Helper()
		s := p.Sessions[key]
		s.Owner = tmux.Owner(owner)
		p.Sessions[key] = s
		data, err := json.Marshal(p.ForAccount("alice"))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(string(data), "secret-command")
		if got != want {
			t.Fatalf("owner %q: %s", owner, data)
		}
		if !want && (strings.Contains(string(data), "secret-") || strings.Contains(string(data), "activity\":\"")) {
			t.Fatalf("information leak: %s", data)
		}
	}
	// The owner is a label: changing it never hides a session from an account.
	check("alice", true)
	check("bob", true)
	check("", true)
}

func TestNewAccountSeesOnlyOnlineHostsWithIdentityFields(t *testing.T) {
	p := NewProjection()
	p.Hosts[NewHostKey("online")] = Host{ID: "online", DisplayName: "vps", Online: true, Version: "secret-version"}
	p.Hosts[NewHostKey("offline")] = Host{ID: "offline", DisplayName: "off"}
	view := p.ForAccount("neuf")
	host, visible := view.Hosts[NewHostKey("online")]
	if !visible || host.DisplayName != "vps" || len(view.Hosts) != 1 {
		t.Fatalf("hôtes visibles : %+v", view.Hosts)
	}
	if host.Version != "" {
		t.Fatalf("version d'hôte exposée : %s", host.Version)
	}
	views := p.AccountHostViews("neuf")
	if len(views) != 1 || views[0]["id"] != "online" {
		t.Fatalf("vues : %v", views)
	}
}

func TestForHostsDropsEverythingOfOtherHosts(t *testing.T) {
	p := NewProjection()
	for _, host := range []string{"mine", "prod"} {
		hostKey := NewHostKey(host)
		sessionKey := NewSessionKey(host, "work")
		windowKey := NewWindowKey(host, "work", "@1")
		p.Hosts[hostKey] = Host{Key: hostKey, ID: host, Online: true}
		p.Health[hostKey] = Health{HostKey: hostKey}
		p.Sessions[sessionKey] = Session{Key: sessionKey, HostKey: hostKey, HostID: host, Name: "work"}
		p.Windows[windowKey] = Window{Key: windowKey, SessionKey: sessionKey}
		p.Panes[NewPaneKey(host, "work", "@1", "%1")] = Pane{WindowKey: windowKey}
		p.ToolEvents[ToolEventKey(host+"/e")] = ToolEvent{HostID: host}
		p.Activity[ActivityKey(host+"/a")] = Activity{HostID: host}
	}
	got := p.ForHosts(func(id string) bool { return id == "mine" })
	for name, size := range map[string]int{
		"hosts": len(got.Hosts), "sessions": len(got.Sessions), "windows": len(got.Windows),
		"panes": len(got.Panes), "events": len(got.ToolEvents), "activity": len(got.Activity), "health": len(got.Health),
	} {
		if size != 1 {
			t.Errorf("%s: %d entries, want 1", name, size)
		}
	}
	if _, ok := got.Hosts[NewHostKey("prod")]; ok {
		t.Fatal("other host kept")
	}
}
