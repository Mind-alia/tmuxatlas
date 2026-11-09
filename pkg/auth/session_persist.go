package auth

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
)

// refreshSaveInterval bounds how often using a session rewrites the file. A
// restart loses at most this much of a session's extended lifetime.
const refreshSaveInterval = time.Minute

// storedSession is a session as written to disk: the token's hash, never the token.
type storedSession struct {
	Key        string    `json:"key"`
	CSRF       string    `json:"csrf"`
	AccountID  string    `json:"account_id,omitempty"`
	Credential string    `json:"credential,omitempty"`
	Issued     time.Time `json:"issued"`
	Expiry     time.Time `json:"expiry"`
}

// PersistTo keeps the sessions in a file, so that restarting TmuxAtlas signs no
// one out. It restores the sessions saved there that have not expired, then
// rewrites the file after each change. Writes happen in the background, outside
// the lock every request takes, so a slow disk never stalls authentication. An
// unreadable file is reported and replaced: the sessions it held are lost.
func (sm *SessionManager) PersistTo(path string) error {
	sm.mu.Lock()
	sm.path = path
	loadErr := sm.loadLocked()
	start := sm.wake == nil
	if start {
		sm.wake = make(chan struct{}, 1)
		sm.stopped = make(chan struct{})
		go sm.writeLoop(sm.wake, sm.stopped)
	}
	sm.mu.Unlock()
	if err := sm.Flush(); err != nil {
		return errors.Join(loadErr, err)
	}
	return loadErr
}

func validHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (sm *SessionManager) loadLocked() error {
	raw, err := os.ReadFile(sm.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sessions: %w", err)
	}
	var stored []storedSession
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("read sessions %s: %w", sm.path, err)
	}
	now := time.Now()
	for _, value := range stored {
		// A session without a full CSRF token would accept a request that sends none.
		if !now.Before(value.Expiry) || !validHex(value.Key, 64) || !validHex(value.CSRF, 64) {
			continue
		}
		key := value.Key
		sm.sessions[key] = session{
			expiry: value.Expiry, issued: value.Issued, csrf: value.CSRF,
			accountID: value.AccountID, credential: value.Credential, done: make(chan struct{}),
		}
		time.AfterFunc(time.Until(value.Expiry), func() { sm.expire(key) })
	}
	return nil
}

// Flush writes the live sessions now. Saves are otherwise asynchronous; call it
// before exiting so that the last changes reach the file.
func (sm *SessionManager) Flush() error {
	sm.writeMu.Lock()
	defer sm.writeMu.Unlock()
	sm.mu.RLock()
	path := sm.path
	now := time.Now()
	stored := make([]storedSession, 0, len(sm.sessions))
	for key, value := range sm.sessions {
		if now.Before(value.expiry) {
			stored = append(stored, storedSession{
				Key: key, CSRF: value.csrf, AccountID: value.accountID,
				Credential: value.credential, Issued: value.issued, Expiry: value.expiry,
			})
		}
	}
	sm.mu.RUnlock()
	if path == "" {
		return nil
	}
	return writeSessions(path, stored)
}

// writeSessions replaces the file atomically, readable by its owner only.
func writeSessions(path string, stored []storedSession) error {
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".sessions-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("save sessions: %w", err)
	}
	return nil
}

// writeLoop saves once per wake-up; changes made during a write share the next one.
func (sm *SessionManager) writeLoop(wake <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	for range wake {
		if err := sm.Flush(); err != nil {
			logrus.WithError(err).Warn("browser sessions not saved")
		}
	}
}

// Close stops the background writer and saves the sessions a last time. Later
// changes stay in memory only.
func (sm *SessionManager) Close() error {
	sm.mu.Lock()
	wake, stopped := sm.wake, sm.stopped
	sm.wake = nil
	if wake != nil {
		close(wake)
	}
	sm.mu.Unlock()
	if stopped == nil {
		return nil
	}
	<-stopped
	err := sm.Flush()
	sm.mu.Lock()
	sm.path = ""
	sm.mu.Unlock()
	return err
}

// persistLocked asks for a save after a change; a failure only costs sessions
// on restart. sm.mu must be held.
func (sm *SessionManager) persistLocked() {
	if sm.path == "" || sm.wake == nil {
		return
	}
	sm.savedAt = time.Now()
	select {
	case sm.wake <- struct{}{}:
	default:
	}
}

// persistRefreshLocked saves a session's extended expiry now and then, not on every request.
func (sm *SessionManager) persistRefreshLocked() {
	if sm.path != "" && time.Since(sm.savedAt) >= refreshSaveInterval {
		sm.persistLocked()
	}
}
