package peer

import (
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// Sudo capabilities. A hub announces sudo-requests when it stores
// requests; a peer announces sudo-terminal only when started with
// --sudo-terminal, and the hub still routes terminals only to pinned peers.
const (
	CapabilitySudoRequests = "sudo-requests"
	CapabilitySudoTerminal = "sudo-terminal"
)

// Sudo messages over the control WebSocket.
const (
	MsgSudoCreate       = "sudo-create"        // agent peer → hub
	MsgSudoCancel       = "sudo-cancel"        // agent peer → hub
	MsgSudoWatch        = "sudo-watch"         // agent peer → hub, after a reconnect
	MsgSudoReply        = "sudo-reply"         // hub → agent peer, answers create and cancel
	MsgSudoUpdate       = "sudo-update"        // hub → agent peer, on every change
	MsgSudoOpen         = "sudo-open"          // hub → sudo peer
	MsgSudoResult       = "sudo-result"        // sudo peer → hub
	MsgSudoTerminalInfo = "sudo-terminal-info" // sudo peer → hub, after negotiation
)

// HubCapabilities is what a hub accepts during negotiation.
var HubCapabilities = append(append([]string(nil), RuntimeCapabilities...), CapabilitySudoRequests, CapabilitySudoTerminal)

type SudoCreatePayload struct {
	Ref     string      `json:"ref"`
	Command string      `json:"command"`
	Reason  string      `json:"reason,omitempty"`
	Origin  sudo.Origin `json:"origin"`
}

type SudoRefPayload struct {
	Ref string `json:"ref,omitempty"`
	ID  string `json:"id"`
}

type SudoReplyPayload struct {
	Ref   string          `json:"ref"`
	View  *sudo.AgentView `json:"view,omitempty"`
	Error string          `json:"error,omitempty"` // sudo.ErrorCode
}

type SudoUpdatePayload struct {
	View sudo.AgentView `json:"view"`
}

// SudoOpenPayload asks the sudo peer to open a terminal with one exact
// version typed at the prompt.
type SudoOpenPayload struct {
	StreamID    string      `json:"stream_id"`
	AttachToken string      `json:"attach_token"`
	Generation  uint64      `json:"generation"`
	RequestID   string      `json:"request_id"`
	Version     int         `json:"version"`
	Command     string      `json:"command"`
	Approver    string      `json:"approver"`
	Origin      sudo.Origin `json:"origin"`
	Cols        uint16      `json:"cols"`
	Rows        uint16      `json:"rows"`
}

// SudoSession is the PTY relay session name of a sudo terminal.
func SudoSession(requestID string) string { return "sudo:" + requestID }

// SudoResultPayload tells the hub that a terminal ended, or never opened.
// It carries nothing of what was shown or typed.
type SudoResultPayload struct {
	RequestID  string `json:"request_id"`
	Version    int    `json:"version"`
	StreamID   string `json:"stream_id"`
	Started    bool   `json:"started"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

type SudoTerminalInfoPayload struct {
	UnixUser string `json:"unix_user"`
}
