// Package loops manages iterative AI improvement loops across local project
// directories and launches isolated Claude, Codex and AGY chat sessions.
package loops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

const (
	CLIClaude = "claude"
	CLICodex  = "codex"
	CLIAgy    = "agy"

	StatusIdle         = "idle"
	StatusRunning      = "running"
	StatusWaitingReset = "waiting_reset"
	StatusPaused       = "paused"
	StatusCompleted    = "completed"
	StatusStopped      = "stopped"
	StatusError        = "error"

	WorkerReady       = "ready"
	WorkerRunning     = "running"
	WorkerPaused      = "paused"
	WorkerRateLimited = "rate_limited"
	WorkerError       = "error"
	WorkerDisabled    = "disabled"

	defaultProjectDirs = "~/Project,~/projects,~/code,~/src,~/dev"
	maxProjects        = 200
	maxLogTailBytes    = 8192
	maxIterationsLimit = 1000
	maxDelaySeconds    = 3600
)

var (
	modelPattern  = regexp.MustCompile(`^[a-zA-Z0-9._:/-]{1,120}$`)
	personPattern = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,80}$`)
	slugUnsafe    = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
)

// Enabled reads TMUXATLAS_LOOPS. Loops stay on unless explicitly disabled.
func Enabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "0", "no":
		return false
	default:
		return true
	}
}

// ValidCLI reports whether cli is one of claude, codex, or agy.
func ValidCLI(cli string) bool {
	switch cli {
	case CLIClaude, CLICodex, CLIAgy:
		return true
	default:
		return false
	}
}

// Profile describes one account registered in ai-accounts for a single CLI.
type Profile struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Person   string `json:"person,omitempty"`
	Account  string `json:"account,omitempty"`
}

// ProjectEntry describes a discovered local project directory.
type ProjectEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsGit  bool   `json:"is_git"`
	Branch string `json:"branch,omitempty"`
}

// Preset is a ready-to-use improvement loop prompt template.
type Preset struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
}

// CLIAvailability reports whether each CLI binary is reachable on PATH.
type CLIAvailability struct {
	CLI       string `json:"cli"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
}

// IterationRecord records the outcome of a single loop pass.
type IterationRecord struct {
	Number       int        `json:"number"`
	Status       string     `json:"status"`
	WorkerID     string     `json:"worker_id,omitempty"`
	CLI          string     `json:"cli,omitempty"`
	Person       string     `json:"person,omitempty"`
	WorkerLabel  string     `json:"worker_label,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMs   int64      `json:"duration_ms"`
	ExitCode     int        `json:"exit_code"`
	ChangedFiles int        `json:"changed_files"`
	DiffStat     string     `json:"diff_stat,omitempty"`
	LogTail      string     `json:"log_tail,omitempty"`
	DoneSignal   bool       `json:"done_signal,omitempty"`
	RateLimited  bool       `json:"rate_limited,omitempty"`
	ResetAt      *time.Time `json:"reset_at,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// LoopWorker represents one CLI subscription assigned to a loop.
type LoopWorker struct {
	ID             string     `json:"id"`
	CLI            string     `json:"cli"`
	Person         string     `json:"person,omitempty"`
	Label          string     `json:"label,omitempty"`
	Model          string     `json:"model,omitempty"`
	Enabled        bool       `json:"enabled"`
	Paused         bool       `json:"paused"`
	PauseRequested bool       `json:"pause_requested,omitempty"`
	Status         string     `json:"status"`
	RateLimitedAt  *time.Time `json:"rate_limited_at,omitempty"`
	ResetAt        *time.Time `json:"reset_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	RunCount       int        `json:"run_count"`
}

// WorkerInput specifies a CLI subscription when creating or updating a loop.
type WorkerInput struct {
	CLI     string `json:"cli"`
	Person  string `json:"person,omitempty"`
	Label   string `json:"label,omitempty"`
	Model   string `json:"model,omitempty"`
	Enabled bool   `json:"enabled"`
	Paused  bool   `json:"paused,omitempty"`
}

// WorkerActionRequest toggles, pauses, resumes, or retries a loop worker.
type WorkerActionRequest struct {
	LoopID   string `json:"loop_id"`
	WorkerID string `json:"worker_id,omitempty"`
	Action   string `json:"action"`
	CLI      string `json:"cli,omitempty"`
	Person   string `json:"person,omitempty"`
	Label    string `json:"label,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Loop represents a configured and/or running improvement loop on a project.
type Loop struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ProjectDir       string            `json:"project_dir"`
	WorkDir          string            `json:"work_dir"`
	Branch           string            `json:"branch,omitempty"`
	CLI              string            `json:"cli"`
	Person           string            `json:"person,omitempty"`
	Model            string            `json:"model,omitempty"`
	Prompt           string            `json:"prompt"`
	MaxIterations    int               `json:"max_iterations"`
	DelaySeconds     int               `json:"delay_seconds"`
	AutoApprove      bool              `json:"auto_approve"`
	UseWorktree      bool              `json:"use_worktree"`
	StopWhenDone     bool              `json:"stop_when_done"`
	OwnerID          string            `json:"owner_id,omitempty"`
	OwnerName        string            `json:"owner_name,omitempty"`
	Status           string            `json:"status"`
	CurrentIteration int               `json:"current_iteration"`
	PauseRequested   bool              `json:"pause_requested,omitempty"`
	AutoPaused       bool              `json:"auto_paused,omitempty"`
	ActiveWorkerID   string            `json:"active_worker_id,omitempty"`
	NextResetAt      *time.Time        `json:"next_reset_at,omitempty"`
	Workers          []LoopWorker      `json:"workers"`
	LastSummary      string            `json:"last_summary,omitempty"`
	LastError        string            `json:"last_error,omitempty"`
	LiveOutput       string            `json:"live_output,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	Iterations       []IterationRecord `json:"iterations"`
}

// CreateRequest configures a new improvement loop.
type CreateRequest struct {
	Name          string        `json:"name"`
	ProjectDir    string        `json:"project_dir"`
	CLI           string        `json:"cli"`
	Person        string        `json:"person,omitempty"`
	Model         string        `json:"model,omitempty"`
	Workers       []WorkerInput `json:"workers,omitempty"`
	Prompt        string        `json:"prompt"`
	MaxIterations int           `json:"max_iterations"`
	Continuous    *bool         `json:"continuous,omitempty"`
	DelaySeconds  int           `json:"delay_seconds"`
	AutoApprove   *bool         `json:"auto_approve,omitempty"`
	UseWorktree   bool          `json:"use_worktree"`
	StopWhenDone  *bool         `json:"stop_when_done,omitempty"`
	StartNow      bool          `json:"start_now"`
	OwnerID       string        `json:"-"`
	OwnerName     string        `json:"-"`
}

// UpdateRequest edits an idle/stopped/completed loop.
type UpdateRequest struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	CLI           string        `json:"cli"`
	Person        string        `json:"person,omitempty"`
	Model         string        `json:"model,omitempty"`
	Workers       []WorkerInput `json:"workers,omitempty"`
	Prompt        string        `json:"prompt"`
	MaxIterations int           `json:"max_iterations"`
	Continuous    *bool         `json:"continuous,omitempty"`
	DelaySeconds  int           `json:"delay_seconds"`
	AutoApprove   *bool         `json:"auto_approve,omitempty"`
	StopWhenDone  *bool         `json:"stop_when_done,omitempty"`
}

// ChatRequest opens a tmux session running claude, codex, or agy.
type ChatRequest struct {
	CLI       string `json:"cli"`
	Cwd       string `json:"cwd"`
	Person    string `json:"person,omitempty"`
	Model     string `json:"model,omitempty"`
	Session   string `json:"session,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	Reuse     bool   `json:"reuse,omitempty"`
	OwnerID   string `json:"-"`
	OwnerName string `json:"-"`
}

// ChatResponse identifies the tmux session created or opened for a chat.
type ChatResponse struct {
	HostID  string `json:"host_id"`
	Session string `json:"session"`
	Key     string `json:"key"`
	Cwd     string `json:"cwd"`
	CLI     string `json:"cli"`
	Person  string `json:"person,omitempty"`
	Reused  bool   `json:"reused,omitempty"`
}

// MetaResponse describes available projects, accounts per CLI, CLIs, presets, and subscriptions.
type MetaResponse struct {
	HostID        string               `json:"host_id"`
	Projects      []ProjectEntry       `json:"projects"`
	Accounts      map[string][]Profile `json:"accounts"`
	CLIs          []CLIAvailability    `json:"clis"`
	Subscriptions []LoopWorker         `json:"subscriptions"`
	Presets       []Preset             `json:"presets"`
}

// LogResponse returns the full output of a loop iteration and current git diff info.
type LogResponse struct {
	LoopID     string `json:"loop_id"`
	Iteration  int    `json:"iteration"`
	Content    string `json:"content"`
	GitStatus  string `json:"git_status,omitempty"`
	GitDiff    string `json:"git_diff,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type activeRun struct {
	cancel         context.CancelFunc
	pauseRequested bool
	liveTail       string
	wakeCh         chan struct{}
}

// Manager persists loops, runs background iterations, and opens chat sessions.
type Manager struct {
	mu               sync.Mutex
	wg               sync.WaitGroup
	rootDir          string
	logsDir          string
	worktreesDir     string
	accountsDir      string
	projectDirsSpec  string
	tmuxClient       *tmux.Client
	localHostID      string
	onSessionCreated func()
	loops            map[string]*Loop
	active           map[string]*activeRun
	baseCtx          context.Context
	baseCancel       context.CancelFunc
}

// Options configures optional dependencies for Manager.
type Options struct {
	AccountsDir      string
	ProjectDirsSpec  string
	LocalHostID      string
	OnSessionCreated func()
}

// NewManager initializes the loops manager, loading persisted loops from rootDir.
func NewManager(rootDir string, tmuxClient *tmux.Client, opts Options) (*Manager, error) {
	if rootDir == "" {
		return nil, errors.New("dossier de stockage des boucles requis")
	}
	logsDir := filepath.Join(rootDir, "logs")
	worktreesDir := filepath.Join(rootDir, "worktrees")
	for _, dir := range []string{rootDir, logsDir, worktreesDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("création du dossier %s: %w", dir, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		rootDir:          rootDir,
		logsDir:          logsDir,
		worktreesDir:     worktreesDir,
		accountsDir:      opts.AccountsDir,
		projectDirsSpec:  opts.ProjectDirsSpec,
		tmuxClient:       tmuxClient,
		localHostID:      opts.LocalHostID,
		onSessionCreated: opts.OnSessionCreated,
		loops:            make(map[string]*Loop),
		active:           make(map[string]*activeRun),
		baseCtx:          ctx,
		baseCancel:       cancel,
	}
	if err := m.load(); err != nil {
		cancel()
		return nil, err
	}
	return m, nil
}

// Shutdown cancels any active loops and waits for their goroutines to exit.
func (m *Manager) Shutdown() {
	m.baseCancel()
	m.wg.Wait()
}

func (m *Manager) statePath() string {
	return filepath.Join(m.rootDir, "loops.json")
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lecture de loops.json: %w", err)
	}
	var list []*Loop
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("décodage de loops.json: %w", err)
	}
	changed := false
	for _, loop := range list {
		if loop == nil || loop.ID == "" {
			continue
		}
		if loop.Iterations == nil {
			loop.Iterations = []IterationRecord{}
		}
		if len(loop.Workers) == 0 && loop.CLI != "" {
			loop.Workers = []LoopWorker{{
				ID:      makeWorkerID(loop.CLI, loop.Person),
				CLI:     loop.CLI,
				Person:  loop.Person,
				Model:   loop.Model,
				Enabled: true,
				Status:  WorkerReady,
			}}
			changed = true
		}
		for i := range loop.Workers {
			if loop.Workers[i].ID == "" {
				loop.Workers[i].ID = makeWorkerID(loop.Workers[i].CLI, loop.Workers[i].Person)
				changed = true
			}
			if loop.Workers[i].Status == WorkerRunning {
				loop.Workers[i].Status = normalizeWorkerStatus(loop.Workers[i], time.Now())
				loop.Workers[i].PauseRequested = false
				changed = true
			}
		}
		if loop.Status == StatusRunning || loop.Status == StatusWaitingReset {
			loop.Status = StatusStopped
			loop.PauseRequested = false
			loop.ActiveWorkerID = ""
			loop.NextResetAt = nil
			loop.LastError = "Interrompue au redémarrage du serveur."
			now := time.Now()
			loop.FinishedAt = &now
			loop.UpdatedAt = now
			for i := range loop.Iterations {
				if loop.Iterations[i].Status == StatusRunning {
					loop.Iterations[i].Status = StatusStopped
					loop.Iterations[i].FinishedAt = &now
				}
			}
			changed = true
		}
		m.loops[loop.ID] = loop
	}
	if changed {
		_ = m.saveLocked()
	}
	return nil
}

func (m *Manager) saveLocked() error {
	list := make([]*Loop, 0, len(m.loops))
	for _, loop := range m.loops {
		list = append(list, loop)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(m.rootDir, "loops-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, m.statePath())
}

func makeWorkerID(cli, person string) string {
	p := strings.TrimSpace(person)
	if p == "" {
		return cli
	}
	return cli + ":" + p
}

func workerDisplayLabel(w LoopWorker) string {
	if w.Label != "" {
		return w.Label
	}
	if w.Person != "" {
		return w.CLI + " · " + w.Person
	}
	return w.CLI
}

func normalizeWorkerStatus(w LoopWorker, now time.Time) string {
	if !w.Enabled {
		return WorkerDisabled
	}
	if w.Paused {
		return WorkerPaused
	}
	if w.ResetAt != nil && w.ResetAt.After(now) {
		return WorkerRateLimited
	}
	if w.Status == WorkerError {
		return WorkerError
	}
	return WorkerReady
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneLoop(src *Loop, run *activeRun) Loop {
	copyLoop := *src
	copyLoop.NextResetAt = cloneTimePtr(src.NextResetAt)
	copyLoop.StartedAt = cloneTimePtr(src.StartedAt)
	copyLoop.FinishedAt = cloneTimePtr(src.FinishedAt)
	if src.Iterations != nil {
		copyLoop.Iterations = append([]IterationRecord(nil), src.Iterations...)
	} else {
		copyLoop.Iterations = []IterationRecord{}
	}
	if src.Workers != nil {
		copyLoop.Workers = make([]LoopWorker, len(src.Workers))
		for i, w := range src.Workers {
			w.RateLimitedAt = cloneTimePtr(w.RateLimitedAt)
			w.ResetAt = cloneTimePtr(w.ResetAt)
			w.LastRunAt = cloneTimePtr(w.LastRunAt)
			copyLoop.Workers[i] = w
		}
	} else {
		copyLoop.Workers = []LoopWorker{}
	}
	if run != nil {
		copyLoop.PauseRequested = run.pauseRequested
		copyLoop.LiveOutput = run.liveTail
	}
	return copyLoop
}

func canAccess(loop *Loop, ownerID string, isAdmin bool) bool {
	if isAdmin || ownerID == "" || loop.OwnerID == "" {
		return true
	}
	return loop.OwnerID == ownerID
}

// List returns all loops accessible to the caller, newest first.
func (m *Manager) List(ownerID string, isAdmin bool) []Loop {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Loop, 0, len(m.loops))
	for _, loop := range m.loops {
		if canAccess(loop, ownerID, isAdmin) {
			out = append(out, cloneLoop(loop, m.active[loop.ID]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		activeI := out[i].Status == StatusRunning || out[i].Status == StatusWaitingReset
		activeJ := out[j].Status == StatusRunning || out[j].Status == StatusWaitingReset
		if activeI != activeJ {
			return activeI
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Get returns a single loop by ID.
func (m *Manager) Get(id, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	loop, ok := m.loops[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		return nil, ErrForbidden
	}
	res := cloneLoop(loop, m.active[loop.ID])
	return &res, nil
}

func (m *Manager) buildWorkersFromInputs(inputs []WorkerInput) ([]LoopWorker, error) {
	seen := make(map[string]bool)
	workers := make([]LoopWorker, 0, len(inputs))
	enabledCount := 0
	for _, in := range inputs {
		if !ValidCLI(in.CLI) {
			return nil, fmt.Errorf("%w: CLI invalide (%s)", ErrInvalid, in.CLI)
		}
		person := strings.TrimSpace(in.Person)
		if err := m.validatePersonForCLI(in.CLI, person); err != nil {
			return nil, err
		}
		model := strings.TrimSpace(in.Model)
		if model != "" && !modelPattern.MatchString(model) {
			return nil, fmt.Errorf("%w: nom de modèle invalide", ErrInvalid)
		}
		id := makeWorkerID(in.CLI, person)
		if seen[id] {
			continue
		}
		seen[id] = true
		if in.Enabled {
			enabledCount++
		}
		w := LoopWorker{
			ID:      id,
			CLI:     in.CLI,
			Person:  person,
			Label:   strings.TrimSpace(in.Label),
			Model:   model,
			Enabled: in.Enabled,
			Paused:  in.Paused,
		}
		w.Status = normalizeWorkerStatus(w, time.Now())
		workers = append(workers, w)
	}
	if enabledCount == 0 {
		return nil, fmt.Errorf("%w: activez au moins un abonnement pour cette boucle", ErrInvalid)
	}
	return workers, nil
}

func primaryWorkerFor(workers []LoopWorker) (string, string) {
	for _, w := range workers {
		if w.Enabled && !w.Paused {
			return w.CLI, w.Person
		}
	}
	for _, w := range workers {
		if w.Enabled {
			return w.CLI, w.Person
		}
	}
	if len(workers) > 0 {
		return workers[0].CLI, workers[0].Person
	}
	return CLIClaude, ""
}

// Create validates and saves a new loop, and optionally starts it immediately.
func (m *Manager) Create(req CreateRequest) (*Loop, error) {
	dir, err := validateDir(req.ProjectDir)
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("%w: le prompt de la boucle est requis", ErrInvalid)
	}
	model := strings.TrimSpace(req.Model)
	if model != "" && !modelPattern.MatchString(model) {
		return nil, fmt.Errorf("%w: nom de modèle invalide", ErrInvalid)
	}

	var workers []LoopWorker
	var primaryCLI, primaryPerson string
	if len(req.Workers) > 0 {
		workers, err = m.buildWorkersFromInputs(req.Workers)
		if err != nil {
			return nil, err
		}
		primaryCLI, primaryPerson = primaryWorkerFor(workers)
	} else {
		if !ValidCLI(req.CLI) {
			return nil, fmt.Errorf("%w: CLI invalide (claude, codex ou agy)", ErrInvalid)
		}
		person := strings.TrimSpace(req.Person)
		if err := m.validatePersonForCLI(req.CLI, person); err != nil {
			return nil, err
		}
		primaryCLI = req.CLI
		primaryPerson = person
		workers = []LoopWorker{{
			ID:      makeWorkerID(primaryCLI, primaryPerson),
			CLI:     primaryCLI,
			Person:  primaryPerson,
			Enabled: true,
			Status:  WorkerReady,
		}}
	}

	maxIter := req.MaxIterations
	if req.Continuous != nil && *req.Continuous {
		maxIter = 0
	} else if maxIter <= 0 {
		maxIter = 0
	}
	if maxIter > maxIterationsLimit {
		maxIter = maxIterationsLimit
	}
	delay := req.DelaySeconds
	if delay < 0 {
		delay = 0
	}
	if delay > maxDelaySeconds {
		delay = maxDelaySeconds
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		if len(workers) > 1 {
			name = filepath.Base(dir) + " · continu"
		} else {
			name = filepath.Base(dir) + " · " + primaryCLI
		}
	}
	if len(name) > 80 {
		name = name[:80]
	}
	autoApprove := true
	if req.AutoApprove != nil {
		autoApprove = *req.AutoApprove
	}
	stopWhenDone := maxIter > 0
	if req.StopWhenDone != nil {
		stopWhenDone = *req.StopWhenDone
	}

	now := time.Now()
	id := newShortID()
	loop := &Loop{
		ID:            id,
		Name:          name,
		ProjectDir:    dir,
		WorkDir:       dir,
		CLI:           primaryCLI,
		Person:        primaryPerson,
		Model:         model,
		Workers:       workers,
		Prompt:        prompt,
		MaxIterations: maxIter,
		DelaySeconds:  delay,
		AutoApprove:   autoApprove,
		UseWorktree:   req.UseWorktree,
		StopWhenDone:  stopWhenDone,
		OwnerID:       req.OwnerID,
		OwnerName:     req.OwnerName,
		Status:        StatusIdle,
		CreatedAt:     now,
		UpdatedAt:     now,
		Iterations:    []IterationRecord{},
	}

	m.mu.Lock()
	m.loops[id] = loop
	if err := m.saveLocked(); err != nil {
		delete(m.loops, id)
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()

	if req.StartNow {
		return m.Start(id, req.OwnerID, true)
	}
	res := cloneLoop(loop, nil)
	return &res, nil
}

// Update modifies an inactive loop's settings.
func (m *Manager) Update(req UpdateRequest, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	loop, ok := m.loops[req.ID]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		return nil, ErrForbidden
	}
	if loop.Status == StatusRunning || loop.Status == StatusWaitingReset || m.active[loop.ID] != nil {
		return nil, fmt.Errorf("%w: arrêtez ou mettez en pause la boucle avant de la modifier", ErrConflict)
	}
	if len(req.Workers) > 0 {
		existing := make(map[string]LoopWorker, len(loop.Workers))
		for _, w := range loop.Workers {
			existing[w.ID] = w
		}
		built, err := m.buildWorkersFromInputs(req.Workers)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		for i := range built {
			if prev, ok := existing[built[i].ID]; ok {
				built[i].RunCount = prev.RunCount
				built[i].LastRunAt = cloneTimePtr(prev.LastRunAt)
				built[i].RateLimitedAt = cloneTimePtr(prev.RateLimitedAt)
				built[i].ResetAt = cloneTimePtr(prev.ResetAt)
				built[i].Status = normalizeWorkerStatus(built[i], now)
			}
		}
		loop.Workers = built
		loop.CLI, loop.Person = primaryWorkerFor(built)
	} else {
		if req.CLI != "" {
			if !ValidCLI(req.CLI) {
				return nil, fmt.Errorf("%w: CLI invalide", ErrInvalid)
			}
			loop.CLI = req.CLI
		}
		person := strings.TrimSpace(req.Person)
		if err := m.validatePersonForCLI(loop.CLI, person); err != nil {
			return nil, err
		}
		loop.Person = person
		if len(loop.Workers) <= 1 {
			loop.Workers = []LoopWorker{{
				ID:      makeWorkerID(loop.CLI, loop.Person),
				CLI:     loop.CLI,
				Person:  loop.Person,
				Enabled: true,
				Status:  WorkerReady,
			}}
		}
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		if !modelPattern.MatchString(model) {
			return nil, fmt.Errorf("%w: modèle invalide", ErrInvalid)
		}
		loop.Model = model
	} else {
		loop.Model = ""
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		if len(name) > 80 {
			name = name[:80]
		}
		loop.Name = name
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		loop.Prompt = prompt
	}
	if req.Continuous != nil {
		if *req.Continuous {
			loop.MaxIterations = 0
		} else if req.MaxIterations > 0 {
			if req.MaxIterations > maxIterationsLimit {
				req.MaxIterations = maxIterationsLimit
			}
			loop.MaxIterations = req.MaxIterations
		}
	} else if req.MaxIterations > 0 {
		if req.MaxIterations > maxIterationsLimit {
			req.MaxIterations = maxIterationsLimit
		}
		loop.MaxIterations = req.MaxIterations
	}
	if req.DelaySeconds >= 0 {
		if req.DelaySeconds > maxDelaySeconds {
			req.DelaySeconds = maxDelaySeconds
		}
		loop.DelaySeconds = req.DelaySeconds
	}
	if req.AutoApprove != nil {
		loop.AutoApprove = *req.AutoApprove
	}
	if req.StopWhenDone != nil {
		loop.StopWhenDone = *req.StopWhenDone
	}
	loop.UpdatedAt = time.Now()
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	res := cloneLoop(loop, nil)
	return &res, nil
}

// Delete removes an inactive loop and its log files.
func (m *Manager) Delete(id, ownerID string, isAdmin bool) error {
	m.mu.Lock()
	loop, ok := m.loops[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		m.mu.Unlock()
		return ErrForbidden
	}
	if loop.Status == StatusRunning || loop.Status == StatusWaitingReset || m.active[id] != nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: arrêtez la boucle avant de la supprimer", ErrConflict)
	}
	worktree := ""
	if loop.UseWorktree && loop.WorkDir != "" && loop.WorkDir != loop.ProjectDir {
		worktree = loop.WorkDir
	}
	projectDir := loop.ProjectDir
	delete(m.loops, id)
	err := m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(m.logsDir, id))
	if worktree != "" {
		_ = exec.Command("git", "-C", projectDir, "worktree", "remove", "--force", worktree).Run()
		_ = os.RemoveAll(worktree)
	}
	return nil
}

// ReadLog reads the full output log of one iteration and current git status/diff.
func (m *Manager) ReadLog(id string, iter int, ownerID string, isAdmin bool) (LogResponse, error) {
	m.mu.Lock()
	loop, ok := m.loops[id]
	if !ok {
		m.mu.Unlock()
		return LogResponse{}, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		m.mu.Unlock()
		return LogResponse{}, ErrForbidden
	}
	if iter <= 0 {
		if len(loop.Iterations) > 0 {
			iter = loop.Iterations[len(loop.Iterations)-1].Number
		} else {
			iter = loop.CurrentIteration
		}
	}
	workDir := loop.WorkDir
	if workDir == "" {
		workDir = loop.ProjectDir
	}
	live := ""
	latestIter := 0
	if len(loop.Iterations) > 0 {
		latestIter = loop.Iterations[len(loop.Iterations)-1].Number
	}
	isRunningIter := loop.Status == StatusRunning && iter == latestIter
	if isRunningIter && m.active[id] != nil {
		live = m.active[id].liveTail
	}
	m.mu.Unlock()

	resp := LogResponse{LoopID: id, Iteration: iter}
	if iter > 0 {
		logPath := filepath.Join(m.logsDir, id, fmt.Sprintf("iter-%d.log", iter))
		if data, err := os.ReadFile(logPath); err == nil {
			const maxRead = 256 * 1024
			if len(data) > maxRead {
				data = data[len(data)-maxRead:]
				resp.Truncated = true
			}
			resp.Content = string(data)
		} else if live != "" {
			resp.Content = live
		}
	}
	if gitIsRepo(workDir) {
		resp.GitStatus, resp.GitDiff = gitStatusAndDiff(workDir)
	}
	return resp, nil
}

// Meta discovers projects, per-CLI accounts, CLI availability, subscriptions, and presets.
func (m *Manager) Meta() MetaResponse {
	accounts := LoadAccounts(m.accountsDir)
	clis := CheckCLIs()
	return MetaResponse{
		HostID:        m.localHostID,
		Projects:      DiscoverProjects(m.projectDirsSpec),
		Accounts:      accounts,
		CLIs:          clis,
		Subscriptions: discoverSubscriptions(m.accountsDir, accounts, clis),
		Presets:       DefaultPresets(),
	}
}

func discoverSubscriptions(accountsDir string, accounts map[string][]Profile, clis []CLIAvailability) []LoopWorker {
	availMap := make(map[string]bool, len(clis))
	for _, c := range clis {
		availMap[c.CLI] = c.Available
	}
	hasAccountsRoot := accountsDirExists(accountsDir)
	out := make([]LoopWorker, 0, 12)
	for _, cli := range []string{CLIClaude, CLICodex, CLIAgy} {
		profiles := accounts[cli]
		if len(profiles) > 0 {
			for _, prof := range profiles {
				person := strings.TrimSpace(prof.Person)
				label := strings.TrimSpace(prof.Name)
				if label == "" {
					label = person
				}
				out = append(out, LoopWorker{
					ID:      makeWorkerID(cli, person),
					CLI:     cli,
					Person:  person,
					Label:   label,
					Enabled: true,
					Status:  WorkerReady,
				})
			}
		} else if !hasAccountsRoot && availMap[cli] {
			out = append(out, LoopWorker{
				ID:      makeWorkerID(cli, ""),
				CLI:     cli,
				Label:   cli,
				Enabled: true,
				Status:  WorkerReady,
			})
		}
	}
	return out
}

// OpenChat creates (or reuses) a tmux session running claude, codex, or agy
// in the requested project directory with the chosen per-CLI account.
func (m *Manager) OpenChat(req ChatRequest) (*ChatResponse, error) {
	if m.tmuxClient == nil {
		return nil, errors.New("client tmux indisponible")
	}
	if !ValidCLI(req.CLI) {
		return nil, fmt.Errorf("%w: CLI invalide (claude, codex ou agy)", ErrInvalid)
	}
	cwd, err := validateDir(req.Cwd)
	if err != nil {
		return nil, err
	}
	person := strings.TrimSpace(req.Person)
	if err := m.validatePersonForCLI(req.CLI, person); err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model != "" && !modelPattern.MatchString(model) {
		return nil, fmt.Errorf("%w: nom de modèle invalide", ErrInvalid)
	}
	if _, err := exec.LookPath(req.CLI); err != nil {
		return nil, fmt.Errorf("%w: CLI introuvable sur cet hôte (%s)", ErrInvalid, req.CLI)
	}

	sessionName := strings.TrimSpace(req.Session)
	if sessionName != "" {
		if err := tmux.ValidateSessionName(sessionName); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if m.tmuxClient.HasSession(sessionName) {
			if req.Reuse {
				return &ChatResponse{
					HostID:  m.localHostID,
					Session: sessionName,
					Key:     sessionKey(m.localHostID, sessionName),
					Cwd:     cwd,
					CLI:     req.CLI,
					Person:  person,
					Reused:  true,
				}, nil
			}
			return nil, fmt.Errorf("%w: une session nommée %q existe déjà", ErrConflict, sessionName)
		}
	} else {
		sessionName = m.pickSessionName(filepath.Base(cwd) + "-" + req.CLI)
	}

	if req.OwnerID != "" {
		if err := m.tmuxClient.NewOwnedSession(sessionName, cwd, req.OwnerID, req.OwnerName); err != nil {
			return nil, err
		}
	} else {
		if err := m.tmuxClient.NewSessionIn(sessionName, cwd); err != nil {
			return nil, err
		}
	}

	if person != "" {
		_, _ = m.tmuxClient.Exec("set-option", "-t", tmux.ExactSession(sessionName), "@ai-person-"+req.CLI, person)
	}

	cmdLine := buildInteractiveCommand(req.CLI, person, model, strings.TrimSpace(req.Prompt))
	exact := tmux.ExactWindow(sessionName, "")
	if err := m.tmuxClient.SendLiteral(exact, cmdLine); err != nil {
		return nil, err
	}
	if err := m.tmuxClient.SendEnter(exact); err != nil {
		return nil, err
	}
	if m.onSessionCreated != nil {
		m.onSessionCreated()
	}

	return &ChatResponse{
		HostID:  m.localHostID,
		Session: sessionName,
		Key:     sessionKey(m.localHostID, sessionName),
		Cwd:     cwd,
		CLI:     req.CLI,
		Person:  person,
	}, nil
}

func (m *Manager) pickSessionName(base string) string {
	slug := strings.Trim(slugUnsafe.ReplaceAllString(base, "-"), "-")
	if slug == "" {
		slug = "chat"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if !m.tmuxClient.HasSession(slug) {
		return slug
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", slug, i)
		if !m.tmuxClient.HasSession(candidate) {
			return candidate
		}
	}
	return slug + "-" + newShortID()[:4]
}

func sessionKey(hostID, session string) string {
	if hostID == "" {
		return session
	}
	return hostID + "/" + session
}

func buildInteractiveCommand(cli, person, model, prompt string) string {
	var parts []string
	if person != "" {
		parts = append(parts, "AI_PERSON="+shellQuote(person))
	}
	parts = append(parts, cli)
	if model != "" {
		parts = append(parts, "--model", shellQuote(model))
	}
	if prompt != "" {
		if cli == CLIAgy {
			parts = append(parts, "-i", shellQuote(prompt))
		} else {
			parts = append(parts, shellQuote(prompt))
		}
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (m *Manager) validatePersonForCLI(cli, person string) error {
	if person == "" {
		return nil
	}
	if !personPattern.MatchString(person) {
		return fmt.Errorf("%w: nom de compte invalide", ErrInvalid)
	}
	accounts := LoadAccounts(m.accountsDir)
	profiles := accounts[cli]
	if len(profiles) == 0 {
		// If no ai-accounts directory exists on this machine, allow passing through.
		if !accountsDirExists(m.accountsDir) {
			return nil
		}
		return fmt.Errorf("%w: aucun compte %s configuré", ErrInvalid, cli)
	}
	for _, profile := range profiles {
		if strings.EqualFold(profile.Person, person) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s n'a pas de compte %s", ErrInvalid, person, cli)
}

func accountsDirPath(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if env := strings.TrimSpace(os.Getenv("AI_ACCOUNTS_DIR")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "ai-accounts")
}

func accountsDirExists(override string) bool {
	dir := accountsDirPath(override)
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// LoadAccounts reads ai-accounts profiles grouped strictly by CLI provider.
// Registering Aris2 for claude never adds Aris2 to codex or agy.
func LoadAccounts(overrideDir string) map[string][]Profile {
	result := map[string][]Profile{
		CLIClaude: {},
		CLICodex:  {},
		CLIAgy:    {},
	}
	root := accountsDirPath(overrideDir)
	if root == "" {
		return result
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, entry.Name(), "profile.json"))
		if err != nil {
			continue
		}
		var profile Profile
		if err := json.Unmarshal(raw, &profile); err != nil {
			continue
		}
		if !ValidCLI(profile.Provider) {
			continue
		}
		if !hasLocalAuthFile(filepath.Join(root, entry.Name()), profile.Provider) {
			continue
		}
		result[profile.Provider] = append(result[profile.Provider], profile)
	}
	for cli := range result {
		profiles := result[cli]
		sort.Slice(profiles, func(i, j int) bool {
			pi, pj := strings.ToLower(profiles[i].Person), strings.ToLower(profiles[j].Person)
			if pi != pj {
				return pi < pj
			}
			return strings.ToLower(profiles[i].Name) < strings.ToLower(profiles[j].Name)
		})
		result[cli] = profiles
	}
	return result
}

func hasLocalAuthFile(profileDir, provider string) bool {
	var file string
	switch provider {
	case CLIClaude:
		file = filepath.Join(profileDir, "config", ".credentials.json")
	case CLICodex:
		file = filepath.Join(profileDir, "config", "auth.json")
	case CLIAgy:
		file = filepath.Join(profileDir, "config", "antigravity-oauth-token")
	default:
		return false
	}
	info, err := os.Stat(file)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// CheckCLIs checks PATH availability of claude, codex, and agy.
func CheckCLIs() []CLIAvailability {
	labels := map[string]string{
		CLIClaude: "Claude Code",
		CLICodex:  "Codex CLI",
		CLIAgy:    "Antigravity (agy)",
	}
	out := make([]CLIAvailability, 0, 3)
	for _, cli := range []string{CLIClaude, CLICodex, CLIAgy} {
		path, err := exec.LookPath(cli)
		out = append(out, CLIAvailability{
			CLI:       cli,
			Label:     labels[cli],
			Available: err == nil && path != "",
			Path:      path,
		})
	}
	return out
}

// DiscoverProjects scans project roots up to 2 levels deep so both top-level
// repositories (~/Project/tmuxatlas) and grouped repositories (~/Project/Aris/StatQuest)
// are listed cleanly.
func DiscoverProjects(spec string) []ProjectEntry {
	if strings.TrimSpace(spec) == "" {
		spec = os.Getenv("TMUXATLAS_PROJECT_DIRS")
	}
	if strings.TrimSpace(spec) == "" {
		spec = defaultProjectDirs
	}
	home, _ := os.UserHomeDir()
	entries := []ProjectEntry{}
	seen := map[string]bool{}
	addEntry := func(name, full string) {
		clean := filepath.Clean(full)
		if seen[clean] {
			return
		}
		seen[clean] = true
		isGit := hasDotGit(clean)
		branch := ""
		if isGit {
			branch = gitCurrentBranch(clean)
		}
		entries = append(entries, ProjectEntry{
			Name:   name,
			Path:   clean,
			IsGit:  isGit,
			Branch: branch,
		})
	}

	for _, root := range strings.FieldsFunc(spec, func(r rune) bool { return r == ':' || r == ',' }) {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if root == "~" || strings.HasPrefix(root, "~/") {
			if home == "" {
				continue
			}
			root = filepath.Join(home, strings.TrimPrefix(root, "~"))
		}
		if !filepath.IsAbs(root) {
			continue
		}
		children, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), ".") {
				continue
			}
			full := filepath.Join(root, child.Name())
			info, err := os.Stat(full)
			if err != nil || !info.IsDir() {
				continue
			}
			if hasDotGit(full) {
				addEntry(child.Name(), full)
				continue
			}
			// Descend one level for grouped project folders (e.g. ~/Project/Aris/StatQuest),
			// skipping worktree scratch directories if empty or non-git.
			subEntries, err := os.ReadDir(full)
			addedSub := false
			if err == nil {
				for _, sub := range subEntries {
					if strings.HasPrefix(sub.Name(), ".") {
						continue
					}
					subPath := filepath.Join(full, sub.Name())
					subInfo, err := os.Stat(subPath)
					if err != nil || !subInfo.IsDir() {
						continue
					}
					if hasDotGit(subPath) {
						addEntry(child.Name()+"/"+sub.Name(), subPath)
						addedSub = true
					}
				}
			}
			if !addedSub {
				addEntry(child.Name(), full)
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a != b {
			return a < b
		}
		return entries[i].Path < entries[j].Path
	})
	if len(entries) > maxProjects {
		entries = entries[:maxProjects]
	}
	return entries
}

// DefaultPresets returns built-in improvement loop objectives.
func DefaultPresets() []Preset {
	return []Preset{
		{
			ID:          "quality-bugs",
			Label:       "Qualité & Bugs",
			Description: "Repérer les bugs réels, corriger proprement et valider avec les tests.",
			Prompt:      "Inspecte le projet, repère un bug réel, une edge-case non gérée ou une fragilité importante, corrige-la proprement, ajoute ou mets à jour les tests correspondants et vérifie que les tests passent.",
		},
		{
			ID:          "tests-coverage",
			Label:       "Tests & Robustesse",
			Description: "Ajouter des tests ciblés sur les chemins critiques non couverts.",
			Prompt:      "Analyse les modules critiques les moins testés du projet, écris des tests utiles et fiables couvrant des cas limites réels (sans tests factices), corrige tout défaut révélé et lance la suite de tests.",
		},
		{
			ID:          "perf-refactor",
			Label:       "Performance & Clarté",
			Description: "Simplifier la logique dupliquée et optimiser les chemins coûteux.",
			Prompt:      "Repère une duplication de logique, une fonction trop complexe ou un goulot de performance mesurable dans le projet, simplifie ou optimise-le sans changer les comportements attendus, et vérifie avec les tests et le build.",
		},
		{
			ID:          "security-audit",
			Label:       "Sécurité & Validation",
			Description: "Auditer entrées, permissions, erreurs et gestion des secrets.",
			Prompt:      "Audite les entrées utilisateur, permissions, chemins fichiers et gestions d'erreurs du projet. Corrige une faille ou faiblesse concrète, ajoute un test de non-régression et vérifie que tout passe.",
		},
		{
			ID:          "ui-ux",
			Label:       "UI / UX & Finitions",
			Description: "Améliorer l'ergonomie, les états vides/erreurs et le responsive.",
			Prompt:      "Examine l'interface utilisateur et ses parcours principaux, améliore un point concret d'ergonomie, d'accessibilité, de feedback erreur/chargement ou de responsive, et vérifie que les tests et le build passent.",
		},
	}
}

func validateDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsRune(raw, 0) || len(raw) > 4096 {
		return "", fmt.Errorf("%w: dossier projet invalide", ErrInvalid)
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("%w: dossier personnel introuvable", ErrInvalid)
		}
		raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%w: le chemin du projet doit être absolu", ErrInvalid)
	}
	clean := filepath.Clean(raw)
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("%w: dossier introuvable (%s)", ErrInvalid, clean)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: le chemin n'est pas un dossier", ErrInvalid)
	}
	return clean, nil
}

func hasDotGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func newShortID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var (
	ErrNotFound  = errors.New("boucle introuvable")
	ErrForbidden = errors.New("accès interdit")
	ErrInvalid   = errors.New("paramètre invalide")
	ErrConflict  = errors.New("conflit d'état")
)
