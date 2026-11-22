package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// MaxAuditEntries bounds one reading of the trail.
	MaxAuditEntries = 200
	// auditTailBytes is how much of the end of the file a reading parses.
	auditTailBytes = 256 << 10
)

// Entry is one line of the audit trail. It has no field for a value, on
// purpose: Detail only ever receives a target path, a container name or a
// count, written by this package.
type Entry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	App    string    `json:"app,omitempty"`
	Name   string    `json:"name,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// appendAudit adds one line to the append-only trail. v.mu is held.
func (v *Vault) appendAudit(entry Entry) error {
	entry.Time = v.now().UTC()
	if entry.Actor == "" {
		entry.Actor = "inconnu"
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(v.dir, auditFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(line, '\n'))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// auditLocked records a change that already happened: a trail that cannot be
// written is reported in the hub's log, without the entry's content beyond
// its action. Operations that hand out values use appendAudit first and stop
// if it fails.
func (v *Vault) auditLocked(entry Entry) {
	if err := v.appendAudit(entry); err != nil {
		logrus.WithField("component", "secrets").WithField("action", entry.Action).WithError(err).
			Warn("secrets audit trail not written")
	}
}

// Audit returns the last entries, newest first.
func (v *Vault) Audit(limit int) ([]Entry, error) {
	if limit <= 0 || limit > MaxAuditEntries {
		limit = MaxAuditEntries
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	file, err := os.Open(filepath.Join(v.dir, auditFile))
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > auditTailBytes {
		offset = info.Size() - auditTailBytes
	}
	raw, err := io.ReadAll(io.NewSectionReader(file, offset, auditTailBytes))
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(raw, []byte("\n"))
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line of a tail is probably cut
	}
	entries := make([]Entry, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(entries) < limit; i-- {
		var entry Entry
		if len(lines[i]) == 0 || json.Unmarshal(lines[i], &entry) != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
