package loops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func signalWake(run *activeRun) {
	if run == nil || run.wakeCh == nil {
		return
	}
	select {
	case run.wakeCh <- struct{}{}:
	default:
	}
}

// Start begins or resumes a loop in the background.
func (m *Manager) Start(id, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	loop, ok := m.loops[id]
	if !ok {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		m.mu.Unlock()
		return nil, ErrForbidden
	}
	if loop.Status == StatusRunning || loop.Status == StatusWaitingReset || m.active[id] != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: la boucle est déjà en cours", ErrConflict)
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
	}
	workersCopy := append([]LoopWorker(nil), loop.Workers...)
	projectDir := loop.ProjectDir
	useWorktree := loop.UseWorktree
	existingWorkDir := loop.WorkDir
	existingBranch := loop.Branch
	wasPaused := loop.Status == StatusPaused
	m.mu.Unlock()

	accountsMap := LoadAccounts(m.accountsDir)
	enabledCount := 0
	availableCount := 0
	for i := range workersCopy {
		if !workersCopy[i].Enabled {
			continue
		}
		enabledCount++
		if _, err := exec.LookPath(workersCopy[i].CLI); err != nil {
			continue
		}
		if workersCopy[i].Person == "" {
			accounts := accountsMap[workersCopy[i].CLI]
			if len(accounts) == 1 && accounts[0].Person != "" {
				workersCopy[i].Person = accounts[0].Person
				workersCopy[i].ID = makeWorkerID(workersCopy[i].CLI, workersCopy[i].Person)
			} else if len(accounts) > 1 {
				return nil, fmt.Errorf("%w: choisissez un compte pour %s avant de démarrer la boucle", ErrInvalid, workersCopy[i].CLI)
			}
		}
		availableCount++
	}
	if enabledCount == 0 {
		return nil, fmt.Errorf("%w: activez au moins un abonnement avant de démarrer la boucle", ErrInvalid)
	}
	if availableCount == 0 {
		return nil, fmt.Errorf("%w: aucun CLI disponible sur cet hôte pour les abonnements activés", ErrInvalid)
	}

	workDir := projectDir
	branch := existingBranch
	if useWorktree && gitIsRepo(projectDir) {
		var err error
		workDir, branch, err = m.ensureWorktree(id, loop.Name, projectDir, existingWorkDir, existingBranch)
		if err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(m.baseCtx)
	run := &activeRun{cancel: cancel, wakeCh: make(chan struct{}, 1)}

	m.mu.Lock()
	// Re-check after non-locked worktree/path checks.
	if loop.Status == StatusRunning || loop.Status == StatusWaitingReset || m.active[id] != nil {
		m.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("%w: la boucle est déjà en cours", ErrConflict)
	}
	now := time.Now()
	loop.Workers = workersCopy
	unpausedEnabled := 0
	for i := range loop.Workers {
		w := &loop.Workers[i]
		w.PauseRequested = false
		if !w.Enabled {
			w.Status = WorkerDisabled
			continue
		}
		if !wasPaused {
			w.LastError = ""
			if w.ResetAt != nil && !w.ResetAt.After(now) {
				w.RateLimitedAt = nil
				w.ResetAt = nil
			}
		} else if w.ResetAt != nil && !w.ResetAt.After(now) {
			w.RateLimitedAt = nil
			w.ResetAt = nil
		}
		if !w.Paused {
			unpausedEnabled++
		}
	}
	// If all enabled workers were individually paused (e.g. AutoPaused), global Resume re-activates them.
	if unpausedEnabled == 0 {
		for i := range loop.Workers {
			if loop.Workers[i].Enabled {
				loop.Workers[i].Paused = false
			}
		}
	}
	for i := range loop.Workers {
		w := &loop.Workers[i]
		if !wasPaused && w.Status == WorkerError {
			w.Status = WorkerReady
		}
		w.Status = normalizeWorkerStatus(*w, now)
	}
	loop.CLI, loop.Person = primaryWorkerFor(loop.Workers)
	loop.WorkDir = workDir
	loop.Branch = branch
	if loop.Status != StatusPaused || (loop.MaxIterations > 0 && loop.CurrentIteration >= loop.MaxIterations) {
		loop.CurrentIteration = 0
	}
	loop.Status = StatusRunning
	loop.PauseRequested = false
	loop.AutoPaused = false
	loop.ActiveWorkerID = ""
	loop.NextResetAt = nil
	loop.LastError = ""
	loop.FinishedAt = nil
	if loop.StartedAt == nil || !wasPaused {
		loop.StartedAt = &now
	}
	loop.UpdatedAt = now
	m.active[id] = run
	_ = m.saveLocked()
	res := cloneLoop(loop, run)
	m.wg.Add(1)
	m.mu.Unlock()

	go m.runLoop(ctx, id, run)
	return &res, nil
}

// Pause requests the loop to pause cleanly once the current iteration finishes (or immediately if waiting for reset).
func (m *Manager) Pause(id, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	loop, ok := m.loops[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		return nil, ErrForbidden
	}
	run := m.active[id]
	if (loop.Status != StatusRunning && loop.Status != StatusWaitingReset) || run == nil {
		return nil, fmt.Errorf("%w: la boucle n'est pas en cours", ErrConflict)
	}
	run.pauseRequested = true
	loop.PauseRequested = true
	loop.AutoPaused = false
	loop.UpdatedAt = time.Now()
	_ = m.saveLocked()
	signalWake(run)
	res := cloneLoop(loop, run)
	return &res, nil
}

// Stop cancels a running loop immediately.
func (m *Manager) Stop(id, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	loop, ok := m.loops[id]
	if !ok {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		m.mu.Unlock()
		return nil, ErrForbidden
	}
	run := m.active[id]
	if run == nil {
		if loop.Status == StatusPaused || loop.Status == StatusWaitingReset {
			now := time.Now()
			loop.Status = StatusStopped
			loop.AutoPaused = false
			loop.ActiveWorkerID = ""
			loop.NextResetAt = nil
			loop.FinishedAt = &now
			loop.UpdatedAt = now
			_ = m.saveLocked()
		}
		res := cloneLoop(loop, nil)
		m.mu.Unlock()
		return &res, nil
	}
	cancel := run.cancel
	m.mu.Unlock()

	cancel()
	return m.Get(id, ownerID, isAdmin)
}

// WorkerAction pauses, resumes, enables, disables, or retries subscriptions on a loop live.
func (m *Manager) WorkerAction(req WorkerActionRequest, ownerID string, isAdmin bool) (*Loop, error) {
	m.mu.Lock()
	loop, ok := m.loops[req.LoopID]
	if !ok {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	if !canAccess(loop, ownerID, isAdmin) {
		m.mu.Unlock()
		return nil, ErrForbidden
	}
	now := time.Now()
	action := strings.TrimSpace(req.Action)

	findOrAppendWorker := func() (*LoopWorker, error) {
		targetID := strings.TrimSpace(req.WorkerID)
		if targetID == "" && req.CLI != "" {
			targetID = makeWorkerID(req.CLI, req.Person)
		}
		for i := range loop.Workers {
			if loop.Workers[i].ID == targetID {
				return &loop.Workers[i], nil
			}
		}
		if req.CLI == "" {
			return nil, fmt.Errorf("%w: abonnement introuvable", ErrNotFound)
		}
		if !ValidCLI(req.CLI) {
			return nil, fmt.Errorf("%w: CLI invalide", ErrInvalid)
		}
		person := strings.TrimSpace(req.Person)
		if err := m.validatePersonForCLI(req.CLI, person); err != nil {
			return nil, err
		}
		model := strings.TrimSpace(req.Model)
		if model != "" && !modelPattern.MatchString(model) {
			return nil, fmt.Errorf("%w: nom de modèle invalide", ErrInvalid)
		}
		loop.Workers = append(loop.Workers, LoopWorker{
			ID:      makeWorkerID(req.CLI, person),
			CLI:     req.CLI,
			Person:  person,
			Label:   strings.TrimSpace(req.Label),
			Model:   model,
			Enabled: false,
			Status:  WorkerDisabled,
		})
		return &loop.Workers[len(loop.Workers)-1], nil
	}

	autoResumeNeeded := false
	switch action {
	case "pause":
		w, err := findOrAppendWorker()
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		w.Paused = true
		if w.Status == WorkerRunning {
			w.PauseRequested = true
		} else {
			w.PauseRequested = false
			if w.Enabled {
				w.Status = WorkerPaused
			}
		}
	case "resume":
		w, err := findOrAppendWorker()
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		w.Enabled = true
		w.Paused = false
		w.PauseRequested = false
		w.LastError = ""
		if w.Status == WorkerError {
			w.Status = WorkerReady
		}
		if w.ResetAt != nil && !w.ResetAt.After(now) {
			w.RateLimitedAt = nil
			w.ResetAt = nil
		}
		if w.Status != WorkerRunning {
			w.Status = normalizeWorkerStatus(*w, now)
		}
		if loop.Status == StatusPaused && loop.AutoPaused && (w.Status == WorkerReady || w.Status == WorkerRateLimited) {
			autoResumeNeeded = true
		}
	case "enable":
		w, err := findOrAppendWorker()
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		w.Enabled = true
		w.Paused = false
		w.PauseRequested = false
		w.LastError = ""
		if w.Status == WorkerError {
			w.Status = WorkerReady
		}
		if w.ResetAt != nil && !w.ResetAt.After(now) {
			w.RateLimitedAt = nil
			w.ResetAt = nil
		}
		if w.Status != WorkerRunning {
			w.Status = normalizeWorkerStatus(*w, now)
		}
		if loop.Status == StatusPaused && loop.AutoPaused && (w.Status == WorkerReady || w.Status == WorkerRateLimited) {
			autoResumeNeeded = true
		}
	case "disable":
		w, err := findOrAppendWorker()
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		w.Enabled = false
		w.Paused = false
		if w.Status == WorkerRunning {
			w.PauseRequested = true
		} else {
			w.PauseRequested = false
			w.Status = WorkerDisabled
		}
	case "retry":
		w, err := findOrAppendWorker()
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		w.Enabled = true
		w.Paused = false
		w.PauseRequested = false
		w.RateLimitedAt = nil
		w.ResetAt = nil
		w.LastError = ""
		if w.Status != WorkerRunning {
			w.Status = WorkerReady
		}
		if loop.Status == StatusPaused && loop.AutoPaused {
			autoResumeNeeded = true
		}
	case "wake":
		for i := range loop.Workers {
			w := &loop.Workers[i]
			if w.Enabled && !w.Paused && w.Status == WorkerRateLimited {
				w.RateLimitedAt = nil
				w.ResetAt = nil
				w.LastError = ""
				w.Status = WorkerReady
			}
		}
		if loop.Status == StatusPaused && loop.AutoPaused {
			autoResumeNeeded = true
		}
	case "enable_all":
		subs := discoverSubscriptions(m.accountsDir, LoadAccounts(m.accountsDir), CheckCLIs())
		existingIdx := make(map[string]int, len(loop.Workers))
		for i, w := range loop.Workers {
			existingIdx[w.ID] = i
		}
		for _, sub := range subs {
			if idx, exists := existingIdx[sub.ID]; exists {
				w := &loop.Workers[idx]
				w.Enabled = true
				w.Paused = false
				w.PauseRequested = false
				w.LastError = ""
				if w.Label == "" {
					w.Label = sub.Label
				}
				if w.Status == WorkerError {
					w.Status = WorkerReady
				}
				if w.ResetAt != nil && !w.ResetAt.After(now) {
					w.RateLimitedAt = nil
					w.ResetAt = nil
				}
				if w.Status != WorkerRunning {
					w.Status = normalizeWorkerStatus(*w, now)
				}
			} else {
				sub.Enabled = true
				sub.Paused = false
				sub.Status = WorkerReady
				loop.Workers = append(loop.Workers, sub)
			}
		}
		if loop.Status == StatusPaused && loop.AutoPaused {
			autoResumeNeeded = true
		}
	case "pause_all":
		for i := range loop.Workers {
			w := &loop.Workers[i]
			if !w.Enabled {
				continue
			}
			w.Paused = true
			if w.Status == WorkerRunning {
				w.PauseRequested = true
			} else {
				w.PauseRequested = false
				w.Status = WorkerPaused
			}
		}
	case "resume_all":
		for i := range loop.Workers {
			w := &loop.Workers[i]
			if !w.Enabled {
				continue
			}
			w.Paused = false
			w.PauseRequested = false
			w.LastError = ""
			if w.Status == WorkerError {
				w.Status = WorkerReady
			}
			if w.ResetAt != nil && !w.ResetAt.After(now) {
				w.RateLimitedAt = nil
				w.ResetAt = nil
			}
			if w.Status != WorkerRunning {
				w.Status = normalizeWorkerStatus(*w, now)
			}
		}
		if loop.Status == StatusPaused && loop.AutoPaused {
			autoResumeNeeded = true
		}
	default:
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: action abonnement invalide (%s)", ErrInvalid, action)
	}

	loop.UpdatedAt = now
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	run := m.active[loop.ID]
	signalWake(run)
	res := cloneLoop(loop, run)
	m.mu.Unlock()

	if autoResumeNeeded {
		started, err := m.Start(req.LoopID, ownerID, isAdmin)
		if err == nil {
			return started, nil
		}
	}
	return &res, nil
}

func (m *Manager) runLoop(ctx context.Context, id string, run *activeRun) {
	defer m.wg.Done()
	defer func() {
		run.cancel()
		m.mu.Lock()
		if m.active[id] == run {
			delete(m.active, id)
		}
		m.mu.Unlock()
	}()

	lastWorkerID := ""

	for {
		m.mu.Lock()
		loop, ok := m.loops[id]
		if !ok {
			m.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			resetRunningWorkersLocked(loop, time.Now())
			m.finishLocked(loop, StatusStopped, "")
			m.mu.Unlock()
			return
		}
		if run.pauseRequested {
			resetRunningWorkersLocked(loop, time.Now())
			m.finishLocked(loop, StatusPaused, "")
			m.mu.Unlock()
			return
		}
		if loop.MaxIterations > 0 && loop.CurrentIteration >= loop.MaxIterations {
			resetRunningWorkersLocked(loop, time.Now())
			if loop.Outcome == "" {
				loop.Outcome = "limit"
				loop.LimitReason = "passes"
			}
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}

		now := time.Now()
		if loop.TimeLimitMinutes > 0 && loop.StartedAt != nil && !now.Before(loop.StartedAt.Add(time.Duration(loop.TimeLimitMinutes)*time.Minute)) {
			resetRunningWorkersLocked(loop, now)
			loop.Outcome = "limit"
			loop.LimitReason = "time"
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}
		if len(loop.Plan) > 0 && nextStep(loop.Plan) == nil {
			loop.Outcome = "review"
			for _, step := range loop.Plan {
				if state := stepState(step); state == "todo" || state == "blocked" || state == "working" {
					loop.Outcome = "blocked"
				}
			}
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}
		refreshExpiredRateLimitsLocked(loop, now)
		workerIdx, accountBusy := m.pickGlobalWorkerLocked(loop, lastWorkerID, now)
		if workerIdx < 0 {
			if accountBusy {
				loop.Phase = "waiting_account"
				loop.ActiveWorkerID = ""
				m.mu.Unlock()
				select {
				case <-ctx.Done():
				case <-run.wakeCh:
				case <-time.After(time.Second):
				}
				continue
			}
			earliestReset, hasRateLimited, hasPaused, lastErr := summarizeBlockedWorkersLocked(loop)
			if hasRateLimited {
				loop.Status = StatusWaitingReset
				loop.ActiveWorkerID = ""
				loop.NextResetAt = &earliestReset
				loop.UpdatedAt = now
				_ = m.saveLocked()

				waitDur := earliestReset.Sub(now) + 2*time.Second
				if loop.TimeLimitMinutes > 0 && loop.StartedAt != nil {
					remaining := time.Until(loop.StartedAt.Add(time.Duration(loop.TimeLimitMinutes) * time.Minute))
					if remaining < waitDur {
						waitDur = remaining
					}
				}
				if waitDur < time.Second {
					waitDur = time.Second
				}
				m.mu.Unlock()
				timer := time.NewTimer(waitDur)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-run.wakeCh:
					timer.Stop()
				case <-timer.C:
				}
				continue
			}
			if hasPaused {
				loop.AutoPaused = true
				m.finishLocked(loop, StatusPaused, "")
				m.mu.Unlock()
				return
			}
			if lastErr != "" {
				m.finishLocked(loop, StatusError, lastErr)
				m.mu.Unlock()
				return
			}
			m.finishLocked(loop, StatusPaused, "Aucun abonnement activé sur cette boucle.")
			m.mu.Unlock()
			return
		}

		w := &loop.Workers[workerIdx]
		wID := w.ID
		startedAt := now
		passNum := loop.CurrentIteration + 1
		logNum := len(loop.Iterations) + 1

		loop.Status = StatusRunning
		loop.AutoPaused = false
		loop.NextResetAt = nil
		loop.Phase = "working"
		loop.ActiveWorkerID = wID
		loop.CLI = w.CLI
		loop.Person = w.Person
		w.Status = WorkerRunning
		w.PauseRequested = false
		w.LastError = ""
		w.LastRunAt = &startedAt

		iterRec := IterationRecord{
			Number:      logNum,
			Status:      StatusRunning,
			WorkerID:    wID,
			CLI:         w.CLI,
			Person:      w.Person,
			WorkerLabel: workerDisplayLabel(*w),
			StartedAt:   startedAt,
		}
		loop.Iterations = append(loop.Iterations, iterRec)
		iterIdx := len(loop.Iterations) - 1
		loop.UpdatedAt = startedAt
		run.liveTail = ""

		loop.Outcome = ""
		if loop.PendingInstruction != "" {
			loop.Memory = boundedMemory(loop.Memory + "\nConsigne utilisateur : " + loop.PendingInstruction)
			loop.Iterations[iterIdx].Instruction = loop.PendingInstruction
			loop.PendingInstruction = ""
		}
		snap := cloneLoop(loop, run)
		snap.CLI = w.CLI
		snap.Person = w.Person
		if w.Model != "" {
			snap.Model = w.Model
		}
		_ = m.saveLocked()
		m.mu.Unlock()

		passCtx := ctx
		cancelPass := func() {}
		if snap.TimeLimitMinutes > 0 && snap.StartedAt != nil {
			passCtx, cancelPass = context.WithDeadline(ctx, snap.StartedAt.Add(time.Duration(snap.TimeLimitMinutes)*time.Minute))
		}
		outcome := m.executeIteration(passCtx, snap, passNum, logNum, run)
		timedOut := passCtx.Err() == context.DeadlineExceeded
		cancelPass()

		m.mu.Lock()
		loop, ok = m.loops[id]
		if !ok {
			m.mu.Unlock()
			return
		}
		now = time.Now()
		rec := &loop.Iterations[iterIdx]
		rec.FinishedAt = &now
		rec.DurationMs = now.Sub(startedAt).Milliseconds()
		rec.ExitCode = outcome.exitCode
		rec.ChangedFiles = outcome.changedFiles
		rec.DiffStat = outcome.diffStat
		rec.LogTail = outcome.tail
		rec.DoneSignal = outcome.doneSignal
		rec.RateLimited = outcome.rateLimited
		if outcome.rateLimited {
			resetCopy := outcome.resetAt
			rec.ResetAt = &resetCopy
		}
		rec.Error = outcome.errMsg
		rec.Verification = outcome.verification
		rec.BeforeTree = outcome.beforeTree
		rec.AfterTree = outcome.afterTree
		rec.Result = outcome.result
		rec.GitPatch = outcome.patch
		rec.Snapshot = outcome.snapshot
		if outcome.result != nil {
			data, _ := json.Marshal(outcome.result)
			loop.Memory = boundedMemory(loop.Memory + "\n" + string(data))
		}

		w = findWorkerByID(loop, wID)
		if timedOut {
			rec.Status = "stopped"
			resetRunningWorkersLocked(loop, time.Now())
			loop.Outcome = "limit"
			loop.LimitReason = "time"
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}
		if ctx.Err() != nil {
			rec.Status = StatusStopped
			if w != nil {
				w.PauseRequested = false
				w.Status = normalizeWorkerStatus(*w, now)
			}
			m.finishLocked(loop, StatusStopped, "")
			m.mu.Unlock()
			return
		}
		if reason := stagnationReason(loop.Iterations); reason != "" {
			rec.Status = "blocked"
			loop.Outcome = "blocked"
			resetRunningWorkersLocked(loop, now)
			m.finishLocked(loop, StatusPaused, reason)
			m.mu.Unlock()
			return
		}
		if outcome.rateLimited {
			rec.Status = WorkerRateLimited
			if w != nil {
				nowCopy := now
				resetCopy := outcome.resetAt
				w.RateLimitedAt = &nowCopy
				w.ResetAt = &resetCopy
				w.LastError = outcome.errMsg
				w.PauseRequested = false
				w.Status = normalizeWorkerStatus(*w, now)
			}
			loop.ActiveWorkerID = ""
			loop.UpdatedAt = now
			_ = m.saveLocked()
			m.mu.Unlock()
			continue
		}
		if outcome.errMsg != "" {
			rec.Status = "failed"
			loop.LastSummary = outcome.summary
			if w != nil {
				w.LastError = outcome.errMsg
				w.PauseRequested = false
				if !w.Enabled {
					w.Status = WorkerDisabled
				} else if w.Paused {
					w.Status = WorkerPaused
				} else {
					w.Status = WorkerError
				}
			}
			loop.ActiveWorkerID = ""
			if hasWorkableWorkersLocked(loop, now) {
				loop.LastError = outcome.errMsg
				loop.UpdatedAt = now
				_ = m.saveLocked()
				m.mu.Unlock()
				continue
			}
			m.finishLocked(loop, StatusError, outcome.errMsg)
			m.mu.Unlock()
			return
		}

		rec.Decision = decidePass(rec)
		applyStepResult(loop, rec)
		rec.Status = StatusCompleted
		loop.CurrentIteration++
		loop.LastSummary = outcome.summary
		loop.LastError = ""
		loop.UpdatedAt = now
		lastWorkerID = wID
		if w != nil {
			w.RunCount++
			w.LastError = ""
			if w.PauseRequested {
				w.Paused = true
				w.PauseRequested = false
			}
			w.Status = normalizeWorkerStatus(*w, now)
		}

		if snap.StopWhenDone && outcome.doneSignal {
			loop.Outcome = "review"
			if outcome.verification != nil && !outcome.verification.Passed {
				loop.Outcome = "blocked"
			}
			if snap.Mode == "maintenance" && (outcome.verification == nil || outcome.verification.Passed) {
				loop.Outcome = "waiting_work"
			}
			if loop.Outcome == "" {
				loop.Outcome = "limit"
				loop.LimitReason = "passes"
			}
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}
		if run.pauseRequested {
			m.finishLocked(loop, StatusPaused, "")
			m.mu.Unlock()
			return
		}
		if loop.MaxIterations > 0 && loop.CurrentIteration >= loop.MaxIterations {
			if loop.Outcome == "" {
				loop.Outcome = "limit"
				loop.LimitReason = "passes"
			}
			m.finishLocked(loop, StatusCompleted, "")
			m.mu.Unlock()
			return
		}
		delay := time.Duration(loop.DelaySeconds) * time.Second
		_ = m.saveLocked()
		m.mu.Unlock()

		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-run.wakeCh:
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

func findWorkerByID(loop *Loop, id string) *LoopWorker {
	for i := range loop.Workers {
		if loop.Workers[i].ID == id {
			return &loop.Workers[i]
		}
	}
	return nil
}

func resetRunningWorkersLocked(loop *Loop, now time.Time) {
	for i := range loop.Workers {
		w := &loop.Workers[i]
		if w.Status == WorkerRunning {
			w.PauseRequested = false
			w.Status = normalizeWorkerStatus(*w, now)
		}
	}
}

func refreshExpiredRateLimitsLocked(loop *Loop, now time.Time) {
	for i := range loop.Workers {
		w := &loop.Workers[i]
		if w.Status == WorkerRateLimited && w.ResetAt != nil && !w.ResetAt.After(now) {
			w.RateLimitedAt = nil
			w.ResetAt = nil
			w.LastError = ""
			w.Status = normalizeWorkerStatus(*w, now)
		}
	}
}

func pickNextWorkerLocked(loop *Loop, lastWorkerID string) int {
	if lastWorkerID != "" {
		for i, w := range loop.Workers {
			if w.ID == lastWorkerID && w.Enabled && !w.Paused && w.Status == WorkerReady {
				return i
			}
		}
	}
	startIdx := 0
	if lastWorkerID != "" {
		for i, w := range loop.Workers {
			if w.ID == lastWorkerID {
				startIdx = (i + 1) % len(loop.Workers)
				break
			}
		}
	}
	for offset := 0; offset < len(loop.Workers); offset++ {
		idx := (startIdx + offset) % len(loop.Workers)
		w := loop.Workers[idx]
		if w.Enabled && !w.Paused && w.Status == WorkerReady {
			return idx
		}
	}
	return -1
}

func summarizeBlockedWorkersLocked(loop *Loop) (earliestReset time.Time, hasRateLimited, hasPaused bool, lastErr string) {
	for _, w := range loop.Workers {
		if !w.Enabled {
			continue
		}
		if w.Paused || w.Status == WorkerPaused {
			hasPaused = true
			continue
		}
		if w.Status == WorkerRateLimited && w.ResetAt != nil {
			if !hasRateLimited || w.ResetAt.Before(earliestReset) {
				earliestReset = *w.ResetAt
			}
			hasRateLimited = true
			continue
		}
		if w.Status == WorkerError && w.LastError != "" {
			lastErr = w.LastError
		}
	}
	return earliestReset, hasRateLimited, hasPaused, lastErr
}

func hasWorkableWorkersLocked(loop *Loop, now time.Time) bool {
	for _, w := range loop.Workers {
		if !w.Enabled || w.Paused {
			continue
		}
		if w.Status == WorkerReady {
			return true
		}
		if w.Status == WorkerRateLimited && w.ResetAt != nil {
			return true
		}
		if w.ResetAt != nil && !w.ResetAt.After(now) {
			return true
		}
	}
	return false
}

func (m *Manager) finishLocked(loop *Loop, status, errMsg string) {
	now := time.Now()
	loop.Phase = ""
	loop.Status = status
	loop.PauseRequested = false
	loop.ActiveWorkerID = ""
	loop.NextResetAt = nil
	loop.LastError = errMsg
	loop.UpdatedAt = now
	if status != StatusPaused {
		loop.FinishedAt = &now
	}
	_ = m.saveLocked()
}

type iterationOutcome struct {
	beforeTree   string
	afterTree    string
	verification *Verification
	result       *PassResult
	patch        string
	snapshot     bool
	exitCode     int
	changedFiles int
	diffStat     string
	tail         string
	summary      string
	doneSignal   bool
	rateLimited  bool
	resetAt      time.Time
	errMsg       string
}

func (m *Manager) executeIteration(ctx context.Context, loop Loop, passNum, logNum int, run *activeRun) iterationOutcome {
	workDir := loop.WorkDir
	if workDir == "" {
		workDir = loop.ProjectDir
	}
	before, snapshot := snapshotTree(workDir)
	iterPrompt := buildIterationPrompt(loop, passNum)
	binPath, err := exec.LookPath(loop.CLI)
	if err != nil {
		return iterationOutcome{exitCode: 127, errMsg: fmt.Sprintf("CLI introuvable (%s): %v", loop.CLI, err)}
	}
	args := buildIterationArgs(loop, workDir, iterPrompt)
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	cmd.Dir = workDir
	cmd.Env = iterationEnv(loop.CLI, loop.Person, loop.AutoApprove)

	logDir := filepath.Join(m.logsDir, loop.ID)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return iterationOutcome{exitCode: 1, errMsg: err.Error()}
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, fmt.Sprintf("iter-%d.log", logNum)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return iterationOutcome{exitCode: 1, errMsg: err.Error()}
	}
	defer logFile.Close()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return iterationOutcome{exitCode: 1, errMsg: err.Error()}
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return iterationOutcome{exitCode: 1, errMsg: err.Error()}
	}

	buf := make([]byte, 4096)
	var tailBuf []byte
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = logFile.Write(chunk)
			tailBuf = appendTail(tailBuf, chunk, maxLogTailBytes)
			m.mu.Lock()
			run.liveTail = string(tailBuf)
			m.mu.Unlock()
		}
		if readErr != nil {
			break
		}
	}

	waitErr := cmd.Wait()
	tail := string(tailBuf)
	exitCode := 0
	errMsg := ""
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
		if ctx.Err() == nil {
			errMsg = fmt.Sprintf("%s s'est terminé avec le code %d", loop.CLI, exitCode)
		}
	}

	changedFiles, diffStat := 0, ""
	if gitIsRepo(workDir) {
		changedFiles, diffStat = gitChangesSummary(workDir)
	}
	doneSignal := hasLoopDoneMarker(tail)
	rateLimited, resetAt, rlReason := detectRateLimit(tail, exitCode, changedFiles, doneSignal, time.Now())
	if rateLimited {
		errMsg = rlReason
	}
	var verification *Verification
	if loop.VerifyCommand != "" {
		m.mu.Lock()
		if current := m.loops[loop.ID]; current != nil {
			current.Phase = "verifying"
		}
		m.mu.Unlock()
	}
	if loop.VerifyCommand != "" && errMsg == "" && ctx.Err() == nil {
		verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		check := exec.CommandContext(verifyCtx, "sh", "-c", loop.VerifyCommand)
		check.Dir = workDir
		check.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		check.Cancel = func() error { return syscall.Kill(-check.Process.Pid, syscall.SIGKILL) }
		check.WaitDelay = 3 * time.Second
		outputBuffer := &tailWriter{}
		check.Stdout = outputBuffer
		check.Stderr = outputBuffer
		verificationStarted := time.Now()
		checkErr := check.Run()
		output := outputBuffer.data
		cancel()
		if len(output) > 32000 {
			output = output[len(output)-32000:]
		}
		verification = &Verification{DurationMs: time.Since(verificationStarted).Milliseconds(), Command: loop.VerifyCommand, Output: string(output), Passed: checkErr == nil}
	}
	summary := summarizeOutput(tail, diffStat, changedFiles)
	result := parsePassResult(tail)
	if result != nil {
		summary = result.Change
	}
	patch := ""
	afterTree := ""
	if snapshot {
		after, ok := snapshotTree(workDir)
		afterTree = after
		snapshot = ok
		if ok {
			out, err := exec.Command("git", "-C", workDir, "diff", before, after, "--").Output()
			snapshot = err == nil
			patch = string(out)
			if len(patch) > 128*1024 {
				patch = patch[:128*1024] + "\n[Diff tronqué]"
			}
		}
	}

	return iterationOutcome{
		beforeTree: before, afterTree: afterTree, verification: verification, result: result, patch: patch, snapshot: snapshot,
		exitCode:     exitCode,
		changedFiles: changedFiles,
		diffStat:     diffStat,
		tail:         tail,
		summary:      summary,
		doneSignal:   doneSignal,
		rateLimited:  rateLimited,
		resetAt:      resetAt,
		errMsg:       errMsg,
	}
}

func buildIterationArgs(loop Loop, workDir, iterPrompt string) []string {
	switch loop.CLI {
	case CLIClaude:
		mode := "acceptEdits"
		if loop.AutoApprove {
			mode = "bypassPermissions"
		}
		args := []string{"-p", iterPrompt, "--permission-mode", mode}
		if loop.Model != "" {
			args = append(args, "--model", loop.Model)
		}
		return args
	case CLICodex:
		args := []string{"exec", "-C", workDir, "--skip-git-repo-check"}
		if loop.AutoApprove {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			args = append(args, "--sandbox", "workspace-write", "--approve-for-me")
		}
		if loop.Model != "" {
			args = append(args, "--model", loop.Model)
		}
		args = append(args, iterPrompt)
		return args
	case CLIAgy:
		args := []string{"-p", iterPrompt}
		if loop.AutoApprove {
			args = append(args, "--dangerously-skip-permissions")
		} else {
			args = append(args, "--mode", "accept-edits")
		}
		if loop.Model != "" {
			args = append(args, "--model", loop.Model)
		}
		return args
	default:
		return []string{"-p", iterPrompt}
	}
}

func iterationEnv(cli, person string, autoApprove bool) []string {
	raw := os.Environ()
	env := make([]string, 0, len(raw)+2)
	for _, entry := range raw {
		// Isolate headless runs from any tmux session that started TmuxAtlas.
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") || strings.HasPrefix(entry, "AI_PERSON=") {
			continue
		}
		env = append(env, entry)
	}
	if person != "" {
		env = append(env, "AI_PERSON="+person)
	}
	if cli == CLIClaude && autoApprove && os.Geteuid() == 0 {
		env = append(env, "IS_SANDBOX=1")
	}
	return env
}

func buildIterationPrompt(loop Loop, passNum int) string {
	var sb strings.Builder
	if loop.MaxIterations > 0 {
		fmt.Fprintf(&sb, "[Boucle d'amélioration Atlas — itération %d/%d]\n", passNum, loop.MaxIterations)
	} else {
		fmt.Fprintf(&sb, "[Boucle d'amélioration Atlas — itération %d (mode continu)]\n", passNum)
	}
	if loop.LastSummary != "" && passNum > 1 {
		fmt.Fprintf(&sb, "Résumé de l'itération précédente : %s\n\n", loop.LastSummary)
	}
	plan, _ := json.Marshal(loop.Plan)
	if selected := nextStep(loop.Plan); selected != nil {
		fmt.Fprintf(&sb, "Étape à traiter exclusivement : %s — %s\nRésultat attendu : %s\n", selected.ID, selected.Title, selected.Expected)
	} else if len(loop.Plan) > 0 {
		sb.WriteString("Aucune étape exécutable : examine les preuves ou explique le blocage ; ne commence pas un autre chantier.\n")
	}
	directives, _ := json.Marshal(loop.Directives)
	fmt.Fprintf(&sb, "Consignes utilisateur durables : %s\n", directives)
	if len(loop.Iterations) > 0 {
		prev := IterationRecord{}
		for i := len(loop.Iterations) - 1; i >= 0; i-- {
			if loop.Iterations[i].Verification != nil {
				prev = loop.Iterations[i]
				break
			}
		}
		if prev.Verification != nil {
			observed, _ := json.Marshal(prev.Verification)
			fmt.Fprintf(&sb, "FAITS OBSERVÉS PAR ATLAS (prioritaires sur les déclarations) : %s\n", observed)
			if !prev.Verification.Passed {
				sb.WriteString("La prochaine action est de corriger cet échec, avec une approche ciblée.\n")
			}
		}
	}
	fmt.Fprintf(&sb, "Plan (completed_steps contient les identifiants des étapes terminées) : %s\n", plan)
	fmt.Fprintf(&sb, "Périmètre : %s\nCritères de réussite : %s\nMémoire persistante :\n%s\n", loop.Scope, loop.Criteria, loop.Memory)
	sb.WriteString("Termine par ATLAS_RESULT suivi sur la même ligne d’un objet JSON avec les champs string problem, change, checks, limitations, next, decision. Les vérifications rapportées restent des déclarations.\n")
	sb.WriteString("Objectif :\n")
	sb.WriteString(loop.Prompt)
	sb.WriteString("\n\nConsignes :\n")
	sb.WriteString("- Concentre-toi sur une amélioration concrète et vérifiable par itération.\n")
	sb.WriteString("- Lance les tests/vérifications pertinents après tes modifications et termine par un court résumé.\n")
	if loop.StopWhenDone {
		sb.WriteString("- Si aucune amélioration pertinente ne reste à faire, ajoute `LOOP_DONE` sur une ligne seule après ton bilan ATLAS_RESULT.\n")
	}
	return sb.String()
}

func hasLoopDoneMarker(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.Trim(line, "`*_"))
		if trimmed == "LOOP_DONE" {
			return true
		}
	}
	return false
}

func summarizeOutput(tail, diffStat string, changedFiles int) string {
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	lastNonEmpty := ""
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			lastNonEmpty = line
			break
		}
	}
	if len(lastNonEmpty) > 160 {
		lastNonEmpty = lastNonEmpty[:160] + "…"
	}
	if changedFiles > 0 && diffStat != "" {
		if lastNonEmpty != "" {
			return fmt.Sprintf("%d fichier(s) modifié(s) (%s) · %s", changedFiles, diffStat, lastNonEmpty)
		}
		return fmt.Sprintf("%d fichier(s) modifié(s) (%s)", changedFiles, diffStat)
	}
	return lastNonEmpty
}

func appendTail(current, chunk []byte, maxBytes int) []byte {
	combined := append(current, chunk...)
	if len(combined) > maxBytes {
		combined = append([]byte(nil), combined[len(combined)-maxBytes:]...)
	}
	return combined
}

func (m *Manager) ensureWorktree(id, name, projectDir, existingWorkDir, existingBranch string) (string, string, error) {
	if existingWorkDir != "" && existingWorkDir != projectDir {
		if info, err := os.Stat(existingWorkDir); err == nil && info.IsDir() {
			return existingWorkDir, existingBranch, nil
		}
	}
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		slug = "improve"
	}
	if len(slug) > 24 {
		slug = slug[:24]
	}
	branch := fmt.Sprintf("loop/%s-%s", slug, id[:6])
	worktreePath := filepath.Join(m.worktreesDir, id)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return "", "", err
	}
	cmd := exec.Command("git", "-C", projectDir, "worktree", "add", "-b", branch, worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Retry attaching existing branch if it already exists.
		retry := exec.Command("git", "-C", projectDir, "worktree", "add", worktreePath, branch)
		if retryOut, retryErr := retry.CombinedOutput(); retryErr != nil {
			return "", "", fmt.Errorf("impossible de créer le worktree git: %s (%s)", strings.TrimSpace(string(out)), strings.TrimSpace(string(retryOut)))
		}
	}
	return worktreePath, branch, nil
}

func gitIsRepo(dir string) bool {
	if dir == "" {
		return false
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitCurrentBranch(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitChangesSummary(dir string) (int, string) {
	statusOut, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return 0, ""
	}
	count := 0
	for _, line := range strings.Split(strings.TrimRight(string(statusOut), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	statOut, _ := exec.Command("git", "-C", dir, "diff", "--shortstat").Output()
	return count, strings.TrimSpace(string(statOut))
}

func gitStatusAndDiff(dir string) (string, string) {
	statusOut, _ := exec.Command("git", "-C", dir, "status", "--short").Output()
	diffOut, _ := exec.Command("git", "-C", dir, "diff", "--stat").Output()
	diff := string(diffOut)
	if strings.TrimSpace(diff) == "" {
		logOut, _ := exec.Command("git", "-C", dir, "log", "-n", "5", "--oneline").Output()
		if s := strings.TrimSpace(string(logOut)); s != "" {
			diff = "Derniers commits :\n" + s
		}
	}
	const maxDiffBytes = 32 * 1024
	if len(diff) > maxDiffBytes {
		diff = diff[:maxDiffBytes] + "\n…"
	}
	return strings.TrimSpace(string(statusOut)), strings.TrimSpace(diff)
}

// ParseIterationQuery parses an optional ?iter= query value.
func ParseIterationQuery(raw string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(raw))
	return n
}

// Unused import guard for io/bytes if needed by callers.
var (
	_ = io.EOF
	_ = bytes.MinRead
)
