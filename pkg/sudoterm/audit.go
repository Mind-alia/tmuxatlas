package sudoterm

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AuditEntry is one line of the sudo audit log. It never holds keystrokes
// nor output: only what was proposed, by whom, who opened the terminal and
// how it ended.
type AuditEntry struct {
	Time          time.Time `json:"time"`
	Event         string    `json:"event"` // open, refused, closed
	RequestID     string    `json:"request_id"`
	Version       int       `json:"version"`
	Command       string    `json:"command"` // as proposed; the human may have typed anything
	CommandSHA256 string    `json:"command_sha256"`
	Requester     string    `json:"requester,omitempty"` // fingerprint of the requesting host
	Approver      string    `json:"approver,omitempty"`
	OriginHost    string    `json:"origin_host,omitempty"`
	OriginSession string    `json:"origin_session,omitempty"`
	UnixUser      string    `json:"unix_user,omitempty"`
	ShellExit     *int      `json:"shell_exit,omitempty"`
	DurationMS    int64     `json:"duration_ms,omitempty"`
	Reason        string    `json:"reason,omitempty"`
}

// CommandDigest is the hex sha256 of a command.
func CommandDigest(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

// AuditPath is $XDG_STATE_HOME/tmuxatlas/sudo-audit.jsonl, by default
// ~/.local/state/tmuxatlas/sudo-audit.jsonl.
func AuditPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "tmuxatlas", "sudo-audit.jsonl"), nil
}

// Audit appends entries to a private JSONL file.
type Audit struct {
	mu   sync.Mutex
	path string
}

func NewAudit(path string) (*Audit, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := file.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("journal sudo trop ouvert : %w", err)
		}
	}
	return &Audit{path: path}, nil
}

// Append writes one entry. A failure is returned: the caller must not run a
// command it cannot record.
func (a *Audit) Append(entry AuditEntry) error {
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	if entry.CommandSHA256 == "" {
		entry.CommandSHA256 = CommandDigest(entry.Command)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	file, err := os.OpenFile(a.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// ReadAudit decodes the last limit entries of an audit file.
func ReadAudit(reader io.Reader, limit int) ([]AuditEntry, error) {
	var entries []AuditEntry
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var entry AuditEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
		if limit > 0 && len(entries) > limit {
			entries = entries[1:]
		}
	}
	return entries, scanner.Err()
}
