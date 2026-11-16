// Package sudo holds the lifecycle of sudo requests made by agents.
//
// An agent asks for a command; an administrator may edit it, approves an
// exact version, types the password in an ephemeral terminal opened by a
// dedicated peer, then reviews the captured output before anything is sent
// back. The package is pure state: it never runs a command.
package sudo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Status is the state of a request.
type Status string

const (
	StatusPending        Status = "pending"         // waiting for an administrator
	StatusApproved       Status = "approved"        // a single-use grant was issued
	StatusRunning        Status = "running"         // the terminal is open
	StatusAwaitingReview Status = "awaiting_review" // output captured, held for review
	StatusDelivered      Status = "delivered"       // result sent to the agent
	StatusWithheld       Status = "withheld"        // the reviewer sent nothing back
	StatusRefused        Status = "refused"
	StatusExpired        Status = "expired"
	StatusCancelled      Status = "cancelled"
	StatusFailed         Status = "failed"
)

// Final reports whether the agent can stop waiting.
func (s Status) Final() bool {
	switch s {
	case StatusDelivered, StatusWithheld, StatusRefused, StatusExpired, StatusCancelled, StatusFailed:
		return true
	}
	return false
}

// Decision is the reviewer's choice of what the agent receives.
type Decision string

const (
	DecisionSend     Decision = "send"      // exit code and output
	DecisionExitCode Decision = "exit-code" // exit code only
	DecisionNothing  Decision = "nothing"   // status only
)

const (
	MaxCommandBytes = 4096
	MaxCommandRunes = 1000 // what an administrator can still read in full
	MaxReasonBytes  = 512
	MaxReasonRunes  = 1000
	MaxBlankRun     = 2 // longer runs of blanks could push a payload out of sight
	MaxNoteBytes    = 512
	MaxOutputBytes  = 64 << 10
)

var (
	ErrNotFound        = errors.New("demande sudo inconnue")
	ErrInvalidState    = errors.New("état de la demande incompatible")
	ErrVersionMismatch = errors.New("la commande a changé : relis la dernière version")
	ErrInvalidCommand  = errors.New("commande invalide")
	ErrInvalidText     = errors.New("texte invalide")
	ErrGrantInvalid    = errors.New("autorisation invalide, expirée ou déjà utilisée")
	ErrTooMany         = errors.New("trop de demandes sudo en attente")
	ErrForbidden       = errors.New("demande sudo d'un autre hôte")
)

// Origin ties a request to the agent conversation that asked for it. Every
// field except HostID is reported by the agent and is shown as such.
type Origin struct {
	HostID          string `json:"host_id"`
	HostName        string `json:"host_name,omitempty"`
	Session         string `json:"session,omitempty"`
	Window          int    `json:"window"`
	Pane            string `json:"pane,omitempty"`
	ClaudeSessionID string `json:"claude_session_id,omitempty"`
	Cwd             string `json:"cwd,omitempty"`
	PID             int    `json:"pid,omitempty"`
	UnixUser        string `json:"unix_user,omitempty"`
}

// Actor identifies the administrator behind an action.
type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Revision is one version of the command.
type Revision struct {
	Version int       `json:"version"`
	Command string    `json:"command"`
	By      string    `json:"by"` // "agent" or an administrator's name
	At      time.Time `json:"at"`
}

// Capture is what the sudo terminal recorded: only the command's stdout and
// stderr, read through pipes, already redacted.
type Capture struct {
	Version         int           `json:"version"`
	Command         string        `json:"command"`
	ExitCode        int           `json:"exit_code"`
	Output          string        `json:"output"`
	OutputTruncated bool          `json:"output_truncated"`
	Redactions      int           `json:"redactions"`
	Duration        time.Duration `json:"duration"`
	Error           string        `json:"error,omitempty"`
}

// Request is a full request, as administrators see it.
type Request struct {
	ID        string     `json:"id"`
	Status    Status     `json:"status"`
	Version   int        `json:"version"`
	Command   string     `json:"command"`
	Reason    string     `json:"reason,omitempty"`
	Origin    Origin     `json:"origin"`
	Revisions []Revision `json:"revisions"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	Approver  *Actor     `json:"approver,omitempty"`
	Peer      string     `json:"peer,omitempty"` // sudo terminal peer chosen at approval
	Capture   *Capture   `json:"capture,omitempty"`
	Decision  Decision   `json:"decision,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// Original returns the command as the agent first asked for it.
func (r Request) Original() string {
	if len(r.Revisions) == 0 {
		return r.Command
	}
	return r.Revisions[0].Command
}

// AgentView is the only thing an agent ever receives.
type AgentView struct {
	ID               string `json:"id"`
	Status           Status `json:"status"`
	CommandRequested string `json:"command_requested"`
	CommandExecuted  string `json:"command_executed,omitempty"`
	Modified         bool   `json:"modified"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Output           string `json:"output,omitempty"`
	OutputTruncated  bool   `json:"output_truncated,omitempty"`
	Note             string `json:"note,omitempty"`
}

// View builds what the agent may see. Output exists only once a reviewer
// chose to send it.
func (r Request) View() AgentView {
	view := AgentView{ID: r.ID, Status: r.Status, CommandRequested: r.Original(), Note: r.Note}
	if !r.Status.Final() {
		view.Note = ""
		return view
	}
	if r.Status == StatusDelivered && r.Capture != nil {
		view.CommandExecuted = r.Capture.Command
		view.Modified = r.Capture.Command != r.Original()
		code := r.Capture.ExitCode
		view.ExitCode = &code
		if r.Decision == DecisionSend {
			view.Output = r.Capture.Output
			view.OutputTruncated = r.Capture.OutputTruncated
		}
	}
	return view
}

func (r Request) clone() Request {
	r.Revisions = append([]Revision(nil), r.Revisions...)
	if r.Approver != nil {
		approver := *r.Approver
		r.Approver = &approver
	}
	if r.Capture != nil {
		capture := *r.Capture
		r.Capture = &capture
	}
	return r
}

// ValidateCommand rejects empty, oversized, binary or multi-line commands,
// and anything that could make the command look different from what runs:
// control characters (C0 and C1, tabs included), format characters (bidi
// overrides, zero-width spaces, BOM), line and paragraph separators, and runs
// of blanks long enough to push a second command out of view.
func ValidateCommand(command string) error {
	if strings.TrimSpace(command) == "" || len(command) > MaxCommandBytes {
		return ErrInvalidCommand
	}
	if !displaySafe(command, MaxCommandRunes) {
		return ErrInvalidCommand
	}
	return nil
}

// Fingerprint is the short sha256 shown next to a command, in the panel and
// in the sudo terminal, so the administrator can check both show the same.
func Fingerprint(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:6])
}

// ValidateReason applies the command rules to the agent's reason, which is
// shown next to it; an empty reason is allowed.
func ValidateReason(reason string) error {
	if reason == "" {
		return nil
	}
	if len(reason) > MaxReasonBytes || !displaySafe(reason, MaxReasonRunes) {
		return ErrInvalidText
	}
	return nil
}

// displaySafe reports whether value is valid UTF-8 of at most limit
// characters, contains no Cc, Cf, Zl or Zp character and no run of more than
// MaxBlankRun blanks.
func displaySafe(value string, limit int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	run := 0
	for _, r := range value {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
		if unicode.Is(unicode.Zs, r) {
			run++
			if run > MaxBlankRun {
				return false
			}
			continue
		}
		run = 0
	}
	return true
}

func validateText(value string, limit int) error {
	if len(value) > limit || !utf8.ValidString(value) {
		return ErrInvalidText
	}
	for _, r := range value {
		if r < 0x20 && r != '\t' && r != '\n' || r == 0x7f {
			return ErrInvalidText
		}
	}
	return nil
}
