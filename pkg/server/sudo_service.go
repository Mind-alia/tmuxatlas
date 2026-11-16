package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/toolevents"
)

// sudoService is the hub side of the sudo terminal: it owns the request
// store, announces requests as tool events, answers agent peers and routes
// terminals to the pinned sudo peers. It never runs a command itself.
type sudoService struct {
	opts   *Options
	store  *sudo.Store
	admins SudoAdmins
	pinned []string
	// requesters may file requests; empty refuses everyone.
	requesters []string

	mu    sync.Mutex
	users map[string]string // sudo peer host ID → Unix user it reported
}

// newSudoService returns nil when the sudo terminal cannot be offered:
// without browser authentication there is no administrator to approve.
func newSudoService(opts *Options) *sudoService {
	if !opts.AuthEnabled || opts.PeerMgr == nil {
		return nil
	}
	pinned := parseFingerprints(opts.SudoPeers)
	service := &sudoService{
		opts: opts, store: sudo.NewStore(sudo.DefaultLimits(), nil), admins: newSudoAdmins(opts),
		pinned: pinned, requesters: parseFingerprints(opts.SudoRequesters), users: make(map[string]string),
	}
	service.store.OnChange(service.onChange)
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

// LocalBroker serves agents that share the hub's own host. The hub's own
// fingerprint must be listed in TMUXATLAS_SUDO_REQUESTERS too.
func (s *sudoService) LocalBroker() sudo.Broker {
	broker := sudo.StoreBroker{Store: s.store, HostID: s.opts.PeerMgr.LocalID(), Host: s.opts.PeerMgr.LocalName()}
	return requesterBroker{StoreBroker: broker, allowed: s.mayRequest}
}

// mayRequest reports whether a host may file sudo requests. Only listed
// fingerprints may: a name or a declared origin is never enough.
func (s *sudoService) mayRequest(hostID string) bool {
	if hostID == "" {
		return false
	}
	for _, value := range s.requesters {
		if value == hostID {
			return true
		}
	}
	return false
}

// requesterBroker refuses local requests when the hub is not a requester.
type requesterBroker struct {
	sudo.StoreBroker
	allowed func(string) bool
}

func (b requesterBroker) Create(ctx context.Context, input sudo.CreateInput) (sudo.AgentView, error) {
	if !b.allowed(b.HostID) {
		return sudo.AgentView{}, sudo.ErrForbidden
	}
	return b.StoreBroker.Create(ctx, input)
}

func (s *sudoService) onChange(changed sudo.Request) {
	s.announce(changed)
	s.pushUpdate(changed)
	logrus.WithFields(logrus.Fields{
		"request": changed.ID, "status": changed.Status, "version": changed.Version, "origin": changed.Origin.HostName,
	}).Info("sudo request changed")
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

// pushUpdate tells the requesting peer, and only it, what its agent may see.
func (s *sudoService) pushUpdate(changed sudo.Request) {
	if s.opts.PeerMgr == nil || s.opts.PeerMgr.IsLocal(changed.Origin.HostID) {
		return
	}
	s.send(changed.Origin.HostID, peer.MsgSudoUpdate, peer.SudoUpdatePayload{View: changed.View()})
}

func (s *sudoService) send(hostID, kind string, payload any) {
	connection := s.opts.PeerMgr.GetPeerConnection(hostID)
	if connection == nil {
		return
	}
	message, err := peer.NewMessage(kind, payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = connection.Send(ctx, message)
}

func (s *sudoService) reply(hostID, ref string, view sudo.AgentView, err error) {
	payload := peer.SudoReplyPayload{Ref: ref}
	if err != nil {
		payload.Error = sudo.ErrorCode(err)
	} else {
		payload.View = &view
	}
	s.send(hostID, peer.MsgSudoReply, payload)
}

// HandlePeerSudo processes sudo messages from authenticated peers. The host
// ID comes from the peer's verified identity, never from the payload.
func (s *sudoService) HandlePeerSudo(hostID string, connection *peer.PeerConnection, message *peer.Message) {
	switch message.Type {
	case peer.MsgSudoCreate:
		var payload peer.SudoCreatePayload
		if json.Unmarshal(message.Payload, &payload) != nil || payload.Ref == "" || len(payload.Ref) > 128 {
			return
		}
		if !s.mayRequest(hostID) {
			logrus.WithField("host", hostID).Warn("sudo request from a host outside TMUXATLAS_SUDO_REQUESTERS")
			s.reply(hostID, payload.Ref, sudo.AgentView{}, sudo.ErrForbidden)
			return
		}
		origin := payload.Origin
		origin.HostID, origin.HostName = hostID, s.opts.PeerMgr.GetHostName(hostID)
		request, err := s.store.Create(sudo.CreateInput{Origin: origin, Command: payload.Command, Reason: payload.Reason})
		s.reply(hostID, payload.Ref, request.View(), err)
	case peer.MsgSudoCancel:
		var payload peer.SudoRefPayload
		if json.Unmarshal(message.Payload, &payload) != nil || payload.Ref == "" || len(payload.Ref) > 128 {
			return
		}
		request, err := s.store.Cancel(payload.ID, hostID)
		s.reply(hostID, payload.Ref, request.View(), err)
	case peer.MsgSudoWatch:
		var payload peer.SudoRefPayload
		if json.Unmarshal(message.Payload, &payload) != nil {
			return
		}
		request, ok := s.store.Get(payload.ID)
		view := request.View()
		if !ok || request.Origin.HostID != hostID {
			view = sudo.AgentView{ID: payload.ID, Status: sudo.StatusFailed, Note: "demande inconnue du hub (redémarré ?)"}
		}
		s.send(hostID, peer.MsgSudoUpdate, peer.SudoUpdatePayload{View: view})
	case peer.MsgSudoTerminalInfo:
		var payload peer.SudoTerminalInfoPayload
		if json.Unmarshal(message.Payload, &payload) != nil || len(payload.UnixUser) > 64 || !connection.Supports(peer.CapabilitySudoTerminal) {
			return
		}
		s.mu.Lock()
		s.users[hostID] = payload.UnixUser
		s.mu.Unlock()
	case peer.MsgSudoResult:
		var payload peer.SudoResultPayload
		if json.Unmarshal(message.Payload, &payload) != nil {
			return
		}
		s.finish(hostID, payload)
	}
}

// finish accepts a result only from the peer the request was sent to.
func (s *sudoService) finish(hostID string, result peer.SudoResultPayload) {
	request, ok := s.store.Get(result.RequestID)
	if !ok || request.Peer != hostID || request.Version != result.Version {
		logrus.WithField("request", result.RequestID).Warn("ignored sudo result from an unexpected peer")
		return
	}
	if !result.Started {
		note := "terminal sudo refusé par le peer"
		if result.Error != "" {
			note += " : " + result.Error
		}
		_, _ = s.store.Fail(request.ID, note)
		return
	}
	_, err := s.store.Finish(request.ID, sudo.Capture{
		Version: result.Version, ExitCode: result.ExitCode, Output: result.Output,
		OutputTruncated: result.Truncated, Redactions: result.Redactions,
		Duration: time.Duration(result.DurationMS) * time.Millisecond, Error: result.Error,
	})
	if err != nil {
		logrus.WithError(err).WithField("request", request.ID).Warn("could not store sudo result")
	}
}

// sudoPeerView is what administrators see of a sudo terminal peer.
type sudoPeerView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	UnixUser string `json:"unix_user,omitempty"`
	Online   bool   `json:"online"`
}

// isPinned accepts fingerprints only: a peer name is a label, a fingerprint
// is a key the agents account cannot hold.
func (s *sudoService) isPinned(id string) bool {
	for _, value := range s.pinned {
		if value == id {
			return true
		}
	}
	return false
}

// Peers lists the pinned sudo peers and whether they can take a terminal.
func (s *sudoService) Peers() []sudoPeerView {
	var peers []sudoPeerView
	for _, host := range s.opts.PeerMgr.GetHosts() {
		if host.Local || !s.isPinned(host.ID) {
			continue
		}
		connection := s.opts.PeerMgr.GetPeerConnection(host.ID)
		s.mu.Lock()
		unixUser := s.users[host.ID]
		s.mu.Unlock()
		peers = append(peers, sudoPeerView{
			ID: host.ID, Name: host.Name, UnixUser: unixUser,
			Online: connection != nil && connection.Supports(peer.CapabilitySudoTerminal),
		})
	}
	return peers
}

var errSudoPeer = errors.New("peer sudo non autorisé, hors ligne ou identique à l'hôte de l'agent")

// terminalPeer returns the connection of an allowed sudo peer.
func (s *sudoService) terminalPeer(hostID, originHost string) (*peer.PeerConnection, error) {
	if hostID == "" || hostID == originHost || s.opts.PeerMgr.IsLocal(hostID) {
		return nil, errSudoPeer
	}
	if !s.isPinned(hostID) {
		return nil, errSudoPeer
	}
	connection := s.opts.PeerMgr.GetPeerConnection(hostID)
	if connection == nil || !connection.Supports(peer.CapabilitySudoTerminal) {
		return nil, errSudoPeer
	}
	return connection, nil
}
