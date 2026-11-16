package sudo

import (
	"context"
	"errors"
)

// Broker is what the local socket offers an agent: ask, wait, give up.
// The hub implements it over its Store; a peer implements it by relaying to
// the hub over its authenticated control channel.
type Broker interface {
	Create(ctx context.Context, input CreateInput) (AgentView, error)
	Wait(ctx context.Context, id string) (AgentView, error)
	Cancel(ctx context.Context, id string) (AgentView, error)
}

// ErrUnavailable means the hub cannot be reached or has no sudo terminal.
var ErrUnavailable = errors.New("terminal sudo indisponible")

// StoreBroker serves agents running on the hub's own host.
type StoreBroker struct {
	Store  *Store
	HostID string
	Host   string
}

func (b StoreBroker) Create(_ context.Context, input CreateInput) (AgentView, error) {
	if b.Store == nil || b.HostID == "" {
		return AgentView{}, ErrUnavailable
	}
	input.Origin.HostID, input.Origin.HostName = b.HostID, b.Host
	request, err := b.Store.Create(input)
	if err != nil {
		return AgentView{}, err
	}
	return request.View(), nil
}

func (b StoreBroker) Wait(ctx context.Context, id string) (AgentView, error) {
	if b.Store == nil {
		return AgentView{}, ErrUnavailable
	}
	if request, ok := b.Store.Get(id); !ok || request.Origin.HostID != b.HostID {
		return AgentView{}, ErrNotFound
	}
	request, err := b.Store.Wait(ctx, id)
	if err != nil {
		return AgentView{}, err
	}
	return request.View(), nil
}

func (b StoreBroker) Cancel(_ context.Context, id string) (AgentView, error) {
	if b.Store == nil {
		return AgentView{}, ErrUnavailable
	}
	request, err := b.Store.Cancel(id, b.HostID)
	if err != nil {
		return AgentView{}, err
	}
	return request.View(), nil
}

var errorCodes = map[string]error{
	"not-found": ErrNotFound, "invalid-state": ErrInvalidState, "version-mismatch": ErrVersionMismatch,
	"invalid-command": ErrInvalidCommand, "invalid-text": ErrInvalidText, "grant-invalid": ErrGrantInvalid,
	"too-many": ErrTooMany, "forbidden": ErrForbidden, "unavailable": ErrUnavailable,
}

// ErrorCode names a package error for the wire; unknown errors become
// "unavailable" so internal details never reach an agent.
func ErrorCode(err error) string {
	for code, known := range errorCodes {
		if errors.Is(err, known) {
			return code
		}
	}
	return "unavailable"
}

// ErrorFromCode is the inverse of ErrorCode.
func ErrorFromCode(code string) error {
	if err, ok := errorCodes[code]; ok {
		return err
	}
	return ErrUnavailable
}
