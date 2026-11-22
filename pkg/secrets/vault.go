// Package secrets is the hub's vault for application environment variables.
//
// Every value is encrypted on its own with age for the vault's X25519 public
// key, so adding or replacing a value never needs the private key. The
// private key is stored encrypted by the admin's passphrase (age scrypt) and
// is only ever held in memory, for a limited time, after an explicit unlock.
// Names stay readable; values, passphrases and keys never reach a log, an
// error or the audit trail. See docs/secrets.md.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"filippo.io/age"
	"filippo.io/age/armor"
	"golang.org/x/sys/unix"
)

const (
	// DefaultUnlockTTL is how long the private key stays in memory after an
	// unlock. It is never extended by activity.
	DefaultUnlockTTL = 10 * time.Minute
	// MinPassphraseLength and MaxPassphraseLength bound the passphrase, in
	// characters.
	MinPassphraseLength = 12
	MaxPassphraseLength = 1024
	// MaxValueBytes bounds one value.
	MaxValueBytes = 64 << 10
	// MaxImportBytes bounds a pasted dotenv text.
	MaxImportBytes = 256 << 10
	// MaxApps and MaxVariables bound the vault.
	MaxApps      = 200
	MaxVariables = 500

	vaultVersion = 1
	vaultFile    = "vault.json"
	lockFile     = ".lock"
	appsDir      = "apps"
	auditFile    = "audit.log"

	// Unlock attempts: after freeAttempts consecutive failures every new
	// failure blocks the next attempt, from baseBlock doubling up to maxBlock.
	freeAttempts = 5
	baseBlock    = 30 * time.Second
	maxBlock     = 15 * time.Minute
	// failureWindow without a new failure clears the count.
	failureWindow = time.Hour
	// maxScryptLogN bounds the work a vault file can ask of an unlock: a
	// rewritten file must not be able to stall the hub. The age default is 18.
	maxScryptLogN = 20
)

// Errors mapped to HTTP statuses by the handler. None of them ever carries a
// value or a passphrase.
var (
	ErrNotInitialized     = errors.New("le coffre n’est pas initialisé")
	ErrAlreadyInitialized = errors.New("le coffre est déjà initialisé")
	ErrLocked             = errors.New("le coffre est verrouillé")
	// ErrLockedHere is ErrLocked for a session that did not give the
	// passphrase while another one did.
	ErrLockedHere      = fmt.Errorf("%w pour cette session : il est déverrouillé dans une autre, saisis la phrase de passe ici", ErrLocked)
	ErrWrongPassphrase = errors.New("phrase de passe incorrecte")
	ErrAppNotFound     = errors.New("application introuvable")
	ErrAppExists       = errors.New("cette application existe déjà")
	ErrVarNotFound     = errors.New("variable introuvable")
	ErrNoTarget        = errors.New("aucun fichier cible n’est configuré pour cette application")
	ErrCorrupt         = errors.New("le coffre est illisible ou incohérent")
	// errKeyChanged and errVaultGone are the vault file changing under a
	// running hub: nothing is encrypted for a key that was not there at start.
	errKeyChanged = fmt.Errorf("%w : sa clé publique a changé depuis le démarrage du hub", ErrCorrupt)
	errVaultGone  = fmt.Errorf("%w : son fichier a disparu depuis le démarrage du hub", ErrCorrupt)
)

// ValidationError is a refused input; its message is safe to show.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// FailureError is an operation that failed on the server's side, with a
// message that is safe to show.
type FailureError struct{ Message string }

func (e *FailureError) Error() string { return e.Message }

func failure(message string) error { return &FailureError{Message: message} }

// ThrottledError refuses an unlock attempt after too many failures.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("trop de tentatives : réessaie dans %d s", int(e.RetryAfter.Round(time.Second)/time.Second))
}

var (
	appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
)

// ValidateAppName accepts [a-z0-9][a-z0-9_-]{0,62}. The name becomes a file
// name, so nothing else is ever allowed.
func ValidateAppName(name string) error {
	if !appNamePattern.MatchString(name) {
		return invalid("nom d’application invalide : minuscules, chiffres, - et _, 63 caractères au plus, sans commencer par - ou _")
	}
	return nil
}

// ValidateVarName accepts the names a shell accepts for a variable.
func ValidateVarName(name string) error {
	if !varNamePattern.MatchString(name) {
		return invalid("nom de variable invalide : lettres, chiffres et _, sans commencer par un chiffre, 128 caractères au plus")
	}
	return nil
}

func validateValue(value string) error {
	if len(value) > MaxValueBytes {
		return invalid("valeur trop longue (%d Kio au plus)", MaxValueBytes>>10)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return invalid("valeur invalide : texte UTF-8 sans octet nul attendu")
	}
	return nil
}

func validatePassphrase(passphrase string) error {
	length := utf8.RuneCountInString(passphrase)
	if length < MinPassphraseLength {
		return invalid("phrase de passe trop courte : %d caractères au moins", MinPassphraseLength)
	}
	if length > MaxPassphraseLength || !utf8.ValidString(passphrase) {
		return invalid("phrase de passe invalide")
	}
	return nil
}

// Restarter restarts a container by name. The server gives the one of the
// Containers view, with its guards; tests give a fake, never Docker.
type Restarter interface {
	RestartContainer(actor, name string) error
}

// Options configures a Vault.
type Options struct {
	// Dir is the vault directory, created 0700 on first use.
	Dir string
	// Home is the only directory under which a target file may be written.
	Home string
	// Forbidden lists directories under Home where no target may be written,
	// such as the hub's own configuration. Dir is always forbidden.
	Forbidden []string
	// Restarter may be nil: apply then refuses targets with a container.
	Restarter Restarter
	// Now defaults to time.Now.
	Now func() time.Time
	// UnlockTTL defaults to DefaultUnlockTTL.
	UnlockTTL time.Duration
	// ScryptWorkFactor is the log2 cost of the passphrase; 0 keeps the age
	// default. Only tests lower it.
	ScryptWorkFactor int
}

// Vault is safe for concurrent use.
type Vault struct {
	dir       string
	home      string
	forbidden []string
	restarter Restarter
	now       func() time.Time
	ttl       time.Duration
	workLogN  int

	maxLogN int
	// seals counts the passphrase derivations made to create a vault.
	seals int

	// unlockMu lets a single passphrase derivation run at a time: scrypt is
	// expensive by design and must not be multiplied by parallel requests.
	unlockMu sync.Mutex

	// mu guards the fields below and every read or write of the directory.
	mu sync.Mutex
	// identity is the private key, kept while at least one session holds an
	// unlock; sessions gives each of them its own expiry.
	identity *age.X25519Identity
	sessions map[string]time.Time
	// pinned is the public key read first from the vault file. Every value
	// is encrypted for it, and a file that later shows another key is
	// refused: whoever can write the file cannot redirect new values.
	pinned       string
	failures     int
	lastFailure  time.Time
	blockedUntil time.Time
}

// vaultDoc is vault.json.
type vaultDoc struct {
	Version   int    `json:"version"`
	PublicKey string `json:"public_key"`
	// Identity is the age X25519 private key, encrypted by the passphrase
	// (age scrypt) and armored.
	Identity  string    `json:"identity"`
	CreatedAt time.Time `json:"created_at"`
}

// New returns the vault stored in opts.Dir. Nothing is created before Init.
func New(opts Options) (*Vault, error) {
	if !filepath.IsAbs(opts.Dir) || !filepath.IsAbs(opts.Home) {
		return nil, errors.New("secrets: absolute vault and home directories required")
	}
	v := &Vault{
		dir: filepath.Clean(opts.Dir), home: filepath.Clean(opts.Home),
		restarter: opts.Restarter, now: opts.Now, ttl: opts.UnlockTTL, workLogN: opts.ScryptWorkFactor,
	}
	for _, dir := range opts.Forbidden {
		if filepath.IsAbs(dir) {
			v.forbidden = append(v.forbidden, filepath.Clean(dir))
		}
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.ttl <= 0 {
		v.ttl = DefaultUnlockTTL
	}
	v.sessions = make(map[string]time.Time)
	v.maxLogN = maxScryptLogN
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cleanTemporaryFiles()
	// The public key is pinned now if the vault exists; an unreadable vault
	// is reported by Status.
	_, _ = v.readVault()
	return v, nil
}

// cleanTemporaryFiles removes what a write interrupted by a crash left in
// the vault. v.mu is held.
func (v *Vault) cleanTemporaryFiles() {
	if _, err := os.Stat(v.dir); err != nil {
		return
	}
	_ = v.withFileLock(func() error {
		for _, pattern := range []string{filepath.Join(v.dir, ".tmp-*"), filepath.Join(v.dir, appsDir, ".tmp-*")} {
			leftovers, _ := filepath.Glob(pattern)
			for _, path := range leftovers {
				if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
					_ = os.Remove(path)
				}
			}
		}
		return nil
	})
}

// Status is the public state of the vault.
type Status struct {
	Initialized bool `json:"initialized"`
	// Unlocked is true for the session that asks, if it gave the passphrase.
	Unlocked bool `json:"unlocked"`
	// UnlockedElsewhere is true when another session holds an unlock.
	UnlockedElsewhere bool `json:"unlocked_elsewhere,omitempty"`
	// ExpiresAt is when this session's unlock ends; nil while locked.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Problem is set for a vault that exists but cannot be trusted or read.
	Problem string `json:"problem,omitempty"`
	// Now is the server clock, so the countdown does not depend on the
	// browser's.
	Now        time.Time `json:"now"`
	TTLSeconds int       `json:"ttl_seconds"`
	PublicKey  string    `json:"public_key,omitempty"`
	// RetryAfterSeconds is positive while unlock attempts are blocked.
	RetryAfterSeconds   int `json:"retry_after_seconds,omitempty"`
	MinPassphraseLength int `json:"min_passphrase_length"`
}

// Status is the state of the vault as session sees it. It does not fail for
// a missing vault (not initialized) nor for a corrupt one (Problem is set).
func (v *Vault) Status(session string) (Status, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	status := Status{Now: now, TTLSeconds: int(v.ttl / time.Second), MinPassphraseLength: MinPassphraseLength}
	doc, err := v.readVault()
	switch {
	case errors.Is(err, ErrNotInitialized):
		return status, nil
	case errors.Is(err, ErrCorrupt):
		status.Initialized = true
		status.Problem = err.Error()
		return status, nil
	case err != nil:
		return status, err
	}
	status.Initialized = true
	status.PublicKey = doc.PublicKey
	v.pruneLocked()
	for holder, expires := range v.sessions {
		if holder == session {
			expires := expires
			status.Unlocked = true
			status.ExpiresAt = &expires
		} else {
			status.UnlockedElsewhere = true
		}
	}
	if wait := v.blockedUntil.Sub(now); wait > 0 {
		status.RetryAfterSeconds = int((wait + time.Second - 1) / time.Second)
	}
	return status, nil
}

// pruneLocked ends the unlocks whose time is up, and forgets the private
// key with the last of them. v.mu is held.
func (v *Vault) pruneLocked() {
	now := v.now()
	for session, expires := range v.sessions {
		if !now.Before(expires) {
			delete(v.sessions, session)
		}
	}
	if len(v.sessions) == 0 {
		v.identity = nil
	}
}

// keyFor returns the private key if session holds an unlock. The key is
// never handed to a session that did not give the passphrase itself, even
// while another one has. v.mu is held.
func (v *Vault) keyFor(session string) (*age.X25519Identity, error) {
	v.pruneLocked()
	if v.identity == nil {
		return nil, ErrLocked
	}
	if _, holds := v.sessions[session]; !holds {
		return nil, ErrLockedHere
	}
	return v.identity, nil
}

func (v *Vault) forgetLocked() {
	v.identity = nil
	clear(v.sessions)
}

// Init creates the vault: a new X25519 identity whose private key is stored
// encrypted by the passphrase. The vault stays locked.
func (v *Vault) Init(actor, passphrase string) error {
	if err := validatePassphrase(passphrase); err != nil {
		return err
	}
	// An existing vault is refused before any costly derivation.
	v.mu.Lock()
	_, err := v.readVault()
	v.mu.Unlock()
	if err == nil {
		return ErrAlreadyInitialized
	} else if !errors.Is(err, ErrNotInitialized) {
		return err
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	// The derivation runs before the lock is taken: it lasts about a second.
	v.unlockMu.Lock()
	sealed, err := v.sealIdentity(identity, passphrase)
	v.unlockMu.Unlock()
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.withFileLock(func() error {
		if _, err := v.readVault(); err == nil {
			return ErrAlreadyInitialized
		} else if !errors.Is(err, ErrNotInitialized) {
			return err
		}
		if err := os.MkdirAll(filepath.Join(v.dir, appsDir), 0o700); err != nil {
			return err
		}
		doc := vaultDoc{Version: vaultVersion, PublicKey: identity.Recipient().String(), Identity: sealed, CreatedAt: v.now().UTC()}
		if err := writeJSONFile(filepath.Join(v.dir, vaultFile), doc); err != nil {
			return err
		}
		v.pinned = doc.PublicKey
		v.auditLocked(Entry{Actor: actor, Action: "init"})
		return nil
	})
}

func (v *Vault) sealIdentity(identity *age.X25519Identity, passphrase string) (string, error) {
	v.seals++
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return "", invalid("phrase de passe invalide")
	}
	if v.workLogN > 0 {
		recipient.SetWorkFactor(v.workLogN)
	}
	return encryptArmored([]byte(identity.String()+"\n"), recipient)
}

// Unlock keeps the private key in memory for the TTL, for session alone:
// every other session still has to give the passphrase itself. A session
// that unlocks again restarts its own TTL. done, when not nil, is closed
// when the session ends (logout, revocation): its unlock ends with it.
// An empty session is the one shared by a server without authentication.
func (v *Vault) Unlock(actor, session, passphrase string, done <-chan struct{}) (Status, error) {
	v.unlockMu.Lock()
	defer v.unlockMu.Unlock()

	v.mu.Lock()
	doc, err := v.readVault()
	if err == nil {
		if wait := v.blockedUntil.Sub(v.now()); wait > 0 {
			err = &ThrottledError{RetryAfter: wait}
			v.auditLocked(Entry{Actor: actor, Action: "unlock_blocked"})
		}
	}
	v.mu.Unlock()
	if err != nil {
		return Status{}, err
	}

	identity, openErr := openIdentity(doc, passphrase, v.maxLogN)

	v.mu.Lock()
	if openErr != nil {
		if errors.Is(openErr, ErrWrongPassphrase) {
			now := v.now()
			if !v.lastFailure.IsZero() && now.Sub(v.lastFailure) >= failureWindow {
				v.failures = 0
			}
			v.failures++
			v.lastFailure = now
			if v.failures >= freeAttempts {
				block := baseBlock << min(v.failures-freeAttempts, 10)
				if block > maxBlock {
					block = maxBlock
				}
				v.blockedUntil = now.Add(block)
			}
			v.auditLocked(Entry{Actor: actor, Action: "unlock_failed"})
		}
		v.mu.Unlock()
		return Status{}, openErr
	}
	v.failures = 0
	v.lastFailure = time.Time{}
	v.blockedUntil = time.Time{}
	v.identity = identity
	expires := v.now().Add(v.ttl)
	v.sessions[session] = expires
	v.auditLocked(Entry{Actor: actor, Action: "unlock"})
	v.mu.Unlock()

	// The unlock also ends when its time is up, not only at the next call,
	// and at once when the session ends.
	timer := time.NewTimer(v.ttl)
	go func() {
		defer timer.Stop()
		ended := false
		select {
		case <-timer.C:
		case <-done: // a nil channel never fires
			ended = true
		}
		v.mu.Lock()
		defer v.mu.Unlock()
		if ended && v.sessions[session].Equal(expires) {
			delete(v.sessions, session)
			v.auditLocked(Entry{Actor: actor, Action: "lock", Detail: "session terminée"})
		}
		v.pruneLocked()
	}()
	return v.Status(session)
}

// openIdentity decrypts the private key. Whatever fails in the passphrase
// layer is reported as a wrong passphrase; a key that does not match the
// stored public key is a corrupt vault.
func openIdentity(doc vaultDoc, passphrase string, maxLogN int) (*age.X25519Identity, error) {
	if passphrase == "" || len(passphrase) > 4*MaxPassphraseLength {
		return nil, ErrWrongPassphrase
	}
	scrypt, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	scrypt.SetMaxWorkFactor(maxLogN)
	plain, err := decryptArmored(doc.Identity, scrypt)
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(plain)))
	if err != nil || identity.Recipient().String() != doc.PublicKey {
		return nil, ErrCorrupt
	}
	return identity, nil
}

// Lock forgets the private key now, for every session.
func (v *Vault) Lock(actor string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked()
	if v.identity != nil {
		v.forgetLocked()
		v.auditLocked(Entry{Actor: actor, Action: "lock"})
	}
}

// readVault reads vault.json and holds it to the public key pinned at the
// first reading. v.mu is held.
func (v *Vault) readVault() (vaultDoc, error) {
	var doc vaultDoc
	raw, err := os.ReadFile(filepath.Join(v.dir, vaultFile))
	if errors.Is(err, os.ErrNotExist) {
		if v.pinned != "" {
			return doc, errVaultGone
		}
		return doc, ErrNotInitialized
	}
	if err != nil {
		return doc, err
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Version != vaultVersion || doc.Identity == "" {
		return doc, ErrCorrupt
	}
	if _, err := age.ParseX25519Recipient(doc.PublicKey); err != nil {
		return doc, ErrCorrupt
	}
	if v.pinned == "" {
		v.pinned = doc.PublicKey
	} else if doc.PublicKey != v.pinned {
		return doc, errKeyChanged
	}
	return doc, nil
}

// recipient is the pinned public key every value is encrypted for; the
// vault file is checked against it first. v.mu is held.
func (v *Vault) recipient() (*age.X25519Recipient, error) {
	if _, err := v.readVault(); err != nil {
		return nil, err
	}
	return age.ParseX25519Recipient(v.pinned)
}

// withFileLock serializes writers across processes too, like the accounts
// store. v.mu is held.
func (v *Vault) withFileLock(fn func() error) error {
	if err := os.MkdirAll(v.dir, 0o700); err != nil {
		return err
	}
	// The mode is enforced even on a directory that already existed.
	if err := os.Chmod(v.dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(v.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	return fn()
}

func encryptArmored(plain []byte, recipient age.Recipient) (string, error) {
	var out bytes.Buffer
	armored := armor.NewWriter(&out)
	writer, err := age.Encrypt(armored, recipient)
	if err != nil {
		return "", err
	}
	if _, err := writer.Write(plain); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	if err := armored.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func decryptArmored(armored string, identity age.Identity) ([]byte, error) {
	reader, err := age.Decrypt(armor.NewReader(strings.NewReader(armored)), identity)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(reader, MaxValueBytes+1))
}

// writeJSONFile writes value to path atomically, 0600.
func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(raw, '\n'))
}

// writeFileAtomic writes a 0600 file through a temporary file of the same
// directory and a rename, so a reader never sees a partial file.
func writeFileAtomic(path string, content []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(content)
	}
	if err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
