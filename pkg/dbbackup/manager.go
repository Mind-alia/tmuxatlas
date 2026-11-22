package dbbackup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sirupsen/logrus"
)

// Defaults of Options.
const (
	DefaultMinFreeBytes   = 1 << 30
	DefaultDumpTimeout    = time.Hour
	DefaultRestoreTimeout = 2 * time.Hour
	// tickInterval is the cadence of the scheduler.
	tickInterval = 30 * time.Second
	// errorLimit and outputLimit bound what a run keeps of a program's
	// error output.
	errorLimit  = 2 << 10
	outputLimit = 16 << 10
	// listedBackups bounds the backups listed per database.
	listedBackups = 100
)

// Container is one running container, as Docker names it.
type Container struct {
	ID    string
	Name  string
	Image string
}

// ExecSpec is a program run inside a container without a shell. Env adds
// variables to that process only.
type ExecSpec struct {
	Cmd    []string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Docker is all this package asks of Docker. The server gives it the
// Containers view, with its switches; tests give it a fake. Its errors must
// be fit to show: they never carry a variable of a container.
type Docker interface {
	// Problem says why Docker may not be used now, or "".
	Problem() string
	Running(ctx context.Context) ([]Container, error)
	// Env returns the environment of a container. It holds secrets.
	Env(ctx context.Context, id string) ([]string, error)
	// Exec returns the exit code of the program.
	Exec(ctx context.Context, id string, spec ExecSpec) (int, error)
}

// Options configures a Manager.
type Options struct {
	// Dir holds the backups: <data>/db-backups.
	Dir    string
	Docker Docker
	// RestoreEnabled is TMUXATLAS_DB_RESTORE=on.
	RestoreEnabled bool
	Now            func() time.Time
	// FreeBytes reads the free space under a directory.
	FreeBytes      func(dir string) (uint64, error)
	MinFreeBytes   uint64
	DumpTimeout    time.Duration
	RestoreTimeout time.Duration
}

// Error is a refusal with the HTTP status that fits it.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func refuse(status int, message string) *Error { return &Error{Status: status, Message: message} }

// Refusals a caller may want to tell apart.
var (
	ErrBusy            = refuse(http.StatusConflict, "une sauvegarde ou une restauration de cette base est déjà en cours")
	ErrRestoreBusy     = refuse(http.StatusConflict, "une restauration est déjà en cours")
	ErrRestoreDisabled = refuse(http.StatusForbidden, "restauration désactivée sur ce serveur (TMUXATLAS_DB_RESTORE=on pour l’activer)")
	ErrConfirm         = refuse(http.StatusBadRequest, "le nom saisi ne correspond pas au conteneur")
	ErrNoDatabase      = refuse(http.StatusNotFound, "aucune base de données détectée dans un conteneur en cours de ce nom")
)

// Failure is the last scheduled backup of a database, when it failed and no
// backup has succeeded since.
type Failure struct {
	At     time.Time `json:"at"`
	Detail string    `json:"detail"`
}

// entry is what the hub remembers of one database.
type entry struct {
	Settings
	// Since is when the schedule was last changed: no run is owed for a
	// time before it.
	Since time.Time `json:"since"`
	// LastAttempt is the start of the last scheduled run.
	LastAttempt time.Time `json:"last_attempt"`
	Failure     *Failure  `json:"failure,omitempty"`
}

type config struct {
	Version   int               `json:"version"`
	Databases map[string]*entry `json:"databases"`
}

// job is the run in progress on one container.
type job struct {
	kind    string // backup | restore
	origin  string
	started time.Time
	bytes   atomic.Int64 // uncompressed bytes dumped so far
}

// RestoreStatus is the restore in progress, or the last one since the hub
// started.
type RestoreStatus struct {
	State     string `json:"state"` // running | ok | failed
	Container string `json:"container"`
	Backup    string `json:"backup"`
	Actor     string `json:"actor,omitempty"`
	// Step: verify | safety-backup | restore | done
	Step       string `json:"step"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	// SafetyBackup is the backup taken just before the restore.
	SafetyBackup string `json:"safety_backup,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	// Output is the start of the client's error output.
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Manager runs the backups, the restores and the schedule.
type Manager struct {
	opts   Options
	store  *store
	docker Docker
	logger *logrus.Entry

	mu        sync.Mutex
	cfg       config
	jobs      map[string]*job
	restoring bool
	restore   *RestoreStatus
	// engines remembers what each container ID is ("" for no database), so
	// a listing inspects a container once.
	engines map[string]string
	ctx     context.Context

	// scheduled lets one scheduled backup run at a time.
	scheduled chan struct{}
	wg        sync.WaitGroup
}

// New opens the manager on opts.Dir, which it creates.
func New(opts Options) (*Manager, error) {
	if opts.Dir == "" || !filepath.IsAbs(opts.Dir) {
		return nil, errors.New("dossier de sauvegarde invalide")
	}
	if opts.Docker == nil {
		return nil, errors.New("accès Docker manquant")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.FreeBytes == nil {
		opts.FreeBytes = freeBytes
	}
	if opts.MinFreeBytes == 0 {
		opts.MinFreeBytes = DefaultMinFreeBytes
	}
	if opts.DumpTimeout <= 0 {
		opts.DumpTimeout = DefaultDumpTimeout
	}
	if opts.RestoreTimeout <= 0 {
		opts.RestoreTimeout = DefaultRestoreTimeout
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		opts: opts, store: &store{root: opts.Dir}, docker: opts.Docker,
		logger:  logrus.WithField("component", "dbbackup"),
		cfg:     config{Version: 1, Databases: map[string]*entry{}},
		jobs:    map[string]*job{},
		engines: map[string]string{},
		ctx:     context.Background(), scheduled: make(chan struct{}, 1),
	}
	if err := m.loadConfig(); err != nil {
		return nil, err
	}
	m.store.sweepTemp()
	return m, nil
}

func (m *Manager) configPath() string { return filepath.Join(m.opts.Dir, "config.json") }

func (m *Manager) loadConfig() error {
	data, err := os.ReadFile(m.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("configuration des sauvegardes illisible : %w", err)
	}
	for name, e := range cfg.Databases {
		if e == nil || !ValidName(name) || e.Settings.validate() != nil {
			continue
		}
		m.cfg.Databases[name] = e
	}
	return nil
}

// saveConfig is called with mu held.
func (m *Manager) saveConfig() {
	if err := writeJSONFile(m.configPath(), m.cfg); err != nil {
		m.logger.WithError(err).Warn("database backup settings not saved")
	}
}

func (m *Manager) now() time.Time { return m.opts.Now() }

// RestoreEnabled tells whether restores are allowed on this server.
func (m *Manager) RestoreEnabled() bool { return m.opts.RestoreEnabled }

// target is a database container about to be used. env holds its secrets
// and never leaves this package.
type target struct {
	Container
	engine string
	env    []string
}

// discover lists the databases among the running containers.
func (m *Manager) discover(ctx context.Context) ([]target, error) {
	running, err := m.docker.Running(ctx)
	if err != nil {
		return nil, err
	}
	var out []target
	seen := make(map[string]bool, len(running))
	for _, c := range running {
		if !ValidName(c.Name) {
			continue
		}
		seen[c.ID] = true
		m.mu.Lock()
		engine, known := m.engines[c.ID]
		m.mu.Unlock()
		if !known {
			engine = engineFromImage(c.Image)
			if engine == "" {
				env, err := m.docker.Env(ctx, c.ID)
				if err != nil {
					continue // asked again at the next listing
				}
				engine = engineFromEnv(env)
			}
			m.mu.Lock()
			m.engines[c.ID] = engine
			m.mu.Unlock()
		}
		if engine != "" {
			out = append(out, target{Container: c, engine: engine})
		}
	}
	m.mu.Lock()
	for id := range m.engines {
		if !seen[id] {
			delete(m.engines, id)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// resolve finds the running database container called name, with its
// environment.
func (m *Manager) resolve(ctx context.Context, name string) (target, error) {
	if !ValidName(name) {
		return target{}, errInvalidName
	}
	found, err := m.discover(ctx)
	if err != nil {
		return target{}, err
	}
	for _, t := range found {
		if t.Name != name {
			continue
		}
		env, err := m.docker.Env(ctx, t.ID)
		if err != nil {
			return target{}, err
		}
		t.env = env
		return t, nil
	}
	return target{}, ErrNoDatabase
}

func (m *Manager) acquire(name, kind, origin string) (*job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acquireLocked(name, kind, origin)
}

func (m *Manager) acquireLocked(name, kind, origin string) (*job, error) {
	if _, busy := m.jobs[name]; busy {
		return nil, ErrBusy
	}
	j := &job{kind: kind, origin: origin, started: m.now()}
	m.jobs[name] = j
	return j, nil
}

func (m *Manager) release(name string) {
	m.mu.Lock()
	delete(m.jobs, name)
	m.mu.Unlock()
}

// StartBackup starts a manual backup of the database container called name
// and returns once it is running; Wait, or the listing, tells the rest.
func (m *Manager) StartBackup(ctx context.Context, name string) error {
	if !ValidName(name) {
		return refuse(http.StatusBadRequest, errInvalidName.Error())
	}
	if _, err := m.resolve(ctx, name); err != nil {
		return err
	}
	j, err := m.acquire(name, "backup", OriginManual)
	if err != nil {
		return err
	}
	// The run outlives the request that asked for it.
	m.mu.Lock()
	base := m.ctx
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.release(name)
		m.finish(m.dump(base, name, OriginManual, j))
	}()
	return nil
}

// Backup runs one backup and waits for it.
func (m *Manager) Backup(ctx context.Context, name, origin string) (Record, error) {
	if !ValidName(name) {
		return Record{}, refuse(http.StatusBadRequest, errInvalidName.Error())
	}
	j, err := m.acquire(name, "backup", origin)
	if err != nil {
		return Record{}, err
	}
	defer m.release(name)
	rec := m.dump(ctx, name, origin, j)
	m.finish(rec)
	return rec, nil
}

// Wait returns once every run started so far has ended.
func (m *Manager) Wait() { m.wg.Wait() }

// dump runs one backup of name, whose job the caller holds. It always
// returns a record; the record of a failed run is stored without a dump.
func (m *Manager) dump(ctx context.Context, name, origin string, j *job) Record {
	started := m.now()
	rec := Record{Container: name, Origin: origin, State: StateFailed, StartedAt: started.UTC()}
	secret := ""
	done := func(problem string) Record {
		rec.DurationMS = m.now().Sub(started).Milliseconds()
		if problem != "" {
			rec.State, rec.Size, rec.SHA256 = StateFailed, 0, ""
			rec.Error = clip(scrub(problem, secret), errorLimit)
		}
		if rec.ID != "" {
			if err := m.store.writeRecord(rec); err != nil && rec.State == StateOK {
				// A dump without its record could never be checked.
				if dump, _, pathErr := m.store.paths(name, rec.ID); pathErr == nil {
					_ = os.Remove(dump)
				}
				rec.State, rec.Size, rec.SHA256 = StateFailed, 0, ""
				rec.Error = "enregistrement de la sauvegarde impossible : " + err.Error()
			}
		}
		return rec
	}

	dir, err := m.store.ensureDir(name)
	if err != nil {
		return done(err.Error())
	}
	if rec.ID, err = m.store.newID(name, started); err != nil {
		return done(err.Error())
	}
	if problem := m.docker.Problem(); problem != "" {
		return done(problem)
	}
	t, err := m.resolve(ctx, name)
	if err != nil {
		return done(err.Error())
	}
	rec.Engine = t.engine
	commands, err := dumpCommands(t.engine, t.env)
	if err != nil {
		return done(err.Error())
	}
	secret = commands[0].password
	if problem := m.spaceProblem(dir, name); problem != "" {
		return done(problem)
	}

	dumpPath, _, _ := m.store.paths(name, rec.ID)
	temp := tempPath(dumpPath)
	for i, c := range commands {
		rec.Tool = c.argv[0]
		result := m.runDump(ctx, t.ID, c, temp, j)
		if result.problem == "" {
			if err := os.Rename(temp, dumpPath); err != nil {
				_ = os.Remove(temp)
				return done("écriture de la sauvegarde impossible : " + err.Error())
			}
			rec.State, rec.Size, rec.SHA256 = StateOK, result.size, result.sha256
			return done("")
		}
		_ = os.Remove(temp)
		if result.missingTool && i < len(commands)-1 {
			continue
		}
		return done(result.problem)
	}
	return done("aucun outil de sauvegarde disponible")
}

// spaceProblem refuses a backup when the disk is nearly full: the minimum
// kept free, plus the size of the last complete backup.
func (m *Manager) spaceProblem(dir, name string) string {
	free, err := m.opts.FreeBytes(dir)
	if err != nil {
		return ""
	}
	needed := m.opts.MinFreeBytes
	if records, err := m.store.list(name); err == nil {
		for _, rec := range records {
			if rec.State == StateOK {
				needed += uint64(rec.Size)
				break
			}
		}
	}
	if free < needed {
		return fmt.Sprintf("espace disque insuffisant : %s libres, %s nécessaires", humanBytes(free), humanBytes(needed))
	}
	return ""
}

type dumpResult struct {
	size        int64
	sha256      string
	problem     string
	missingTool bool
}

// runDump streams the output of one dump program, compressed as it comes,
// into temp. The caller removes temp when problem is set.
func (m *Manager) runDump(ctx context.Context, containerID string, c command, temp string, j *job) dumpResult {
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return dumpResult{problem: "écriture de la sauvegarde impossible : " + err.Error()}
	}
	defer file.Close()
	hash := sha256.New()
	compressed := &countingWriter{w: io.MultiWriter(file, hash)}
	gz := gzip.NewWriter(compressed)
	j.bytes.Store(0)
	raw := &countingWriter{w: gz, progress: &j.bytes}
	stderr := &boundedBuffer{limit: errorLimit}

	runCtx, cancel := context.WithTimeout(ctx, m.opts.DumpTimeout)
	code, execErr := m.docker.Exec(runCtx, containerID, ExecSpec{Cmd: c.argv, Env: c.env, Stdout: raw, Stderr: stderr})
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	cancel()
	closeErr := gz.Close()
	if closeErr == nil {
		closeErr = file.Sync()
	}
	if closeErr == nil {
		closeErr = file.Close()
	}
	output := strings.TrimSpace(stderr.String())
	switch {
	case timedOut:
		return dumpResult{problem: fmt.Sprintf("%s n’a pas terminé en %s : sauvegarde abandonnée", c.argv[0], m.opts.DumpTimeout)}
	case execErr != nil:
		return dumpResult{
			problem:     "exécution de " + c.argv[0] + " impossible : " + execErr.Error(),
			missingTool: raw.n == 0 && toolMissing(execErr.Error()),
		}
	case code != 0:
		problem := fmt.Sprintf("%s a échoué (code %d)", c.argv[0], code)
		if output != "" {
			problem += " : " + output
		}
		return dumpResult{problem: problem, missingTool: raw.n == 0 && (code == 126 || code == 127 || toolMissing(output))}
	case raw.n == 0:
		return dumpResult{problem: c.argv[0] + " n’a rien produit : sauvegarde vide supprimée"}
	case closeErr != nil:
		return dumpResult{problem: "écriture de la sauvegarde impossible : " + closeErr.Error()}
	}
	return dumpResult{size: compressed.n, sha256: hex.EncodeToString(hash.Sum(nil))}
}

// toolMissing reads Docker's message for a program absent from the image.
func toolMissing(message string) bool {
	return strings.Contains(message, "executable file not found") || strings.Contains(message, "no such file or directory")
}

// finish records what a run changes for its database, applies the retention
// after a complete backup, and logs the run.
func (m *Manager) finish(rec Record) {
	keep := 0
	m.mu.Lock()
	e := m.cfg.Databases[rec.Container]
	switch {
	case rec.State == StateOK && rec.Origin != OriginPreRestore:
		keep = DefaultKeep
		if e != nil {
			keep = e.Keep
			if e.Failure != nil {
				e.Failure = nil
				m.saveConfig()
			}
		}
	case rec.State != StateOK && rec.Origin == OriginScheduled && e != nil:
		e.Failure = &Failure{At: rec.StartedAt, Detail: rec.Error}
		m.saveConfig()
	}
	m.mu.Unlock()

	entry := m.logger.WithFields(logrus.Fields{
		"container": rec.Container, "engine": rec.Engine, "origin": rec.Origin, "backup": rec.ID,
		"state": rec.State, "size": rec.Size, "duration_ms": rec.DurationMS,
	})
	if rec.State != StateOK {
		entry.WithField("error", rec.Error).Warn("database backup failed")
		return
	}
	entry.Info("database backup done")
	// The retention only ever follows a complete backup: a database whose
	// backups fail keeps its old ones.
	if keep > 0 {
		deleted, err := m.store.prune(rec.Container, keep)
		if err != nil {
			m.logger.WithError(err).WithField("container", rec.Container).Warn("database backup retention failed")
		}
		if len(deleted) > 0 {
			m.logger.WithFields(logrus.Fields{"container": rec.Container, "deleted": len(deleted), "keep": keep}).Info("database backup retention applied")
		}
	}
}

// Run drives the schedule until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	m.Tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Tick()
		}
	}
}

// Tick starts the scheduled backups that are due. A database owes at most
// one run, the last one its schedule names, however long the hub was away;
// one whose backup or restore is still running is looked at again at the
// next tick.
func (m *Manager) Tick() {
	now := m.now()
	type due struct {
		name string
		job  *job
	}
	var start []due
	m.mu.Lock()
	ctx := m.ctx
	names := make([]string, 0, len(m.cfg.Databases))
	for name := range m.cfg.Databases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := m.cfg.Databases[name]
		previous := e.previous(now)
		if previous.IsZero() || !previous.After(e.Since) || !previous.After(e.LastAttempt) {
			continue
		}
		j, err := m.acquireLocked(name, "backup", OriginScheduled)
		if err != nil {
			continue
		}
		e.LastAttempt = now
		start = append(start, due{name, j})
	}
	if len(start) > 0 {
		m.saveConfig()
	}
	m.mu.Unlock()
	for _, d := range start {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			defer m.release(d.name)
			m.scheduled <- struct{}{}
			defer func() { <-m.scheduled }()
			m.finish(m.dump(ctx, d.name, OriginScheduled, d.job))
		}()
	}
}

// Failures lists the databases whose last scheduled backup failed, for the
// alerts. A database whose schedule was turned off is no longer reported.
func (m *Manager) Failures() map[string]Failure {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Failure{}
	for name, e := range m.cfg.Databases {
		if e.Failure != nil && e.Mode != ModeOff {
			out[name] = *e.Failure
		}
	}
	return out
}

// SetSettings stores the schedule and the retention of a database.
func (m *Manager) SetSettings(name string, settings Settings) error {
	if !ValidName(name) {
		return refuse(http.StatusBadRequest, errInvalidName.Error())
	}
	if err := settings.validate(); err != nil {
		return refuse(http.StatusBadRequest, err.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.cfg.Databases[name]
	if e == nil {
		e = &entry{Settings: defaultSettings()}
		m.cfg.Databases[name] = e
	}
	if e.Mode != settings.Mode || e.Time != settings.Time || e.Weekday != settings.Weekday || e.Since.IsZero() {
		e.Since = m.now()
	}
	e.Settings = settings
	if settings.Mode == ModeOff {
		e.Failure = nil
	}
	m.saveConfig()
	return nil
}

// Delete removes one backup.
func (m *Manager) Delete(name, id string) error {
	if !ValidName(name) || !ValidID(id) {
		return refuse(http.StatusBadRequest, "sauvegarde invalide")
	}
	// Held as a job, so no restore reads the file meanwhile.
	if _, err := m.acquire(name, "delete", ""); err != nil {
		return err
	}
	defer m.release(name)
	return m.store.remove(name, id)
}

// Open returns a complete backup for a download. The caller closes the file.
func (m *Manager) Open(name, id string) (*os.File, Record, error) {
	if !ValidName(name) || !ValidID(id) {
		return nil, Record{}, refuse(http.StatusBadRequest, "sauvegarde invalide")
	}
	rec, path, err := m.store.get(name, id)
	if err != nil {
		return nil, Record{}, err
	}
	if rec.State != StateOK {
		return nil, Record{}, ErrNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Record{}, ErrNotFound
	}
	return file, rec, nil
}

// StartRestore checks a restore request and starts it. confirm is the
// container name typed again by the admin. The restore itself, with its
// checks of the file and its safety backup, reports through RestoreStatus.
func (m *Manager) StartRestore(name, id, confirm, actor string) error {
	if !m.opts.RestoreEnabled {
		return ErrRestoreDisabled
	}
	if !ValidName(name) || !ValidID(id) {
		return refuse(http.StatusBadRequest, "sauvegarde invalide")
	}
	if confirm != name {
		return ErrConfirm
	}
	if problem := m.docker.Problem(); problem != "" {
		return refuse(http.StatusServiceUnavailable, problem)
	}
	rec, path, err := m.store.get(name, id)
	if err != nil {
		return err
	}
	if rec.State != StateOK {
		return refuse(http.StatusConflict, "cette sauvegarde est incomplète : elle ne peut pas être restaurée")
	}
	m.mu.Lock()
	if m.restoring {
		m.mu.Unlock()
		return ErrRestoreBusy
	}
	j, err := m.acquireLocked(name, "restore", "")
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.restoring = true
	status := &RestoreStatus{State: "running", Container: name, Backup: id, Actor: actor, Step: "verify", StartedAt: m.now().Unix()}
	m.restore = status
	ctx := m.ctx
	m.mu.Unlock()

	m.logger.WithFields(logrus.Fields{"actor": actor, "container": name, "backup": id}).Warn("database restore started")
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		problem := m.runRestore(ctx, rec, path, j)
		m.mu.Lock()
		status.State, status.Step, status.Error = "ok", "done", ""
		if problem != "" {
			status.State, status.Error = "failed", problem
		}
		status.FinishedAt = m.now().Unix()
		final := *status
		m.restoring = false
		delete(m.jobs, name)
		m.mu.Unlock()
		entry := m.logger.WithFields(logrus.Fields{
			"actor": actor, "container": name, "backup": id, "state": final.State, "safety_backup": final.SafetyBackup,
		})
		if final.State != "ok" {
			entry.WithField("error", final.Error).Warn("database restore failed")
			return
		}
		entry.Warn("database restore done")
	}()
	return nil
}

func (m *Manager) setRestore(change func(*RestoreStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.restore != nil {
		change(m.restore)
	}
}

// runRestore verifies the file, takes the safety backup, then feeds the
// dump to the database client. It returns what went wrong, or "".
func (m *Manager) runRestore(ctx context.Context, rec Record, path string, j *job) string {
	// 1. The file is the one that was written.
	sum, err := fileSHA256(path)
	if err != nil {
		return "lecture de la sauvegarde impossible : " + err.Error()
	}
	if rec.SHA256 == "" || sum != rec.SHA256 {
		return "la somme SHA-256 du fichier ne correspond plus à celle enregistrée : fichier modifié ou abîmé, restauration refusée"
	}
	// 2. The container is there and is still the same kind of database.
	t, err := m.resolve(ctx, rec.Container)
	if err != nil {
		return err.Error()
	}
	if t.engine != rec.Engine {
		return fmt.Sprintf("cette sauvegarde vient d’une base %s, le conteneur est aujourd’hui une base %s", rec.Engine, t.engine)
	}
	commands, err := restoreCommands(t.engine, t.env)
	if err != nil {
		return err.Error()
	}
	// 3. The current state is saved first, or nothing is touched.
	m.setRestore(func(s *RestoreStatus) { s.Step = "safety-backup" })
	safety := m.dump(ctx, rec.Container, OriginPreRestore, j)
	m.finish(safety)
	if safety.State != StateOK {
		return "sauvegarde de sécurité en échec, rien n’a été restauré : " + safety.Error
	}
	m.setRestore(func(s *RestoreStatus) { s.Step, s.SafetyBackup = "restore", safety.ID })

	// 4. The restore.
	for i, c := range commands {
		code, output, truncated, problem, missing := m.runClient(ctx, t.ID, c, path)
		if missing && i < len(commands)-1 {
			continue
		}
		output = scrub(output, c.password)
		m.setRestore(func(s *RestoreStatus) {
			s.Output, s.OutputTruncated = output, truncated
			if code >= 0 {
				s.ExitCode = &code
			}
		})
		return scrub(problem, c.password)
	}
	return "aucun client de base de données disponible dans le conteneur"
}

// runClient feeds the decompressed dump to one client program.
func (m *Manager) runClient(ctx context.Context, containerID string, c command, path string) (code int, output string, truncated bool, problem string, missing bool) {
	file, err := os.Open(path)
	if err != nil {
		return -1, "", false, "lecture de la sauvegarde impossible : " + err.Error(), false
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return -1, "", false, "sauvegarde illisible : " + err.Error(), false
	}
	defer gz.Close()
	stderr := &boundedBuffer{limit: outputLimit}
	fed := &countingReader{r: gz}
	runCtx, cancel := context.WithTimeout(ctx, m.opts.RestoreTimeout)
	code, execErr := m.docker.Exec(runCtx, containerID, ExecSpec{Cmd: c.argv, Env: c.env, Stdin: fed, Stdout: io.Discard, Stderr: stderr})
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	cancel()
	output = strings.TrimSpace(stderr.String())
	switch {
	case timedOut:
		return -1, output, stderr.truncated, fmt.Sprintf("%s n’a pas terminé en %s : la base peut être dans un état partiel", c.argv[0], m.opts.RestoreTimeout), false
	case execErr != nil:
		return -1, output, stderr.truncated, "exécution de " + c.argv[0] + " impossible : " + execErr.Error(), fed.n == 0 && toolMissing(execErr.Error())
	case code != 0:
		missing = fed.n == 0 && (code == 126 || code == 127 || toolMissing(output))
		return code, output, stderr.truncated, fmt.Sprintf("%s a échoué (code %d) : la base peut être dans un état partiel", c.argv[0], code), missing
	}
	return code, output, stderr.truncated, "", false
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Views sent to the browser.

// RunningView is the run in progress on a database.
type RunningView struct {
	Kind      string `json:"kind"` // backup | restore
	Origin    string `json:"origin,omitempty"`
	StartedAt int64  `json:"started_at"`
	// Bytes is the uncompressed size dumped so far.
	Bytes int64 `json:"bytes"`
}

// Database is one card of the panel.
type Database struct {
	Container string `json:"container"`
	Engine    string `json:"engine,omitempty"`
	Image     string `json:"image,omitempty"`
	// Present is true while a running container of this name is a database.
	Present  bool         `json:"present"`
	Settings Settings     `json:"settings"`
	NextRun  int64        `json:"next_run,omitempty"`
	Running  *RunningView `json:"running,omitempty"`
	Failure  *Failure     `json:"failure,omitempty"`
	Backups  []Record     `json:"backups"`
	// MoreBackups counts the older backups left out of Backups.
	MoreBackups int `json:"more_backups,omitempty"`
}

// View is the payload of GET /api/db-backups.
type View struct {
	// Available is false when Docker cannot be asked; stored backups are
	// still listed.
	Available      bool           `json:"available"`
	Error          string         `json:"error,omitempty"`
	RestoreEnabled bool           `json:"restore_enabled"`
	Dir            string         `json:"dir"`
	FreeBytes      uint64         `json:"free_bytes,omitempty"`
	MinFreeBytes   uint64         `json:"min_free_bytes"`
	Databases      []Database     `json:"databases"`
	Restore        *RestoreStatus `json:"restore,omitempty"`
	Now            int64          `json:"now"`
}

// List describes every database known here: those running now, those with
// stored backups and those with a schedule.
func (m *Manager) List(ctx context.Context) View {
	now := m.now()
	view := View{RestoreEnabled: m.opts.RestoreEnabled, Dir: m.opts.Dir, MinFreeBytes: m.opts.MinFreeBytes, Databases: []Database{}, Now: now.Unix()}
	if free, err := m.opts.FreeBytes(m.opts.Dir); err == nil {
		view.FreeBytes = free
	}
	byName := map[string]*Database{}
	add := func(name string) *Database {
		if db, ok := byName[name]; ok {
			return db
		}
		db := &Database{Container: name, Settings: defaultSettings(), Backups: []Record{}}
		byName[name] = db
		return db
	}
	if problem := m.docker.Problem(); problem != "" {
		view.Error = problem
	} else if found, err := m.discover(ctx); err != nil {
		view.Error = err.Error()
	} else {
		view.Available = true
		for _, t := range found {
			db := add(t.Name)
			db.Engine, db.Image, db.Present = t.engine, t.Image, true
		}
	}
	for _, name := range m.store.containers() {
		add(name)
	}
	m.mu.Lock()
	for name, e := range m.cfg.Databases {
		db := add(name)
		db.Settings = e.Settings
		if next := e.next(now); !next.IsZero() {
			db.NextRun = next.Unix()
		}
		if e.Failure != nil && e.Mode != ModeOff {
			failure := *e.Failure
			db.Failure = &failure
		}
	}
	for name, j := range m.jobs {
		if j.kind == "delete" {
			continue
		}
		add(name).Running = &RunningView{Kind: j.kind, Origin: j.origin, StartedAt: j.started.Unix(), Bytes: j.bytes.Load()}
	}
	if m.restore != nil {
		status := *m.restore
		view.Restore = &status
	}
	m.mu.Unlock()
	for name, db := range byName {
		records, _ := m.store.list(name)
		if len(records) > listedBackups {
			db.MoreBackups = len(records) - listedBackups
			records = records[:listedBackups]
		}
		if records != nil {
			db.Backups = records
		}
		if db.Engine == "" && len(records) > 0 {
			for _, rec := range records {
				if rec.Engine != "" {
					db.Engine = rec.Engine
					break
				}
			}
		}
		view.Databases = append(view.Databases, *db)
	}
	sort.Slice(view.Databases, func(i, j int) bool { return view.Databases[i].Container < view.Databases[j].Container })
	return view
}

// Known tells whether name is a database of the listing: settings are only
// kept for those.
func (m *Manager) Known(ctx context.Context, name string) bool {
	for _, db := range m.List(ctx).Databases {
		if db.Container == name {
			return true
		}
	}
	return false
}

// Small helpers.

type countingWriter struct {
	w        io.Writer
	n        int64
	progress *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if c.progress != nil {
		c.progress.Store(c.n)
	}
	return n, err
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// boundedBuffer keeps the first limit bytes written to it. Exec writes to it
// from one goroutine; the mutex covers a reader that looks meanwhile.
type boundedBuffer struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - len(b.data)
	if room < len(p) {
		b.truncated = true
	}
	if room > 0 {
		b.data = append(b.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// String returns the text kept, made safe to show: valid UTF-8 and no
// control character but the line break and the tab.
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, string(b.data))
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d octets", n)
	}
	value, suffix := float64(n), ""
	for _, s := range []string{"Kio", "Mio", "Gio", "Tio"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
