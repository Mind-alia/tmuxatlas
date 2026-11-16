package peer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// sudoRelay is the agent peer's side of sudo requests: it forwards them to
// the hub and keeps the latest view of each one for local waiters.
type sudoRelay struct {
	client *Client

	mu      sync.Mutex
	replies map[string]chan SudoReplyPayload
	views   map[string]sudo.AgentView
	changed chan struct{}
}

func newSudoRelay(client *Client) *sudoRelay {
	return &sudoRelay{client: client, replies: make(map[string]chan SudoReplyPayload),
		views: make(map[string]sudo.AgentView), changed: make(chan struct{})}
}

// SudoBroker lets the local socket relay sudo requests to the hub.
func (c *Client) SudoBroker() sudo.Broker { return c.sudo }

func (c *Client) sendControl(message *Message) error {
	c.mu.Lock()
	conn := c.conn
	_, supported := c.runtimeCapabilities[CapabilitySudoRequests]
	c.mu.Unlock()
	if conn == nil {
		return sudo.ErrUnavailable
	}
	if message.Type != MsgSudoResult && message.Type != MsgSudoTerminalInfo && !supported {
		return sudo.ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != conn {
		return sudo.ErrUnavailable
	}
	return conn.WriteJSON(message)
}

func (r *sudoRelay) request(ctx context.Context, kind string, payload any, ref string) (sudo.AgentView, error) {
	reply := make(chan SudoReplyPayload, 1)
	r.mu.Lock()
	r.replies[ref] = reply
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.replies, ref)
		r.mu.Unlock()
	}()
	message, err := NewMessage(kind, payload)
	if err != nil {
		return sudo.AgentView{}, err
	}
	if err := r.client.sendControl(message); err != nil {
		return sudo.AgentView{}, sudo.ErrUnavailable
	}
	select {
	case <-ctx.Done():
		return sudo.AgentView{}, sudo.ErrUnavailable
	case answer := <-reply:
		if answer.Error != "" || answer.View == nil {
			return sudo.AgentView{}, sudo.ErrorFromCode(answer.Error)
		}
		r.store(*answer.View)
		return *answer.View, nil
	}
}

func (r *sudoRelay) Create(ctx context.Context, input sudo.CreateInput) (sudo.AgentView, error) {
	ref := randomPTYSecret()
	input.Origin.HostID, input.Origin.HostName = "", "" // the hub stamps the authenticated host
	return r.request(ctx, MsgSudoCreate, SudoCreatePayload{
		Ref: ref, Command: input.Command, Reason: input.Reason, Origin: input.Origin,
	}, ref)
}

func (r *sudoRelay) Cancel(ctx context.Context, id string) (sudo.AgentView, error) {
	ref := randomPTYSecret()
	return r.request(ctx, MsgSudoCancel, SudoRefPayload{Ref: ref, ID: id}, ref)
}

// Wait returns once the request is final, or its latest state when ctx ends.
func (r *sudoRelay) Wait(ctx context.Context, id string) (sudo.AgentView, error) {
	if id == "" {
		return sudo.AgentView{}, sudo.ErrNotFound
	}
	if message, err := NewMessage(MsgSudoWatch, SudoRefPayload{ID: id}); err == nil {
		_ = r.client.sendControl(message)
	}
	for {
		r.mu.Lock()
		view, known := r.views[id]
		changed := r.changed
		r.mu.Unlock()
		if known && view.Status.Final() {
			return view, nil
		}
		select {
		case <-ctx.Done():
			if !known {
				return sudo.AgentView{ID: id, Status: sudo.StatusPending}, nil
			}
			return view, nil
		case <-changed:
		}
	}
}

func (r *sudoRelay) store(view sudo.AgentView) {
	if view.ID == "" {
		return
	}
	r.mu.Lock()
	r.views[view.ID] = view
	// Keep memory bounded: forget final views beyond a few hundred.
	if len(r.views) > 256 {
		for id, existing := range r.views {
			if existing.Status.Final() && id != view.ID {
				delete(r.views, id)
				break
			}
		}
	}
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}

func (r *sudoRelay) handle(message *Message) {
	switch message.Type {
	case MsgSudoReply:
		var reply SudoReplyPayload
		if json.Unmarshal(message.Payload, &reply) != nil {
			return
		}
		r.mu.Lock()
		waiter := r.replies[reply.Ref]
		r.mu.Unlock()
		if waiter != nil {
			select {
			case waiter <- reply:
			default:
			}
		}
	case MsgSudoUpdate:
		var update SudoUpdatePayload
		if json.Unmarshal(message.Payload, &update) == nil {
			r.store(update.View)
		}
	}
}

// resync asks the hub again about every open request after a reconnect.
func (r *sudoRelay) resync() {
	r.mu.Lock()
	var open []string
	for id, view := range r.views {
		if !view.Status.Final() {
			open = append(open, id)
		}
	}
	r.mu.Unlock()
	for _, id := range open {
		if message, err := NewMessage(MsgSudoWatch, SudoRefPayload{ID: id}); err == nil {
			if errors.Is(r.client.sendControl(message), sudo.ErrUnavailable) {
				return
			}
		}
	}
}
