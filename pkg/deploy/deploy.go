// Package deploy updates a Compose application from its git repository and
// rolls it back: fast-forward the working directory, remember the images in
// use, `docker compose build` then `up -d`, and check the containers. Every
// operation runs in the background, one at a time per stack, and leaves a
// record. Every external command goes through a Runner. Admin-only, and off
// unless TMUXATLAS_DEPLOY=on. See docs/deploiement-applications.md.
package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
)

// Maximum duration of each step. Waiting for another build counts in the
// build step.
var stepTimeouts = map[string]time.Duration{
	stepCheck:  time.Minute,
	stepFetch:  3 * time.Minute,
	stepMerge:  2 * time.Minute,
	stepImages: 2 * time.Minute,
	stepRetag:  2 * time.Minute,
	stepBuild:  40 * time.Minute,
	stepUp:     10 * time.Minute,
}

// Defaults of the final verification.
const (
	defaultVerifyDelay    = 5 * time.Second
	defaultVerifyInterval = 4 * time.Second
	defaultVerifyTimeout  = 3 * time.Minute
)

// StackSource describes a Compose project from the labels of its containers.
// *dockerui.Service is the real one: it carries the switches of the Docker
// actions.
type StackSource interface {
	DeployTarget(ctx context.Context, project string) (dockerui.DeployTarget, error)
}

// Options configures a Service.
type Options struct {
	// Dir is where the records are kept.
	Dir    string
	Runner Runner
	Stacks StackSource
	// Enabled is true with TMUXATLAS_DEPLOY=on. Without it the state is
	// still readable and every operation is refused with DisabledReason.
	Enabled        bool
	DisabledReason string
	// Problem refuses every operation whatever the switch (git missing…).
	Problem string
	Now     func() time.Time
	// The verification waits VerifyDelay, then reads the containers every
	// VerifyInterval for at most VerifyTimeout.
	VerifyDelay    time.Duration
	VerifyInterval time.Duration
	VerifyTimeout  time.Duration
}

// Error is a refusal with its HTTP status.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func refuse(status int, format string, args ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

// State is the payload of GET /api/docker/stacks/{project}/deploy.
type State struct {
	Stack string `json:"stack"`
	// Enabled is false while TMUXATLAS_DEPLOY is not on: read-only.
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	// Blocked says why this stack cannot be deployed nor rolled back from
	// here, whatever the switch.
	Blocked     string   `json:"blocked,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	ConfigFiles []string `json:"config_files"`
	// Services names the Compose services of the stack.
	Services []string `json:"services"`
	Git      GitState `json:"git"`
	// Current is the operation in progress, with the recent output of its steps.
	Current *Record `json:"current,omitempty"`
	// History lists the latest records, most recent first, without output.
	History []Record `json:"history"`
	// RollbackID is the deployment that can be rolled back, if any.
	RollbackID string `json:"rollback_id,omitempty"`
}

// Service runs and records the deployments.
type Service struct {
	opts   Options
	store  store
	logger *logrus.Entry

	mu      sync.Mutex
	running map[string]*operation
	// build holds the one build allowed at a time on the server.
	build chan struct{}
}

// operation is one deployment or rollback in progress.
type operation struct {
	plan   plan
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	record Record
	tails  map[string]*tail
}

// New returns the service and closes the records a previous hub left running.
func New(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.VerifyDelay == 0 {
		opts.VerifyDelay = defaultVerifyDelay
	}
	if opts.VerifyInterval == 0 {
		opts.VerifyInterval = defaultVerifyInterval
	}
	if opts.VerifyTimeout == 0 {
		opts.VerifyTimeout = defaultVerifyTimeout
	}
	if !opts.Enabled && opts.DisabledReason == "" {
		opts.DisabledReason = "déploiement désactivé sur ce serveur (TMUXATLAS_DEPLOY=on pour l’activer)"
	}
	s := &Service{
		opts:    opts,
		store:   store{dir: opts.Dir},
		logger:  logrus.WithField("component", "deploy"),
		running: make(map[string]*operation),
		build:   make(chan struct{}, 1),
	}
	if opts.Dir != "" {
		s.store.closeInterrupted(opts.Now())
	}
	return s
}

func (s *Service) stamp() string { return s.opts.Now().UTC().Format(time.RFC3339) }

// target reads the stack and turns a Docker refusal into an Error.
func (s *Service) target(ctx context.Context, stack string) (dockerui.DeployTarget, error) {
	if !ValidProject(stack) {
		return dockerui.DeployTarget{}, refuse(http.StatusBadRequest, "nom d’application invalide")
	}
	if s.opts.Stacks == nil {
		return dockerui.DeployTarget{}, refuse(http.StatusServiceUnavailable, "vue Docker indisponible")
	}
	target, err := s.opts.Stacks.DeployTarget(ctx, stack)
	if err != nil {
		status, message := dockerui.ErrorStatus(err)
		return target, &Error{Status: status, Message: message}
	}
	return target, nil
}

// blocked says why a stack cannot be operated from here, and returns its
// validated plan otherwise.
func (s *Service) blocked(target dockerui.DeployTarget) (plan, string) {
	if s.opts.Problem != "" {
		return plan{}, s.opts.Problem
	}
	if s.opts.Runner == nil || s.opts.Dir == "" {
		return plan{}, "déploiement indisponible sur ce serveur"
	}
	if target.Hub != "" {
		return plan{}, "cette application contient Atlas lui-même (conteneur « " + target.Hub +
			" ») : la déployer d’ici couperait l’opération en cours de route"
	}
	validated, err := validatePlan(target.Project, target.WorkingDir, target.ConfigFiles, target.EnvFiles)
	if err != nil {
		return plan{}, err.Error()
	}
	return validated, ""
}

func serviceNames(target dockerui.DeployTarget) []string {
	names := []string{}
	seen := make(map[string]bool)
	for _, service := range target.Services {
		if service.Service != "" && !seen[service.Service] {
			seen[service.Service] = true
			names = append(names, service.Service)
		}
	}
	return names
}

// State reads the deployment state of a stack. It runs two read-only git
// commands and changes nothing.
func (s *Service) State(ctx context.Context, stack string) (State, error) {
	target, err := s.target(ctx, stack)
	if err != nil {
		return State{}, err
	}
	state := State{
		Stack: stack, Enabled: s.opts.Enabled, WorkingDir: target.WorkingDir,
		ConfigFiles: append([]string{}, target.ConfigFiles...), Services: serviceNames(target),
		Git: GitState{Dirty: []string{}}, History: []Record{},
	}
	if !s.opts.Enabled {
		state.DisabledReason = s.opts.DisabledReason
	}
	_, state.Blocked = s.blocked(target)
	switch {
	case s.opts.Runner == nil || s.opts.Problem != "":
		state.Git.Error = "état git non lu"
	case checkDir(target.WorkingDir) != nil:
		state.Git.Error = "dossier de travail illisible depuis le hub"
	default:
		state.Git = readGit(ctx, s.opts.Runner, target.WorkingDir)
	}

	s.mu.Lock()
	op := s.running[stack]
	s.mu.Unlock()
	var current *Record
	if op != nil {
		snapshot := op.snapshot(recentOutput)
		current = &snapshot
		state.Current = current
	}
	if s.opts.Dir != "" {
		history := s.store.list(stack, listedRecords)
		for i := range history {
			if current != nil && history[i].ID == current.ID {
				history[i] = *current
			}
			history[i] = history[i].withoutOutput()
		}
		state.History = history
		if current == nil && state.Blocked == "" {
			state.RollbackID = rollbackCandidate(history)
		}
	}
	return state, nil
}

// Record returns one record of a stack with the output of its steps.
func (s *Service) Record(stack, id string) (Record, error) {
	if !ValidProject(stack) || !recordIDPattern.MatchString(id) {
		return Record{}, refuse(http.StatusBadRequest, "identifiant invalide")
	}
	s.mu.Lock()
	op := s.running[stack]
	s.mu.Unlock()
	if op != nil {
		if snapshot := op.snapshot(maxStepOutput); snapshot.ID == id {
			return snapshot, nil
		}
	}
	record, err := s.store.load(stack, id)
	if err != nil || record.Stack != stack {
		return Record{}, refuse(http.StatusNotFound, "%s", errNoRecord.Error())
	}
	return record, nil
}

// rollbackCandidate returns the deployment a rollback would undo: the latest
// one, if its images were kept, if it went as far as restarting the services
// and if it has not been rolled back since. history is most recent first.
func rollbackCandidate(history []Record) string {
	undone := make(map[string]bool)
	for _, record := range history {
		if record.Result == ResultRunning {
			return ""
		}
		if record.Kind == KindRollback {
			if record.Result == ResultSuccess {
				undone[record.RollbackOf] = true
			}
			continue
		}
		if record.Kind != KindDeploy {
			continue
		}
		up := record.step(stepUp)
		if undone[record.ID] || up == nil || (up.Status != StepDone && up.Status != StepFailed) {
			return ""
		}
		for _, image := range record.Images {
			if image.RollbackTag != "" && retaggable(image.Image) {
				return record.ID
			}
		}
		return ""
	}
	return ""
}

// begin checks the guards and reserves the stack for a new operation.
func (s *Service) begin(ctx context.Context, kind, stack, actor string, steps []string) (*operation, error) {
	if !s.opts.Enabled {
		return nil, refuse(http.StatusServiceUnavailable, "%s", s.opts.DisabledReason)
	}
	target, err := s.target(ctx, stack)
	if err != nil {
		return nil, err
	}
	validated, problem := s.blocked(target)
	if problem != "" {
		return nil, refuse(http.StatusConflict, "%s", problem)
	}
	record := Record{
		ID: newRecordID(s.opts.Now()), Kind: kind, Stack: stack, Actor: actor,
		StartedAt: s.stamp(), Result: ResultRunning,
		WorkingDir: validated.workingDir, ConfigFiles: append([]string{}, validated.configFiles...),
		Images: []Image{},
	}
	tails := make(map[string]*tail, len(steps))
	for _, name := range steps {
		record.Steps = append(record.Steps, Step{Name: name, Status: StepPending})
		tails[name] = &tail{}
	}
	opCtx, cancel := context.WithCancel(context.Background())
	op := &operation{plan: validated, ctx: opCtx, cancel: cancel, done: make(chan struct{}), record: record, tails: tails}

	s.mu.Lock()
	if s.running[stack] != nil {
		s.mu.Unlock()
		cancel()
		return nil, refuse(http.StatusConflict, "une opération est déjà en cours sur cette application")
	}
	s.running[stack] = op
	s.mu.Unlock()
	if err := s.store.save(record); err != nil {
		s.mu.Lock()
		delete(s.running, stack)
		s.mu.Unlock()
		cancel()
		s.logger.WithError(err).Warn("deployment record not written")
		return nil, refuse(http.StatusInternalServerError, "journal de déploiement impossible à écrire")
	}
	return op, nil
}

// Deploy starts a deployment in the background and returns its record.
func (s *Service) Deploy(ctx context.Context, stack, actor string) (Record, error) {
	op, err := s.begin(ctx, KindDeploy, stack, actor,
		[]string{stepCheck, stepFetch, stepMerge, stepImages, stepBuild, stepUp, stepVerify})
	if err != nil {
		return Record{}, err
	}
	go s.runDeploy(op)
	return op.snapshot(recentOutput), nil
}

// Rollback restarts the services of a stack with the images they used before
// the deployment id, which must be the one State names in RollbackID. The
// code on disk is not touched.
func (s *Service) Rollback(ctx context.Context, stack, id, actor string) (Record, error) {
	if !s.opts.Enabled {
		return Record{}, refuse(http.StatusServiceUnavailable, "%s", s.opts.DisabledReason)
	}
	if !ValidProject(stack) || !recordIDPattern.MatchString(id) {
		return Record{}, refuse(http.StatusBadRequest, "identifiant invalide")
	}
	if rollbackCandidate(s.store.list(stack, listedRecords)) != id {
		return Record{}, refuse(http.StatusConflict, "ce déploiement n’est pas, ou plus, celui sur lequel on peut revenir : recharge l’état")
	}
	source, err := s.store.load(stack, id)
	if err != nil || source.Stack != stack {
		return Record{}, refuse(http.StatusNotFound, "%s", errNoRecord.Error())
	}
	op, err := s.begin(ctx, KindRollback, stack, actor, []string{stepImages, stepRetag, stepUp, stepVerify})
	if err != nil {
		return Record{}, err
	}
	op.mu.Lock()
	op.record.RollbackOf = id
	op.mu.Unlock()
	go s.runRollback(op, source)
	return op.snapshot(recentOutput), nil
}

// Cancel asks the operation in progress on a stack to stop. The command in
// progress receives SIGTERM; what it already did is not undone.
func (s *Service) Cancel(stack string) (Record, error) {
	if !ValidProject(stack) {
		return Record{}, refuse(http.StatusBadRequest, "nom d’application invalide")
	}
	s.mu.Lock()
	op := s.running[stack]
	s.mu.Unlock()
	if op == nil {
		return Record{}, refuse(http.StatusConflict, "aucune opération en cours sur cette application")
	}
	op.mu.Lock()
	op.record.CancelRequested = true
	op.mu.Unlock()
	op.cancel()
	return op.snapshot(recentOutput), nil
}

// Wait blocks until no operation runs on the stack. Tests use it.
func (s *Service) Wait(stack string) {
	s.mu.Lock()
	op := s.running[stack]
	s.mu.Unlock()
	if op != nil {
		<-op.done
	}
}

// snapshot copies the record, with at most limit bytes of output per step;
// the running step shows what its commands printed so far.
func (op *operation) snapshot(limit int) Record {
	op.mu.Lock()
	defer op.mu.Unlock()
	record := op.record.clone()
	for i := range record.Steps {
		step := &record.Steps[i]
		switch step.Status {
		case StepRunning:
			step.Output, step.Truncated = op.tails[step.Name].text(limit)
		case StepDone, StepFailed:
			if text, cut := lastBytes(step.Output, limit); cut {
				step.Output, step.Truncated = text, true
			}
		}
	}
	return record
}

// update changes the record under its lock and writes it.
func (s *Service) update(op *operation, change func(record *Record)) {
	op.mu.Lock()
	change(&op.record)
	record := op.record.clone()
	op.mu.Unlock()
	if err := s.store.save(record); err != nil {
		s.logger.WithError(err).WithField("stack", record.Stack).Warn("deployment record not written")
	}
}

// stepRunner runs the commands of one step: each is noted in the record and
// its output goes to the step's tail as well as to the caller.
type stepRunner struct {
	service *Service
	op      *operation
	step    string
}

func (r stepRunner) Run(ctx context.Context, cmd Command, output io.Writer) error {
	display := cmd.Tool + " " + strings.Join(cmd.Args, " ")
	tail := r.op.tails[r.step]
	r.op.mu.Lock()
	if step := r.op.record.step(r.step); step != nil {
		step.Commands = append(step.Commands, display)
	}
	r.op.mu.Unlock()
	fmt.Fprintf(tail, "$ %s\n", display)
	if output == nil {
		output = io.Discard
	}
	return r.service.opts.Runner.Run(ctx, cmd, io.MultiWriter(output, tail))
}

func minutes(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Seconds()))
	}
	return fmt.Sprintf("%d min", int(d.Minutes()))
}

// do runs one step with its deadline. On failure the following steps are
// marked skipped and the operation is over: it returns false.
func (s *Service) do(op *operation, name string, timeout time.Duration, fn func(ctx context.Context, run Runner, out io.Writer) error) bool {
	started := s.opts.Now()
	s.update(op, func(record *Record) {
		step := record.step(name)
		step.Status, step.StartedAt = StepRunning, started.UTC().Format(time.RFC3339)
	})
	ctx, cancel := context.WithTimeout(op.ctx, timeout)
	var err error
	if err = op.ctx.Err(); err == nil {
		err = fn(ctx, stepRunner{service: s, op: op, step: name}, op.tails[name])
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel()
	output, truncated := op.tails[name].text(maxStepOutput)
	s.update(op, func(record *Record) {
		step := record.step(name)
		step.DurationMs = s.opts.Now().Sub(started).Milliseconds()
		step.Output, step.Truncated = output, truncated
		if err == nil {
			step.Status = StepDone
			return
		}
		step.Status = StepFailed
		record.Result = ResultFailed
		switch {
		case op.ctx.Err() != nil:
			step.Error = "annulée"
			record.Result = ResultCancelled
		case timedOut:
			step.Error = "délai maximum dépassé (" + minutes(timeout) + ")"
		default:
			step.Error = Redact(err.Error())
		}
		record.Error = "étape « " + name + " » : " + step.Error
		for i := range record.Steps {
			if record.Steps[i].Status == StepPending {
				record.Steps[i].Status = StepSkipped
			}
		}
	})
	return err == nil
}

// finish closes the record and frees the stack.
func (s *Service) finish(op *operation) {
	s.update(op, func(record *Record) {
		record.FinishedAt = s.stamp()
		if record.Result == ResultRunning {
			record.Result = ResultSuccess
		}
	})
	op.cancel()
	record := op.snapshot(0)
	s.mu.Lock()
	delete(s.running, record.Stack)
	s.mu.Unlock()
	s.store.prune(record.Stack)
	entry := s.logger.WithFields(logrus.Fields{
		"actor": record.Actor, "stack": record.Stack, "action": record.Kind, "deployment": record.ID, "result": record.Result,
	})
	if record.Result == ResultSuccess {
		entry.Info("application deployment finished")
	} else {
		entry.WithField("error", record.Error).Warn("application deployment finished")
	}
	close(op.done)
}

// failed turns the failure of a command into the error of its step: the
// output of the command is shown next to it.
func failed(what string, err error) error {
	return fmt.Errorf("%s a échoué (%v)", what, err)
}

func (s *Service) runDeploy(op *operation) {
	defer s.finish(op)
	dir := op.plan.workingDir

	if !s.do(op, stepCheck, stepTimeouts[stepCheck], func(ctx context.Context, run Runner, _ io.Writer) error {
		git := readGit(ctx, run, dir)
		switch {
		case !git.Repo:
			return errors.New(git.Error)
		case git.Error != "":
			return errors.New(git.Error)
		case git.Detached || git.Branch == "":
			return errors.New("le dossier n’est sur aucune branche (HEAD détachée) : rien à avancer")
		case git.Upstream == "":
			return fmt.Errorf("la branche « %s » ne suit aucune branche distante", git.Branch)
		case git.DirtyCount > 0:
			list := strings.Join(git.Dirty, ", ")
			if extra := git.DirtyCount - len(git.Dirty); extra > 0 {
				list += fmt.Sprintf(" et %d autres", extra)
			}
			return fmt.Errorf("des fichiers suivis ont des modifications non commitées : %s", list)
		}
		s.update(op, func(record *Record) {
			record.Branch, record.Upstream, record.CommitBefore = git.Branch, git.Upstream, git.Commit
		})
		return nil
	}) {
		return
	}

	if !s.do(op, stepFetch, stepTimeouts[stepFetch], func(ctx context.Context, run Runner, _ io.Writer) error {
		if err := run.Run(ctx, Command{Tool: ToolGit, Args: []string{"fetch"}, Dir: dir}, nil); err != nil {
			return failed("git fetch", err)
		}
		return nil
	}) {
		return
	}

	if !s.do(op, stepMerge, stepTimeouts[stepMerge], func(ctx context.Context, run Runner, _ io.Writer) error {
		// Fast-forward only, to the branch the current one follows: never a
		// merge commit, a reset, another branch nor a forced update.
		if err := run.Run(ctx, Command{Tool: ToolGit, Args: []string{"merge", "--ff-only", "@{upstream}"}, Dir: dir}, nil); err != nil {
			return fmt.Errorf("avance rapide impossible (%v) : la branche locale a sans doute des commits que la branche distante n’a pas", err)
		}
		commit, err := readHead(ctx, run, dir)
		if err != nil {
			return err
		}
		s.update(op, func(record *Record) { record.CommitAfter = commit })
		return nil
	}) {
		return
	}

	if !s.do(op, stepImages, stepTimeouts[stepImages], func(ctx context.Context, run Runner, out io.Writer) error {
		return s.keepImages(ctx, op, run, out)
	}) {
		return
	}

	if !s.do(op, stepBuild, stepTimeouts[stepBuild], func(ctx context.Context, run Runner, out io.Writer) error {
		select {
		case s.build <- struct{}{}:
		default:
			fmt.Fprintln(out, "Un autre build est en cours sur ce serveur : en attente de sa fin…")
			select {
			case s.build <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		defer func() { <-s.build }()
		if err := run.Run(ctx, Command{Tool: ToolDocker, Args: op.plan.composeArgs("build"), Dir: dir}, nil); err != nil {
			return failed("docker compose build", err)
		}
		return nil
	}) {
		return
	}

	if !s.do(op, stepUp, stepTimeouts[stepUp], func(ctx context.Context, run Runner, _ io.Writer) error {
		if err := run.Run(ctx, Command{Tool: ToolDocker, Args: op.plan.composeArgs("up", "-d"), Dir: dir}, nil); err != nil {
			return failed("docker compose up -d", err)
		}
		return nil
	}) {
		return
	}

	s.do(op, stepVerify, s.verifyBudget(), func(ctx context.Context, _ Runner, out io.Writer) error {
		return s.verify(ctx, op, out)
	})
}

// keepImages records, for each container of the stack, the image it uses,
// and puts a local rollback tag on it. Nothing is pushed.
func (s *Service) keepImages(ctx context.Context, op *operation, run Runner, out io.Writer) error {
	target, err := s.opts.Stacks.DeployTarget(ctx, op.plan.project)
	if err != nil {
		_, message := dockerui.ErrorStatus(err)
		return errors.New(message)
	}
	op.mu.Lock()
	started := op.record.StartedAt
	op.mu.Unlock()
	stamp := s.opts.Now().UTC().Format("20060102-150405")
	if at, err := time.Parse(time.RFC3339, started); err == nil {
		stamp = at.UTC().Format("20060102-150405")
	}
	var images []Image
	tagged := make(map[string]string) // tag -> image ID
	for _, service := range target.Services {
		if !imageIDPattern.MatchString(service.ImageID) {
			fmt.Fprintf(out, "%s : identifiant d’image inattendu, image non mémorisée\n", service.Container)
			continue
		}
		tag := rollbackTag(op.plan.project, service.Service, service.Image, stamp)
		if known, done := tagged[tag]; done && known != service.ImageID {
			// Two containers of one repository on different images: one tag each.
			tag += "-" + service.ImageID[len("sha256:"):len("sha256:")+12]
		}
		if _, done := tagged[tag]; !done {
			if err := run.Run(ctx, Command{Tool: ToolDocker, Args: []string{"image", "tag", service.ImageID, tag}, Dir: op.plan.workingDir}, nil); err != nil {
				return failed("docker image tag", err)
			}
			tagged[tag] = service.ImageID
		}
		images = append(images, Image{
			Service: service.Service, Container: service.Container, Image: service.Image, ImageID: service.ImageID, RollbackTag: tag,
		})
	}
	if len(images) == 0 {
		return errors.New("aucune image à mémoriser : un retour arrière serait impossible")
	}
	s.update(op, func(record *Record) { record.Images = images })
	return nil
}

func (s *Service) runRollback(op *operation, source Record) {
	defer s.finish(op)
	dir := op.plan.workingDir

	if !s.do(op, stepImages, stepTimeouts[stepImages], func(ctx context.Context, run Runner, out io.Writer) error {
		// The code does not move; its commit is noted when git can tell.
		if commit, err := readHead(ctx, run, dir); err == nil {
			s.update(op, func(record *Record) { record.CommitBefore, record.CommitAfter = commit, commit })
		}
		return s.keepImages(ctx, op, run, out)
	}) {
		return
	}

	if !s.do(op, stepRetag, stepTimeouts[stepRetag], func(ctx context.Context, run Runner, out io.Writer) error {
		type move struct{ tag, id, name string }
		var moves []move
		seen := make(map[string]string)
		for _, image := range source.Images {
			switch {
			case image.RollbackTag == "" || !imageIDPattern.MatchString(image.ImageID):
				fmt.Fprintf(out, "%s : aucune image mémorisée, service laissé tel quel\n", image.Service)
			case !retaggable(image.Image):
				fmt.Fprintf(out, "%s : image désignée par son empreinte ou son identifiant, laissée telle quelle\n", image.Service)
			case seen[image.Image] == "":
				seen[image.Image] = image.ImageID
				moves = append(moves, move{tag: image.RollbackTag, id: image.ImageID, name: image.Image})
			case seen[image.Image] != image.ImageID:
				fmt.Fprintf(out, "%s : une autre image était mémorisée sous le même nom, la première est gardée\n", image.Service)
			}
		}
		if len(moves) == 0 {
			return errors.New("aucune image à restaurer")
		}
		// Every kept image is checked before any name moves.
		for _, m := range moves {
			found := &limited{limit: 4096}
			if err := run.Run(ctx, Command{Tool: ToolDocker, Args: []string{"image", "inspect", "--format", "{{.Id}}", m.tag}, Dir: dir}, found); err != nil {
				return fmt.Errorf("l’image de retour arrière %s est introuvable (%v) : elle a peut-être été supprimée", m.tag, err)
			}
			if strings.TrimSpace(found.buf.String()) != m.id {
				return fmt.Errorf("l’étiquette %s ne désigne plus l’image mémorisée", m.tag)
			}
		}
		for _, m := range moves {
			if err := run.Run(ctx, Command{Tool: ToolDocker, Args: []string{"image", "tag", m.tag, m.name}, Dir: dir}, nil); err != nil {
				return failed("docker image tag", err)
			}
		}
		return nil
	}) {
		return
	}

	if !s.do(op, stepUp, stepTimeouts[stepUp], func(ctx context.Context, run Runner, _ io.Writer) error {
		if err := run.Run(ctx, Command{Tool: ToolDocker, Args: op.plan.composeArgs("up", "-d", "--no-build"), Dir: dir}, nil); err != nil {
			return failed("docker compose up -d --no-build", err)
		}
		return nil
	}) {
		return
	}

	s.do(op, stepVerify, s.verifyBudget(), func(ctx context.Context, _ Runner, out io.Writer) error {
		return s.verify(ctx, op, out)
	})
}

func (s *Service) verifyBudget() time.Duration {
	return s.opts.VerifyDelay + s.opts.VerifyTimeout + s.opts.VerifyInterval + 30*time.Second
}

// Verdicts of a container at the verification.
const (
	verdictOK      = "ok"
	verdictWaiting = "waiting"
	verdictFailed  = "failed"
)

// assess judges the containers of a stack after `up -d`.
func assess(services []dockerui.DeployService) []ServiceCheck {
	checks := make([]ServiceCheck, 0, len(services))
	for _, service := range services {
		check := ServiceCheck{Service: service.Service, Container: service.Container, State: service.State, Health: service.Health}
		switch service.State {
		case "running":
			switch service.Health {
			case "unhealthy":
				check.Verdict, check.Detail = verdictFailed, "healthcheck en échec"
			case "starting":
				check.Verdict, check.Detail = verdictWaiting, "healthcheck en cours"
			default:
				check.Verdict = verdictOK
			}
		case "created", "restarting":
			check.Verdict, check.Detail = verdictWaiting, "pas encore démarré"
			if service.State == "restarting" {
				check.Detail = "redémarre"
			}
		case "exited":
			// A one-shot service (a migration job) that ended well is fine.
			if service.ExitCode != nil && *service.ExitCode == 0 {
				check.Verdict, check.Detail = verdictOK, "terminé (code 0)"
			} else if service.ExitCode != nil {
				check.Verdict, check.Detail = verdictFailed, fmt.Sprintf("arrêté avec le code %d", *service.ExitCode)
			} else {
				check.Verdict, check.Detail = verdictFailed, "arrêté"
			}
		default:
			check.Verdict, check.Detail = verdictFailed, "état « "+service.State+" »"
		}
		checks = append(checks, check)
	}
	return checks
}

func describe(checks []ServiceCheck, verdict string) string {
	var parts []string
	for _, check := range checks {
		if check.Verdict == verdict {
			part := check.Container
			if check.Detail != "" {
				part += " (" + check.Detail + ")"
			}
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, ", ")
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// verify waits, then reads the containers until they all run (healthy when
// they have a healthcheck) on two readings in a row, one fails, or the
// maximum wait is over.
func (s *Service) verify(ctx context.Context, op *operation, out io.Writer) error {
	if err := sleep(ctx, s.opts.VerifyDelay); err != nil {
		return err
	}
	deadline := time.Now().Add(s.opts.VerifyTimeout)
	stable, last := 0, ""
	for {
		target, err := s.opts.Stacks.DeployTarget(ctx, op.plan.project)
		line := ""
		if err != nil {
			_, message := dockerui.ErrorStatus(err)
			line, stable = "lecture des conteneurs impossible : "+message, 0
		} else {
			checks := assess(target.Services)
			s.update(op, func(record *Record) { record.Checks = checks })
			if bad := describe(checks, verdictFailed); bad != "" {
				fmt.Fprintln(out, "En échec : "+bad)
				return errors.New("des conteneurs ne tournent pas correctement : " + bad)
			}
			if waiting := describe(checks, verdictWaiting); waiting != "" {
				line, stable = "En attente : "+waiting, 0
			} else {
				stable++
				line = fmt.Sprintf("%d conteneur(s) en état de marche", len(checks))
				if len(checks) == 0 {
					line, stable = "aucun conteneur", 0
				}
			}
		}
		if line != last {
			fmt.Fprintln(out, line)
			last = line
		}
		if stable >= 2 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pas en état de marche après %s : %s", minutes(s.opts.VerifyTimeout), strings.ToLower(line[:1])+line[1:])
		}
		if err := sleep(ctx, s.opts.VerifyInterval); err != nil {
			return err
		}
	}
}
