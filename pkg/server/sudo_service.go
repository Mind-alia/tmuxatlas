package server

import (
	"context"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

// sudoService is the hub side of the sudo terminal: it owns the request
// store, announces requests as tool events and routes terminals to the
// pinned sudo peers.
type sudoService struct {
	opts  *Options
	store *sudo.Store
}

// newSudoService returns nil when the sudo terminal cannot be offered:
// without browser authentication there is no administrator to approve.
func newSudoService(opts *Options) *sudoService {
	if !opts.AuthEnabled || opts.PeerMgr == nil {
		return nil
	}
	service := &sudoService{opts: opts, store: sudo.NewStore(sudo.DefaultLimits(), nil)}
	service.store.OnChange(service.announce)
	return service
}

// Run expires requests even when nobody is looking at them.
func (s *sudoService) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.store.Sweep()
		}
	}
}

// LocalBroker serves agents that share the hub's own host.
func (s *sudoService) LocalBroker() sudo.Broker {
	return sudo.StoreBroker{Store: s.store, HostID: s.opts.PeerMgr.LocalID(), Host: s.opts.PeerMgr.LocalName()}
}

// announce mirrors the most urgent open request of a pane as a "sudo" tool
// event, which drives the sidebar, the badge and web push. The message stays
// generic: only administrators may read the command.
func (s *sudoService) announce(changed sudo.Request) {
	if s.opts.Tracker == nil || changed.Origin.Session == "" {
		return
	}
	origin := changed.Origin
	status := toolevents.Status("")
	message := ""
	for _, request := range s.store.List() {
		o := request.Origin
		if o.HostID != origin.HostID || o.Session != origin.Session || o.Window != origin.Window || o.Pane != origin.Pane {
			continue
		}
		switch request.Status {
		case sudo.StatusPending, sudo.StatusApproved:
			status, message = toolevents.StatusWaiting, "Demande sudo en attente"
		case sudo.StatusAwaitingReview:
			status, message = toolevents.StatusWaiting, "Résultat sudo à relire"
		case sudo.StatusRunning:
			if status == "" {
				status, message = toolevents.StatusActive, "Terminal sudo ouvert"
			}
		}
	}
	event := &toolevents.Event{
		Tool: toolevents.ToolSudo, Host: origin.HostID, HostName: origin.HostName,
		Session: origin.Session, Window: origin.Window, Pane: origin.Pane,
		Status: status, Message: message, Removed: status == "",
	}
	s.opts.Tracker.Record(event)
}
