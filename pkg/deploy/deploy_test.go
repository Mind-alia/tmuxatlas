package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
)

const (
	imageWeb    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageDB     = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	imageWebNew = "sha256:3333333333333333333333333333333333333333333333333333333333333333"

	cleanStatus = "# branch.oid 1111111111111111111111111111111111111111\n# branch.head main\n# branch.upstream origin/main\n# branch.ab +0 -2\n"
	headBefore  = "1111111111111111111111111111111111111111\x1f1111111\x1f2026-01-05T10:00:00+00:00\x1fAdd the cart page\n"
	headAfter   = "2222222222222222222222222222222222222222\x1f2222222\x1f2026-01-06T09:30:00+00:00\x1fFix the checkout total\n"
)

// fakeRunner records every command instead of running it. No test of this
// package ever starts git nor docker.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	dirs  []string
	// on answers the commands whose line starts with a key; the longest key wins.
	on map[string]func(ctx context.Context, out io.Writer) error
	// merged is set once `git merge` ran: the head is then the new commit.
	merged bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{on: map[string]func(context.Context, io.Writer) error{}}
}

func (f *fakeRunner) Run(ctx context.Context, cmd Command, out io.Writer) error {
	line := cmd.Tool + " " + strings.Join(cmd.Args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.dirs = append(f.dirs, cmd.Dir)
	var handler func(context.Context, io.Writer) error
	best := -1
	for prefix, fn := range f.on {
		if strings.HasPrefix(line, prefix) && len(prefix) > best {
			handler, best = fn, len(prefix)
		}
	}
	merged := f.merged
	if strings.HasPrefix(line, "git merge") {
		f.merged = true
	}
	f.mu.Unlock()
	if handler != nil {
		return handler(ctx, out)
	}
	switch {
	case strings.HasPrefix(line, "git --no-optional-locks status"):
		io.WriteString(out, cleanStatus)
	case strings.HasPrefix(line, "git --no-optional-locks log"):
		if merged {
			io.WriteString(out, headAfter)
		} else {
			io.WriteString(out, headBefore)
		}
	}
	return nil
}

func (f *fakeRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// mutating returns the commands that are not the two read-only git ones.
func (f *fakeRunner) mutating() []string {
	var out []string
	for _, call := range f.commands() {
		if !strings.HasPrefix(call, "git --no-optional-locks ") {
			out = append(out, call)
		}
	}
	return out
}

// fakeStacks describes invented Compose projects.
type fakeStacks struct {
	mu      sync.Mutex
	targets map[string]dockerui.DeployTarget
	err     error
}

func (f *fakeStacks) DeployTarget(_ context.Context, project string) (dockerui.DeployTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return dockerui.DeployTarget{}, f.err
	}
	target, ok := f.targets[project]
	if !ok {
		return dockerui.DeployTarget{}, &dockerui.APIError{Status: http.StatusNotFound}
	}
	target.Services = append([]dockerui.DeployService(nil), target.Services...)
	return target, nil
}

func (f *fakeStacks) set(project string, change func(target *dockerui.DeployTarget)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.targets[project]
	change(&target)
	f.targets[project] = target
}

type fixture struct {
	service *Service
	runner  *fakeRunner
	stacks  *fakeStacks
	appDir  string
	compose string
	dataDir string
}

func shopTarget(dir string) dockerui.DeployTarget {
	return dockerui.DeployTarget{
		Project: "shop", WorkingDir: dir, ConfigFiles: []string{filepath.Join(dir, "compose.yaml")},
		Services: []dockerui.DeployService{
			{ContainerID: "c1", Container: "shop-db-1", Service: "db", Image: "postgres:16", ImageID: imageDB, State: "running", Health: "healthy"},
			{ContainerID: "c2", Container: "shop-web-1", Service: "web", Image: "shop-web", ImageID: imageWeb, State: "running"},
		},
	}
}

func newFixture(t *testing.T, change func(options *Options)) *fixture {
	t.Helper()
	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "shop")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(appDir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		runner: newFakeRunner(), appDir: appDir, compose: compose, dataDir: filepath.Join(root, "data"),
		stacks: &fakeStacks{targets: map[string]dockerui.DeployTarget{"shop": shopTarget(appDir)}},
	}
	options := Options{
		Dir: f.dataDir, Runner: f.runner, Stacks: f.stacks, Enabled: true,
		VerifyDelay: time.Millisecond, VerifyInterval: time.Millisecond, VerifyTimeout: 2 * time.Second,
	}
	if change != nil {
		change(&options)
	}
	f.service = New(options)
	return f
}

// run starts a deployment and waits for its end.
func (f *fixture) run(t *testing.T, stack string) Record {
	t.Helper()
	started, err := f.service.Deploy(context.Background(), stack, "admin")
	if err != nil {
		t.Fatalf("déploiement refusé : %v", err)
	}
	f.service.Wait(stack)
	record, err := f.service.Record(stack, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func statuses(record Record) string {
	var parts []string
	for _, step := range record.Steps {
		parts = append(parts, step.Name+"="+step.Status)
	}
	return strings.Join(parts, " ")
}

func refusal(t *testing.T, err error) *Error {
	t.Helper()
	var refused *Error
	if !errors.As(err, &refused) {
		t.Fatalf("refus attendu, reçu %v", err)
	}
	return refused
}

func TestDeployRunsTheStepsInOrder(t *testing.T) {
	f := newFixture(t, nil)
	record := f.run(t, "shop")

	stamp := mustStamp(t, record.StartedAt)
	compose := "docker compose -p shop --project-directory " + f.appDir + " -f " + f.compose
	want := []string{
		"git --no-optional-locks status --porcelain=v2 --branch --untracked-files=no",
		"git --no-optional-locks log -1 --format=%H%x1f%h%x1f%cI%x1f%s",
		"git fetch",
		"git merge --ff-only @{upstream}",
		"git --no-optional-locks log -1 --format=%H%x1f%h%x1f%cI%x1f%s",
		"docker image tag " + imageDB + " postgres:atlas-rollback-" + stamp,
		"docker image tag " + imageWeb + " shop-web:atlas-rollback-" + stamp,
		compose + " build",
		compose + " up -d",
	}
	if got := f.runner.commands(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commandes :\n%s\nattendu :\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, dir := range f.runner.dirs {
		if dir != f.appDir {
			t.Errorf("commande %d lancée dans %q", i, dir)
		}
	}
	if record.Result != ResultSuccess || record.Error != "" || record.FinishedAt == "" {
		t.Fatalf("résultat : %s %q (%s)", record.Result, record.Error, statuses(record))
	}
	if statuses(record) != "check=done fetch=done merge=done images=done build=done up=done verify=done" {
		t.Fatalf("étapes : %s", statuses(record))
	}
	if record.Actor != "admin" || record.Kind != KindDeploy || record.Branch != "main" || record.Upstream != "origin/main" {
		t.Fatalf("enregistrement : %+v", record)
	}
	if record.CommitBefore == nil || record.CommitBefore.Short != "1111111" || record.CommitAfter == nil ||
		record.CommitAfter.Short != "2222222" || record.CommitAfter.Subject != "Fix the checkout total" {
		t.Fatalf("commits : %+v %+v", record.CommitBefore, record.CommitAfter)
	}
	wantImages := []Image{
		{Service: "db", Container: "shop-db-1", Image: "postgres:16", ImageID: imageDB, RollbackTag: "postgres:atlas-rollback-" + stamp},
		{Service: "web", Container: "shop-web-1", Image: "shop-web", ImageID: imageWeb, RollbackTag: "shop-web:atlas-rollback-" + stamp},
	}
	if !reflect.DeepEqual(record.Images, wantImages) {
		t.Fatalf("images : %+v", record.Images)
	}
	if len(record.Checks) != 2 || record.Checks[0].Verdict != verdictOK {
		t.Fatalf("vérification : %+v", record.Checks)
	}
	if step := record.step(stepFetch); !reflect.DeepEqual(step.Commands, []string{"git fetch"}) || !strings.Contains(step.Output, "$ git fetch") {
		t.Fatalf("étape fetch : %+v", step)
	}

	// The record is on disk, private to the hub's account.
	path := filepath.Join(f.dataDir, "shop", record.ID+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("fichier de l’enregistrement : %v %v", info, err)
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Fatalf("dossier des enregistrements : %o", dir.Mode().Perm())
	}

	state, err := f.service.State(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if state.Current != nil || len(state.History) != 1 || state.History[0].ID != record.ID || state.RollbackID != record.ID {
		t.Fatalf("état : %+v", state)
	}
	if state.History[0].Steps[2].Output != "" {
		t.Fatal("l’historique listé porte la sortie des commandes")
	}
	if !state.Git.Repo || state.Git.Branch != "main" || state.Git.Behind != 2 || !state.Git.Tracking || state.Git.Commit == nil {
		t.Fatalf("état git : %+v", state.Git)
	}
	if !reflect.DeepEqual(state.Services, []string{"db", "web"}) {
		t.Fatalf("services : %v", state.Services)
	}
}

func mustStamp(t *testing.T, started string) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339, started)
	if err != nil {
		t.Fatal(err)
	}
	return at.UTC().Format("20060102-150405")
}

func TestEnvFilesOfTheStackAreKept(t *testing.T) {
	f := newFixture(t, nil)
	env := filepath.Join(f.appDir, "prod.env")
	if err := os.WriteFile(env, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.stacks.set("shop", func(target *dockerui.DeployTarget) { target.EnvFiles = []string{env} })
	f.run(t, "shop")
	want := "docker compose -p shop --project-directory " + f.appDir + " --env-file " + env + " -f " + f.compose + " build"
	if got := f.runner.mutating(); len(got) < 5 || got[4] != want {
		t.Fatalf("commandes : %v", got)
	}
}

func TestDirtyWorkingTreeIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	f.runner.on["git --no-optional-locks status"] = func(_ context.Context, out io.Writer) error {
		io.WriteString(out, cleanStatus+
			"1 .M N... 100644 100644 100644 aaaa bbbb src/app.py\n"+
			"2 R. N... 100644 100644 100644 aaaa bbbb R100 docs/new name.md\tdocs/old.md\n")
		return nil
	}
	record := f.run(t, "shop")
	if record.Result != ResultFailed || statuses(record) != "check=failed fetch=skipped merge=skipped images=skipped build=skipped up=skipped verify=skipped" {
		t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
	}
	for _, want := range []string{"non commitées", "M src/app.py", "R docs/new name.md"} {
		if !strings.Contains(record.Error, want) {
			t.Errorf("l’erreur ne dit pas %q : %s", want, record.Error)
		}
	}
	if got := f.runner.mutating(); len(got) != 0 {
		t.Fatalf("commandes lancées malgré le refus : %v", got)
	}
}

func TestCheckRefusesWhatCannotBeFastForwarded(t *testing.T) {
	for name, tc := range map[string]struct {
		status func(out io.Writer) error
		want   string
	}{
		"not a repository": {func(out io.Writer) error {
			io.WriteString(out, "fatal: not a git repository (or any of the parent directories): .git\n")
			return errors.New("exit status 128")
		}, "pas dans un dépôt git"},
		"detached": {func(out io.Writer) error {
			io.WriteString(out, "# branch.oid 1111\n# branch.head (detached)\n")
			return nil
		}, "HEAD détachée"},
		"no upstream": {func(out io.Writer) error {
			io.WriteString(out, "# branch.oid 1111\n# branch.head feature\n")
			return nil
		}, "ne suit aucune branche distante"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.runner.on["git --no-optional-locks status"] = func(_ context.Context, out io.Writer) error { return tc.status(out) }
			record := f.run(t, "shop")
			if record.Result != ResultFailed || !strings.Contains(record.Error, tc.want) {
				t.Fatalf("résultat : %s %q", record.Result, record.Error)
			}
			if got := f.runner.mutating(); len(got) != 0 {
				t.Fatalf("commandes lancées : %v", got)
			}
		})
	}
}

func TestFastForwardFailureStopsBeforeDocker(t *testing.T) {
	f := newFixture(t, nil)
	f.runner.on["git merge"] = func(_ context.Context, out io.Writer) error {
		io.WriteString(out, "fatal: Not possible to fast-forward, aborting.\n")
		return errors.New("exit status 128")
	}
	record := f.run(t, "shop")
	if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=failed images=skipped build=skipped up=skipped verify=skipped" {
		t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
	}
	if !strings.Contains(record.Error, "avance rapide impossible") || !strings.Contains(record.step(stepMerge).Output, "Not possible to fast-forward") {
		t.Fatalf("erreur : %q, sortie : %q", record.Error, record.step(stepMerge).Output)
	}
	if got := f.runner.mutating(); !reflect.DeepEqual(got, []string{"git fetch", "git merge --ff-only @{upstream}"}) {
		t.Fatalf("commandes : %v", got)
	}
	// Never a reset, a checkout nor a forced update.
	for _, call := range f.runner.commands() {
		for _, forbidden := range []string{"reset", "checkout", "switch", "--force", "rebase", "push", "clean", "stash"} {
			if strings.Contains(call, forbidden) {
				t.Errorf("commande interdite : %s", call)
			}
		}
	}
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != "" {
		t.Fatal("retour arrière proposé alors que rien n’a été relancé")
	}
}

func TestBuildFailureRestartsNothing(t *testing.T) {
	f := newFixture(t, nil)
	f.runner.on["docker compose"] = func(_ context.Context, out io.Writer) error {
		io.WriteString(out, "failed to solve: process \"/bin/sh -c make\" did not complete successfully: exit code: 2\n")
		return errors.New("exit status 17")
	}
	record := f.run(t, "shop")
	if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=done images=done build=failed up=skipped verify=skipped" {
		t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
	}
	if !strings.Contains(record.Error, "docker compose build a échoué") || !strings.Contains(record.step(stepBuild).Output, "failed to solve") {
		t.Fatalf("erreur : %q", record.Error)
	}
	for _, call := range f.runner.commands() {
		if strings.Contains(call, " up ") {
			t.Fatalf("services relancés après un build en échec : %s", call)
		}
	}
	// The images were kept, but nothing changed: no rollback is offered.
	if len(record.Images) != 2 {
		t.Fatalf("images : %+v", record.Images)
	}
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != "" {
		t.Fatal("retour arrière proposé alors que rien n’a été relancé")
	}
}

func TestRollbackPutsTheKeptImagesBack(t *testing.T) {
	f := newFixture(t, nil)
	deployed := f.run(t, "shop")
	stamp := mustStamp(t, deployed.StartedAt)
	// The build gave the web service a new image.
	f.stacks.set("shop", func(target *dockerui.DeployTarget) { target.Services[1].ImageID = imageWebNew })
	inspected := map[string]string{"postgres": imageDB, "shop-web": imageWeb}
	for repo, id := range inspected {
		f.runner.on["docker image inspect --format {{.Id}} "+repo+":"] = func(_ context.Context, out io.Writer) error {
			io.WriteString(out, id+"\n")
			return nil
		}
	}

	if _, err := f.service.Rollback(context.Background(), "shop", "20200101T000000.000000000Z-00000000", "admin"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("retour vers un déploiement inconnu : %v", err)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", "../../etc/passwd", "admin"); refusal(t, err).Status != http.StatusBadRequest {
		t.Fatalf("identifiant invalide : %v", err)
	}
	before := len(f.runner.commands())
	// The clock moves between the two operations: their tags differ.
	time.Sleep(1100 * time.Millisecond)
	started, err := f.service.Rollback(context.Background(), "shop", deployed.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	f.service.Wait("shop")
	record, err := f.service.Record("shop", started.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := mustStamp(t, record.StartedAt)
	want := []string{
		"git --no-optional-locks log -1 --format=%H%x1f%h%x1f%cI%x1f%s",
		"docker image tag " + imageDB + " postgres:atlas-rollback-" + now,
		"docker image tag " + imageWebNew + " shop-web:atlas-rollback-" + now,
		"docker image inspect --format {{.Id}} postgres:atlas-rollback-" + stamp,
		"docker image inspect --format {{.Id}} shop-web:atlas-rollback-" + stamp,
		"docker image tag postgres:atlas-rollback-" + stamp + " postgres:16",
		"docker image tag shop-web:atlas-rollback-" + stamp + " shop-web",
		"docker compose -p shop --project-directory " + f.appDir + " -f " + f.compose + " up -d --no-build",
	}
	if got := f.runner.commands()[before:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("commandes :\n%s\nattendu :\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if record.Result != ResultSuccess || record.Kind != KindRollback || record.RollbackOf != deployed.ID ||
		statuses(record) != "images=done retag=done up=done verify=done" {
		t.Fatalf("résultat : %s %q (%s)", record.Result, record.Error, statuses(record))
	}
	// The code did not move, and no git command changed anything.
	if record.CommitBefore == nil || record.CommitAfter == nil || record.CommitBefore.SHA != record.CommitAfter.SHA {
		t.Fatalf("commits : %+v %+v", record.CommitBefore, record.CommitAfter)
	}
	// The image the rollback replaced is kept too.
	if record.Images[1].ImageID != imageWebNew || record.Images[1].RollbackTag != "shop-web:atlas-rollback-"+now {
		t.Fatalf("images : %+v", record.Images)
	}
	state, _ := f.service.State(context.Background(), "shop")
	if state.RollbackID != "" || len(state.History) != 2 || state.History[0].Kind != KindRollback {
		t.Fatalf("état après retour arrière : %+v", state)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", deployed.ID, "admin"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("second retour arrière : %v", err)
	}
}

func TestRollbackStopsWhenAKeptImageIsGoneOrMoved(t *testing.T) {
	for name, inspect := range map[string]func(out io.Writer) error{
		"gone":  func(out io.Writer) error { return errors.New("exit status 1") },
		"moved": func(out io.Writer) error { io.WriteString(out, imageWebNew+"\n"); return nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			deployed := f.run(t, "shop")
			f.runner.on["docker image inspect"] = func(_ context.Context, out io.Writer) error { return inspect(out) }
			before := len(f.runner.commands())
			started, err := f.service.Rollback(context.Background(), "shop", deployed.ID, "admin")
			if err != nil {
				t.Fatal(err)
			}
			f.service.Wait("shop")
			record, _ := f.service.Record("shop", started.ID)
			if record.Result != ResultFailed || statuses(record) != "images=done retag=failed up=skipped verify=skipped" {
				t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
			}
			for _, call := range f.runner.commands()[before:] {
				if strings.Contains(call, "compose") || strings.HasPrefix(call, "docker image tag postgres:atlas-rollback-") {
					t.Fatalf("un nom d’image a bougé ou un service a été relancé : %s", call)
				}
			}
		})
	}
}

func TestImagesPinnedByDigestAreLeftAlone(t *testing.T) {
	f := newFixture(t, nil)
	f.stacks.set("shop", func(target *dockerui.DeployTarget) {
		target.Services[0].Image = "postgres@sha256:" + strings.Repeat("a", 64)
		target.Services[1].Image = imageWeb
	})
	deployed := f.run(t, "shop")
	stamp := mustStamp(t, deployed.StartedAt)
	if deployed.Images[0].RollbackTag != "postgres:atlas-rollback-"+stamp || deployed.Images[1].RollbackTag != "atlas-rollback/shop-web:atlas-rollback-"+stamp {
		t.Fatalf("étiquettes : %+v", deployed.Images)
	}
	// Neither name can receive a tag: there is nothing to roll back to.
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != "" {
		t.Fatalf("retour arrière proposé : %s", state.RollbackID)
	}
}

func TestOneOperationPerStackAndOneBuildAtATime(t *testing.T) {
	f := newFixture(t, nil)
	other := filepath.Join(filepath.Dir(f.appDir), "blog")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blog := shopTarget(other)
	blog.Project = "blog"
	f.stacks.set("blog", func(target *dockerui.DeployTarget) { *target = blog })

	entered := make(chan string, 16)
	release := make(chan struct{})
	for _, project := range []string{"shop", "blog"} {
		f.runner.on["docker compose -p "+project+" "] = func(ctx context.Context, _ io.Writer) error {
			entered <- project
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	first, err := f.service.Deploy(context.Background(), "shop", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-entered; got != "shop" {
		t.Fatalf("premier build : %s", got)
	}
	// The same stack is refused while its deployment runs.
	if _, err := f.service.Deploy(context.Background(), "shop", "admin"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("second déploiement de la même application : %v", err)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", first.ID, "admin"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("retour arrière pendant un déploiement : %v", err)
	}
	state, err := f.service.State(context.Background(), "shop")
	if err != nil || state.Current == nil || state.Current.ID != first.ID || state.Current.step(stepBuild).Status != StepRunning || state.RollbackID != "" {
		t.Fatalf("état pendant le build : %+v %v", state, err)
	}
	if len(state.History) != 1 || state.History[0].Result != ResultRunning {
		t.Fatalf("historique pendant le build : %+v", state.History)
	}

	// Another stack starts, but its build waits for the first one.
	second, err := f.service.Deploy(context.Background(), "blog", "admin")
	if err != nil {
		t.Fatal(err)
	}
	waiting := func() bool {
		state, _ := f.service.State(context.Background(), "blog")
		return state.Current != nil && strings.Contains(state.Current.step(stepBuild).Output, "en attente de sa fin")
	}
	for deadline := time.Now().Add(5 * time.Second); !waiting(); {
		if time.Now().After(deadline) {
			t.Fatal("le second build n’attend pas")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case got := <-entered:
		t.Fatalf("deux builds en même temps : %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	f.service.Wait("shop")
	f.service.Wait("blog")
	for stack, id := range map[string]string{"shop": first.ID, "blog": second.ID} {
		if record, _ := f.service.Record(stack, id); record.Result != ResultSuccess {
			t.Errorf("%s : %s %q", stack, record.Result, record.Error)
		}
	}
}

func TestCancelStopsTheCommandInProgress(t *testing.T) {
	f := newFixture(t, nil)
	entered := make(chan struct{})
	f.runner.on["docker compose"] = func(ctx context.Context, _ io.Writer) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := f.service.Cancel("shop"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("annulation sans opération : %v", err)
	}
	started, err := f.service.Deploy(context.Background(), "shop", "admin")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if cancelled, err := f.service.Cancel("shop"); err != nil || !cancelled.CancelRequested {
		t.Fatalf("annulation : %+v %v", cancelled, err)
	}
	f.service.Wait("shop")
	record, _ := f.service.Record("shop", started.ID)
	if record.Result != ResultCancelled || statuses(record) != "check=done fetch=done merge=done images=done build=failed up=skipped verify=skipped" {
		t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
	}
	if record.step(stepBuild).Error != "annulée" {
		t.Fatalf("erreur de l’étape : %q", record.step(stepBuild).Error)
	}
	// The stack is free again.
	delete(f.runner.on, "docker compose")
	if record := f.run(t, "shop"); record.Result != ResultSuccess {
		t.Fatalf("après annulation : %s", record.Result)
	}
}

func TestStepDeadline(t *testing.T) {
	previous := stepTimeouts[stepFetch]
	stepTimeouts[stepFetch] = 20 * time.Millisecond
	t.Cleanup(func() { stepTimeouts[stepFetch] = previous })
	f := newFixture(t, nil)
	f.runner.on["git fetch"] = func(ctx context.Context, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	record := f.run(t, "shop")
	if record.Result != ResultFailed || !strings.Contains(record.step(stepFetch).Error, "délai maximum dépassé") {
		t.Fatalf("résultat : %s %q", record.Result, record.Error)
	}
	if got := f.runner.mutating(); !reflect.DeepEqual(got, []string{"git fetch"}) {
		t.Fatalf("commandes : %v", got)
	}
}

func TestDisabledSwitchIsReadOnly(t *testing.T) {
	f := newFixture(t, func(options *Options) { options.Enabled = false })
	state, err := f.service.State(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled || !strings.Contains(state.DisabledReason, "TMUXATLAS_DEPLOY=on") || !state.Git.Repo || state.Git.Behind != 2 {
		t.Fatalf("état : %+v", state)
	}
	if _, err := f.service.Deploy(context.Background(), "shop", "admin"); refusal(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("déploiement : %v", err)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", "20200101T000000.000000000Z-00000000", "admin"); refusal(t, err).Status != http.StatusServiceUnavailable {
		t.Fatalf("retour arrière : %v", err)
	}
	if got := f.runner.mutating(); len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}
	if entries, _ := os.ReadDir(f.dataDir); len(entries) != 0 {
		t.Fatal("un enregistrement a été écrit")
	}
}

func TestGuardsRefuseBeforeAnyCommand(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(f *fixture, target *dockerui.DeployTarget)
		want   string
	}{
		"hub in the stack":          {func(_ *fixture, target *dockerui.DeployTarget) { target.Hub = "shop-atlas-1" }, "contient Atlas"},
		"relative working dir":      {func(_ *fixture, target *dockerui.DeployTarget) { target.WorkingDir = "apps/shop" }, "chemin absolu"},
		"missing working dir":       {func(f *fixture, target *dockerui.DeployTarget) { target.WorkingDir = f.appDir + "-gone" }, "introuvable"},
		"working dir with dots":     {func(f *fixture, target *dockerui.DeployTarget) { target.WorkingDir = f.appDir + "/../shop" }, "normalisé"},
		"no compose file":           {func(_ *fixture, target *dockerui.DeployTarget) { target.ConfigFiles = nil }, "fichiers Compose"},
		"option as compose file":    {func(_ *fixture, target *dockerui.DeployTarget) { target.ConfigFiles = []string{"--volume=/:/host"} }, "chemin absolu"},
		"directory as compose file": {func(f *fixture, target *dockerui.DeployTarget) { target.ConfigFiles = []string{f.appDir} }, "fichier ordinaire"},
		"missing compose file": {func(f *fixture, target *dockerui.DeployTarget) {
			target.ConfigFiles = []string{filepath.Join(f.appDir, "absent.yaml")}
		}, "introuvable"},
		"missing env file": {func(f *fixture, target *dockerui.DeployTarget) {
			target.EnvFiles = []string{filepath.Join(f.appDir, "absent.env")}
		}, "fichier d’environnement"},
		"newline in path": {func(f *fixture, target *dockerui.DeployTarget) { target.WorkingDir = f.appDir + "\nx" }, "caractère de contrôle"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.stacks.set("shop", func(target *dockerui.DeployTarget) { tc.change(f, target) })
			_, err := f.service.Deploy(context.Background(), "shop", "admin")
			if refused := refusal(t, err); refused.Status != http.StatusConflict || !strings.Contains(refused.Message, tc.want) {
				t.Fatalf("refus : %d %s", refused.Status, refused.Message)
			}
			state, err := f.service.State(context.Background(), "shop")
			if err != nil || !strings.Contains(state.Blocked, tc.want) {
				t.Fatalf("état : %q %v", state.Blocked, err)
			}
			if got := f.runner.mutating(); len(got) != 0 {
				t.Fatalf("commandes : %v", got)
			}
		})
	}

	f := newFixture(t, nil)
	for _, name := range []string{"", "-p", "../shop", "shop;id", "shop name", "$(id)", strings.Repeat("a", 200)} {
		if _, err := f.service.Deploy(context.Background(), name, "admin"); refusal(t, err).Status != http.StatusBadRequest {
			t.Errorf("nom %q : %v", name, err)
		}
		if _, err := f.service.State(context.Background(), name); refusal(t, err).Status != http.StatusBadRequest {
			t.Errorf("état de %q : %v", name, err)
		}
	}
	if _, err := f.service.Deploy(context.Background(), "unknown", "admin"); refusal(t, err).Status != http.StatusNotFound {
		t.Errorf("application inconnue : %v", err)
	}
	if got := f.runner.commands(); len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}

	// A problem of the server (git missing) blocks every stack.
	f = newFixture(t, func(options *Options) { options.Problem = "git introuvable sur ce serveur" })
	if _, err := f.service.Deploy(context.Background(), "shop", "admin"); refusal(t, err).Message != "git introuvable sur ce serveur" {
		t.Fatalf("problème du serveur : %v", err)
	}
}

func TestVerification(t *testing.T) {
	zero, one := 0, 1
	t.Run("waits for the healthcheck", func(t *testing.T) {
		f := newFixture(t, nil)
		f.stacks.set("shop", func(target *dockerui.DeployTarget) {
			target.Services[0].Health = "starting"
			target.Services = append(target.Services, dockerui.DeployService{Container: "shop-migrate-1", Service: "migrate", Image: "shop-web", ImageID: imageWeb, State: "exited", ExitCode: &zero})
		})
		f.runner.on["docker compose -p shop --project-directory "+f.appDir+" -f "+f.compose+" up"] = func(context.Context, io.Writer) error {
			go func() {
				time.Sleep(30 * time.Millisecond)
				f.stacks.set("shop", func(target *dockerui.DeployTarget) { target.Services[0].Health = "healthy" })
			}()
			return nil
		}
		record := f.run(t, "shop")
		if record.Result != ResultSuccess {
			t.Fatalf("résultat : %s %q", record.Result, record.Error)
		}
		output := record.step(stepVerify).Output
		if !strings.Contains(output, "En attente : shop-db-1 (healthcheck en cours)") || !strings.Contains(output, "3 conteneur(s) en état de marche") {
			t.Fatalf("sortie : %q", output)
		}
		if record.Checks[2].Detail != "terminé (code 0)" {
			t.Fatalf("service ponctuel : %+v", record.Checks[2])
		}
	})
	for name, tc := range map[string]struct {
		change func(service *dockerui.DeployService)
		want   string
	}{
		"unhealthy":      {func(s *dockerui.DeployService) { s.Health = "unhealthy" }, "healthcheck en échec"},
		"exited badly":   {func(s *dockerui.DeployService) { s.State, s.ExitCode = "exited", &one }, "arrêté avec le code 1"},
		"never starts":   {func(s *dockerui.DeployService) { s.State = "restarting" }, "pas en état de marche après"},
		"always waiting": {func(s *dockerui.DeployService) { s.Health = "starting" }, "healthcheck en cours"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, func(options *Options) { options.VerifyTimeout = 30 * time.Millisecond })
			f.stacks.set("shop", func(target *dockerui.DeployTarget) { tc.change(&target.Services[1]) })
			record := f.run(t, "shop")
			if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=done images=done build=done up=done verify=failed" {
				t.Fatalf("résultat : %s (%s)", record.Result, statuses(record))
			}
			if !strings.Contains(record.Error, tc.want) {
				t.Fatalf("erreur : %q", record.Error)
			}
			// The services were restarted: the previous images can be put back.
			if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != record.ID {
				t.Fatalf("retour arrière : %q", state.RollbackID)
			}
		})
	}
}

func TestOutputIsMaskedAndBounded(t *testing.T) {
	f := newFixture(t, nil)
	f.runner.on["git fetch"] = func(_ context.Context, out io.Writer) error {
		io.WriteString(out, "From https://deploy:s3cr3t-pass@git.example.com/team/shop\n")
		return nil
	}
	f.runner.on["docker compose"] = func(_ context.Context, out io.Writer) error {
		for i := 0; i < 20000; i++ {
			fmt.Fprintf(out, "#%d step output line with API_TOKEN=tok-%d and more text\n", i, i)
		}
		io.WriteString(out, "\x1b[31mERROR\x1b[0m DATABASE_PASSWORD=\"hunter two\" NPM_KEY='abc' Authorization: Bearer abcdefghijklmnop\r\nlast line\n")
		return errors.New("exit status 1 with SECRET_KEY=leaked")
	}
	record := f.run(t, "shop")
	raw, err := os.ReadFile(filepath.Join(f.dataDir, "shop", record.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"s3cr3t-pass", "tok-19999", "tok-1", "hunter two", "'abc'", "abcdefghijklmnop", "leaked"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("le fichier contient %q", secret)
		}
	}
	build := record.step(stepBuild)
	if len(build.Output) > maxStepOutput || !build.Truncated || !strings.HasSuffix(build.Output, "last line\n") {
		t.Fatalf("sortie du build : %d octets, tronquée %v", len(build.Output), build.Truncated)
	}
	for _, want := range []string{"API_TOKEN=***", "DATABASE_PASSWORD=***", "NPM_KEY=***", "Bearer ***", "ERROR DATABASE"} {
		if !strings.Contains(build.Output, want) {
			t.Errorf("la sortie n’a pas %q", want)
		}
	}
	if !strings.HasPrefix(build.Output, "#") {
		t.Errorf("la sortie tronquée commence au milieu d’une ligne : %q", build.Output[:40])
	}
	if !strings.Contains(record.step(stepFetch).Output, "https://***@git.example.com/team/shop") {
		t.Errorf("sortie du fetch : %q", record.step(stepFetch).Output)
	}
	if !strings.Contains(build.Error, "SECRET_KEY=***") {
		t.Errorf("erreur du build : %q", build.Error)
	}
	// The polled state carries less than the record.
	state, _ := f.service.State(context.Background(), "shop")
	if len(raw) > maxRecordSize || state.History[0].Steps[4].Output != "" {
		t.Fatalf("taille de l’enregistrement : %d", len(raw))
	}
}

func TestRedact(t *testing.T) {
	for input, want := range map[string]string{
		"TOKEN=abc":                                "TOKEN=***",
		"export GITHUB_TOKEN = abc def":            "export GITHUB_TOKEN = *** def",
		"db_password=\"a b\" next":                 "db_password=*** next",
		"aws.secret-access-key='x y'":              "aws.secret-access-key=***",
		"PORT=8080 HOST=example.com":               "PORT=8080 HOST=example.com",
		"clone https://user:pw@example.com/r.git":  "clone https://***@example.com/r.git",
		"clone https://ghp_abc@example.com/r.git":  "clone https://***@example.com/r.git",
		"see https://example.com/a@b":              "see https://example.com/a@b",
		"header: Basic dXNlcjpwYXNzd29yZA==":       "header: Basic ***",
		"a plain line about a key and a password.": "a plain line about a key and a password.",
	} {
		if got := Redact(input); got != want {
			t.Errorf("Redact(%q) = %q, attendu %q", input, got, want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	state := parseStatus("# branch.oid abc\n# branch.head release/1.x\n# branch.upstream origin/release/1.x\n# branch.ab +3 -12\n" +
		"1 M. N... 100644 100644 100644 a b staged.txt\n" +
		"u UU N... 100644 100644 100644 100644 a b c conflict.txt\n" +
		"? untracked.txt\n")
	want := GitState{Repo: true, Branch: "release/1.x", Upstream: "origin/release/1.x", Tracking: true, Ahead: 3, Behind: 12,
		Dirty: []string{"M staged.txt", "UU conflict.txt"}, DirtyCount: 2}
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("état : %+v", state)
	}
	// A branch whose upstream is gone has no counts.
	if state := parseStatus("# branch.oid abc\n# branch.head main\n# branch.upstream origin/main\n"); state.Tracking || state.Upstream != "origin/main" {
		t.Fatalf("sans compteurs : %+v", state)
	}
	var many strings.Builder
	for i := 0; i < maxDirtyListed+5; i++ {
		fmt.Fprintf(&many, "1 .M N... 100644 100644 100644 a b file-%d\n", i)
	}
	if state := parseStatus(many.String()); state.DirtyCount != maxDirtyListed+5 || len(state.Dirty) != maxDirtyListed {
		t.Fatalf("liste bornée : %d %d", state.DirtyCount, len(state.Dirty))
	}
}

func TestImageNames(t *testing.T) {
	for image, want := range map[string]string{
		"shop-web":                                "shop-web:atlas-rollback-S",
		"shop-web:latest":                         "shop-web:atlas-rollback-S",
		"registry.example.com:5000/team/app:1":    "registry.example.com:5000/team/app:atlas-rollback-S",
		"registry.example.com:5000/team/app":      "registry.example.com:5000/team/app:atlas-rollback-S",
		"nginx@sha256:" + strings.Repeat("a", 64): "nginx:atlas-rollback-S",
		imageWeb: "atlas-rollback/my.shop-web_1:atlas-rollback-S",
		"--help": "atlas-rollback/my.shop-web_1:atlas-rollback-S",
	} {
		if got := rollbackTag("My.Shop", "web_1", image, "S"); got != want {
			t.Errorf("rollbackTag(%q) = %q, attendu %q", image, got, want)
		}
	}
	for image, want := range map[string]bool{
		"shop-web": true, "shop-web:v2": true, "registry.example.com:5000/team/app:1": true,
		"nginx@sha256:" + strings.Repeat("a", 64): false, imageWeb: false, "--help": false, "": false, "a b": false, "-t": false,
	} {
		if retaggable(image) != want {
			t.Errorf("retaggable(%q) = %v", image, !want)
		}
	}
}

func TestInterruptedRecordsAreClosedAtStartup(t *testing.T) {
	f := newFixture(t, nil)
	record := Record{
		ID: "20260105T100000.000000000Z-0a0b0c0d", Kind: KindDeploy, Stack: "shop", Actor: "admin", Result: ResultRunning,
		StartedAt: "2026-01-05T10:00:00Z", Images: []Image{},
		Steps: []Step{{Name: stepCheck, Status: StepDone}, {Name: stepBuild, Status: StepRunning}, {Name: stepUp, Status: StepPending}},
	}
	if err := f.service.store.save(record); err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{Dir: f.dataDir, Runner: f.runner, Stacks: f.stacks, Enabled: true})
	got, err := restarted.Record("shop", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != ResultInterrupted || got.FinishedAt == "" || statuses(got) != "check=done build=failed up=skipped" {
		t.Fatalf("enregistrement : %s (%s)", got.Result, statuses(got))
	}
	state, _ := restarted.State(context.Background(), "shop")
	if state.Current != nil || state.RollbackID != "" {
		t.Fatalf("état : %+v", state)
	}
}

func TestOldRecordsArePruned(t *testing.T) {
	f := newFixture(t, nil)
	for i := 0; i < keptRecords+5; i++ {
		record := Record{ID: fmt.Sprintf("202601%02dT1000%02d.000000000Z-00000000", 1+i/60, i%60), Kind: KindDeploy, Stack: "shop", Result: ResultFailed}
		if err := f.service.store.save(record); err != nil {
			t.Fatal(err)
		}
	}
	f.service.store.prune("shop")
	ids := f.service.store.ids("shop")
	if len(ids) != keptRecords || ids[0] != "20260101T100044.000000000Z-00000000" {
		t.Fatalf("enregistrements gardés : %d, le plus récent %s", len(ids), ids[0])
	}
	// A file that is not a record is neither listed nor read.
	if err := os.WriteFile(filepath.Join(f.dataDir, "shop", "notes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := f.service.store.ids("shop"); len(got) != keptRecords {
		t.Fatalf("fichier étranger listé : %d", len(got))
	}
	if _, err := f.service.Record("shop", "notes"); refusal(t, err).Status != http.StatusBadRequest {
		t.Fatalf("lecture d’un fichier étranger : %v", err)
	}
}
