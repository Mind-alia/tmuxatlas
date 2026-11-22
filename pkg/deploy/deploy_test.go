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
	"sort"
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
	// tags is the image store of the fake Docker: name -> image ID. `image
	// tag`, `image rm` and `image inspect` act on it as Docker does.
	tags map[string]string
	// builds is what `compose build` produces: image name -> new image ID.
	builds map[string]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		on:     map[string]func(context.Context, io.Writer) error{},
		tags:   map[string]string{"postgres:16": imageDB, "shop-web": imageWeb},
		builds: map[string]string{},
	}
}

// image returns what a name designates in the fake image store.
func (f *fakeRunner) image(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tags[name]
}

// rollbackTags lists the atlas-rollback tags of the fake image store.
func (f *fakeRunner) rollbackTags() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for name := range f.tags {
		if strings.Contains(name, ":atlas-rollback-") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// docker plays the image commands on the fake store.
func (f *fakeRunner) docker(args []string, out io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(args) == 4 && args[0] == "image" && args[1] == "tag":
		id := args[2]
		if !strings.HasPrefix(id, "sha256:") {
			if id = f.tags[args[2]]; id == "" {
				io.WriteString(out, "Error response from daemon: No such image: "+args[2]+"\n")
				return errors.New("exit status 1")
			}
		}
		f.tags[args[3]] = id
	case len(args) == 4 && args[0] == "image" && args[1] == "rm":
		if _, found := f.tags[args[3]]; !found {
			io.WriteString(out, "Error response from daemon: No such image: "+args[3]+"\n")
			return errors.New("exit status 1")
		}
		delete(f.tags, args[3])
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect":
		id := f.tags[args[4]]
		if id == "" {
			io.WriteString(out, "Error response from daemon: No such image: "+args[4]+"\n")
			return errors.New("exit status 1")
		}
		io.WriteString(out, id+"\n")
	case args[0] == "compose" && args[len(args)-1] == "build":
		for name, id := range f.builds {
			f.tags[name] = id
		}
	}
	return nil
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
	case cmd.Tool == ToolDocker:
		return f.docker(cmd.Args, out)
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
	// The build gives the web service a new image; the database image comes
	// from a registry and stays what it was.
	f.runner.builds["shop-web"] = imageWebNew
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
		"docker image inspect --format {{.Id}} postgres:16",
		"docker image inspect --format {{.Id}} shop-web",
		"docker image rm --no-prune postgres:atlas-rollback-" + stamp,
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
	if statuses(record) != "check=done fetch=done merge=done images=done build=done up=done verify=done prune=done" {
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
		{Service: "db", Container: "shop-db-1", Image: "postgres:16", ImageID: imageDB, Status: ImageUnchanged},
		{Service: "web", Container: "shop-web-1", Image: "shop-web", ImageID: imageWeb, RollbackTag: "shop-web:atlas-rollback-" + stamp, Status: ImageRebuilt},
	}
	if !reflect.DeepEqual(record.Images, wantImages) {
		t.Fatalf("images : %+v", record.Images)
	}
	// Only the rebuilt image keeps a rollback tag; the shared one has none.
	if got := f.runner.rollbackTags(); !reflect.DeepEqual(got, []string{"shop-web:atlas-rollback-" + stamp}) {
		t.Fatalf("étiquettes de retour arrière : %v", got)
	}
	if f.runner.image("postgres:16") != imageDB || f.runner.image("shop-web:atlas-rollback-"+stamp) != imageWeb {
		t.Fatal("images déplacées")
	}
	if output := record.step(stepBuild).Output; !strings.Contains(output, "Images reconstruites : web ; inchangées : db") {
		t.Fatalf("sortie du build : %q", output)
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
	if record.Result != ResultFailed || statuses(record) != "check=failed fetch=skipped merge=skipped images=skipped build=skipped up=skipped verify=skipped prune=skipped" {
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
	if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=failed images=skipped build=skipped up=skipped verify=skipped prune=skipped" {
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
	if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=done images=done build=failed up=skipped verify=skipped prune=skipped" {
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
	// Nothing changed: the tags just made are removed and no rollback is offered.
	stamp := mustStamp(t, record.StartedAt)
	got := f.runner.mutating()
	if want := []string{
		"docker image rm --no-prune postgres:atlas-rollback-" + stamp,
		"docker image rm --no-prune shop-web:atlas-rollback-" + stamp,
	}; len(got) < 2 || !reflect.DeepEqual(got[len(got)-2:], want) {
		t.Fatalf("commandes : %v", got)
	}
	if tags := f.runner.rollbackTags(); len(tags) != 0 {
		t.Fatalf("étiquettes laissées : %v", tags)
	}
	for _, image := range record.Images {
		if image.Status != ImageUnchanged || image.RollbackTag != "" {
			t.Fatalf("images : %+v", record.Images)
		}
	}
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != "" || state.RollbackBlockedID != "" {
		t.Fatal("retour arrière proposé alors que rien n’a été relancé")
	}
}

func TestRollbackPutsTheKeptImagesBack(t *testing.T) {
	const imagePulled = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	f := newFixture(t, nil)
	f.runner.builds["shop-web"] = imageWebNew
	deployed := f.run(t, "shop")
	stamp := mustStamp(t, deployed.StartedAt)
	f.stacks.set("shop", func(target *dockerui.DeployTarget) { target.Services[1].ImageID = imageWebNew })
	// Meanwhile another application pulled a newer postgres:16: the name the
	// database service shares now designates another image.
	f.runner.mu.Lock()
	f.runner.tags["postgres:16"] = imagePulled
	f.runner.mu.Unlock()

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
		"docker image tag " + imageWebNew + " shop-web:atlas-rollback-" + now,
		"docker image inspect --format {{.Id}} shop-web:atlas-rollback-" + stamp,
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
	// The rebuilt image is back under its name; the shared image, its name
	// and its tags were never touched.
	if f.runner.image("shop-web") != imageWeb || f.runner.image("postgres:16") != imagePulled {
		t.Fatalf("images : web %s, postgres %s", f.runner.image("shop-web"), f.runner.image("postgres:16"))
	}
	for _, call := range f.runner.commands() {
		if strings.HasPrefix(call, "docker image tag") && strings.HasSuffix(call, " postgres:16") {
			t.Fatalf("nom partagé ré-étiqueté : %s", call)
		}
	}
	if got := f.runner.rollbackTags(); !reflect.DeepEqual(got, []string{"shop-web:atlas-rollback-" + stamp, "shop-web:atlas-rollback-" + now}) {
		t.Fatalf("étiquettes : %v", got)
	}
	// The code did not move, and no git command changed anything.
	if record.CommitBefore == nil || record.CommitAfter == nil || record.CommitBefore.SHA != record.CommitAfter.SHA {
		t.Fatalf("commits : %+v %+v", record.CommitBefore, record.CommitAfter)
	}
	// Only the image the rollback replaced is kept.
	wantImages := []Image{
		{Service: "db", Container: "shop-db-1", Image: "postgres:16", ImageID: imageDB, Status: ImageUnchanged},
		{Service: "web", Container: "shop-web-1", Image: "shop-web", ImageID: imageWebNew, RollbackTag: "shop-web:atlas-rollback-" + now, Status: ImageReplaced},
	}
	if !reflect.DeepEqual(record.Images, wantImages) {
		t.Fatalf("images : %+v", record.Images)
	}
	state, _ := f.service.State(context.Background(), "shop")
	if state.RollbackID != "" || state.RollbackBlockedID != "" || len(state.History) != 2 || state.History[0].Kind != KindRollback {
		t.Fatalf("état après retour arrière : %+v", state)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", deployed.ID, "admin"); refusal(t, err).Status != http.StatusConflict {
		t.Fatalf("second retour arrière : %v", err)
	}
}

// A deployment that rebuilt nothing leaves nothing to restore: its images
// come from a registry and may be shared with other applications.
func TestRollbackIsRefusedWhenNothingWasRebuilt(t *testing.T) {
	f := newFixture(t, nil)
	deployed := f.run(t, "shop")
	if deployed.Result != ResultSuccess || len(f.runner.rollbackTags()) != 0 {
		t.Fatalf("déploiement : %s, étiquettes %v", deployed.Result, f.runner.rollbackTags())
	}
	state, _ := f.service.State(context.Background(), "shop")
	if state.RollbackID != "" || state.RollbackBlockedID != deployed.ID || !strings.Contains(state.RollbackBlocked, "rien à restaurer") {
		t.Fatalf("état : %q %q %q", state.RollbackID, state.RollbackBlockedID, state.RollbackBlocked)
	}
	before := len(f.runner.commands())
	_, err := f.service.Rollback(context.Background(), "shop", deployed.ID, "admin")
	if refused := refusal(t, err); refused.Status != http.StatusConflict || !strings.Contains(refused.Message, "rien à restaurer") {
		t.Fatalf("refus : %d %s", refused.Status, refused.Message)
	}
	if got := f.runner.commands()[before:]; len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}

	// A record written before images had a status says nothing of what was
	// rebuilt: its tags are not trusted either.
	legacy := deployed
	legacy.ID = "20990101T000000.000000000Z-00000000"
	legacy.Images = []Image{{Service: "db", Container: "shop-db-1", Image: "postgres:16", ImageID: imageDB, RollbackTag: "postgres:atlas-rollback-20990101-000000"}}
	if err := f.service.store.save(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Rollback(context.Background(), "shop", legacy.ID, "admin"); !strings.Contains(refusal(t, err).Message, "rien à restaurer") {
		t.Fatalf("ancien enregistrement : %v", err)
	}
	if got := f.runner.commands()[before:]; len(got) != 0 {
		t.Fatalf("commandes : %v", got)
	}
}

// After a successful deployment only the three most recent rollback tags of
// each rebuilt image stay, and nothing but tags Atlas made is removed.
func TestOldRollbackTagsArePruned(t *testing.T) {
	var clock sync.Mutex
	now := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	f := newFixture(t, func(options *Options) {
		options.Now = func() time.Time {
			clock.Lock()
			defer clock.Unlock()
			now = now.Add(time.Minute)
			return now
		}
	})
	// Tags that look alike but that no record of Atlas knows.
	f.runner.mu.Lock()
	f.runner.tags["shop-web:atlas-rollback-manual"] = imageWeb
	f.runner.tags["shop-web:atlas-rollback-20200101-000000"] = imageWeb
	f.runner.tags["shop-web:v1"] = imageWeb
	f.runner.mu.Unlock()

	var stamps []string
	var records []Record
	for i := 0; i < 5; i++ {
		built := fmt.Sprintf("sha256:%064d", 5000+i)
		f.runner.builds["shop-web"] = built
		before := len(f.runner.commands())
		record := f.run(t, "shop")
		if record.Result != ResultSuccess || statuses(record) != "check=done fetch=done merge=done images=done build=done up=done verify=done prune=done" {
			t.Fatalf("déploiement %d : %s %q", i, record.Result, record.Error)
		}
		records = append(records, record)
		stamps = append(stamps, mustStamp(t, record.StartedAt))
		f.stacks.set("shop", func(target *dockerui.DeployTarget) { target.Services[1].ImageID = built })
		var removed []string
		for _, call := range f.runner.commands()[before:] {
			if strings.HasPrefix(call, "docker image rm") {
				removed = append(removed, strings.TrimPrefix(call, "docker image rm --no-prune "))
			}
		}
		// Each deployment drops the tag of the unchanged database image, then
		// from the fourth on the oldest tag of the rebuilt one, last.
		want := []string{"postgres:atlas-rollback-" + stamps[i]}
		if i >= keptTags {
			want = append(want, "shop-web:atlas-rollback-"+stamps[i-keptTags])
		}
		if !reflect.DeepEqual(removed, want) {
			t.Fatalf("déploiement %d, étiquettes retirées : %v, attendu %v", i, removed, want)
		}
	}
	want := []string{
		"shop-web:atlas-rollback-20200101-000000",
		"shop-web:atlas-rollback-" + stamps[2], "shop-web:atlas-rollback-" + stamps[3], "shop-web:atlas-rollback-" + stamps[4],
		"shop-web:atlas-rollback-manual",
	}
	if got := f.runner.rollbackTags(); !reflect.DeepEqual(got, want) {
		t.Fatalf("étiquettes gardées : %v\nattendu : %v", got, want)
	}
	if f.runner.image("shop-web:v1") != imageWeb || f.runner.image("postgres:16") != imageDB {
		t.Fatal("une autre étiquette a été touchée")
	}
	// The records forget the tags that are gone and keep the others.
	for i, record := range records {
		stored, err := f.service.Record("shop", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		tag := stored.Images[1].RollbackTag
		if kept := i >= 2; kept != (tag == "shop-web:atlas-rollback-"+stamps[i]) || (!kept && tag != "") {
			t.Errorf("déploiement %d : étiquette enregistrée %q", i, tag)
		}
	}
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != records[4].ID {
		t.Fatalf("retour arrière : %q", state.RollbackID)
	}
}

func TestOnlyRollbackTagsAreEverRemoved(t *testing.T) {
	for tag, want := range map[string]bool{
		"shop-web:atlas-rollback-20260105-100000":                           true,
		"registry.example.com:5000/team/app:atlas-rollback-20260105-100000": true,
		"atlas-rollback/shop-web:atlas-rollback-20260105-100000":            true,
		"shop-web:atlas-rollback-20260105-100000-0123456789ab":              true,
		"shop-web:atlas-rollback-manual":                                    false,
		"shop-web:latest":                                                   false,
		"shop-web":                                                          false,
		"postgres:16":                                                       false,
		"shop-web:atlas-rollback-20260105-100000 --force":                   false,
		"--all":                             false,
		"":                                  false,
		"sha256:" + strings.Repeat("a", 64): false,
	} {
		if isRollbackTag(tag) != want {
			t.Errorf("isRollbackTag(%q) = %v", tag, !want)
		}
	}
	f := newFixture(t, nil)
	f.runner.mu.Lock()
	f.runner.tags["shop-web:atlas-rollback-20260105-100000"] = imageWeb
	f.runner.mu.Unlock()
	gone := f.service.untag(f.runner, io.Discard, f.appDir, []string{
		"shop-web", "postgres:16", "--all", imageWeb, "shop-web:atlas-rollback-20260105-100000", "shop-web:atlas-rollback-20260105-100000",
		"shop-web:atlas-rollback-20990101-000000",
	})
	if got := f.runner.commands(); !reflect.DeepEqual(got, []string{
		"docker image rm --no-prune shop-web:atlas-rollback-20260105-100000",
		"docker image rm --no-prune shop-web:atlas-rollback-20990101-000000",
	}) {
		t.Fatalf("commandes : %v", got)
	}
	// A tag Docker no longer has counts as gone.
	if len(gone) != 2 || f.runner.image("shop-web") != imageWeb || f.runner.image("postgres:16") != imageDB {
		t.Fatalf("retirées : %v", gone)
	}
}

func TestRollbackStopsWhenAKeptImageIsGoneOrMoved(t *testing.T) {
	for name, inspect := range map[string]func(out io.Writer) error{
		"gone":  func(out io.Writer) error { return errors.New("exit status 1") },
		"moved": func(out io.Writer) error { io.WriteString(out, imageDB+"\n"); return nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.runner.builds["shop-web"] = imageWebNew
			deployed := f.run(t, "shop")
			f.runner.on["docker image inspect --format {{.Id}} shop-web:atlas-rollback-"] = func(_ context.Context, out io.Writer) error { return inspect(out) }
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
				if strings.Contains(call, "compose") || strings.HasPrefix(call, "docker image tag shop-web:atlas-rollback-") {
					t.Fatalf("un nom d’image a bougé ou un service a été relancé : %s", call)
				}
			}
			if f.runner.image("shop-web") != imageWebNew {
				t.Fatal("le nom de l’image a bougé")
			}
			// Nothing was replaced: the rollback keeps no tag of its own.
			for _, image := range record.Images {
				if image.RollbackTag != "" || image.Status != ImageUnchanged {
					t.Fatalf("images : %+v", record.Images)
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
	// Both get a tag before the build, under a name made for them, and lose
	// it after: a name by digest or by ID cannot be rebuilt.
	for _, want := range []string{
		"docker image tag " + imageDB + " postgres:atlas-rollback-" + stamp,
		"docker image tag " + imageWeb + " atlas-rollback/shop-web:atlas-rollback-" + stamp,
		"docker image rm --no-prune postgres:atlas-rollback-" + stamp,
		"docker image rm --no-prune atlas-rollback/shop-web:atlas-rollback-" + stamp,
	} {
		found := false
		for _, call := range f.runner.mutating() {
			found = found || call == want
		}
		if !found {
			t.Errorf("commande absente : %s\n%s", want, strings.Join(f.runner.mutating(), "\n"))
		}
	}
	for _, call := range f.runner.commands() {
		if strings.HasPrefix(call, "docker image inspect") {
			t.Errorf("image épinglée inspectée : %s", call)
		}
	}
	if tags := f.runner.rollbackTags(); len(tags) != 0 || deployed.Images[0].Status != ImageUnchanged || deployed.Images[1].Status != ImageUnchanged {
		t.Fatalf("étiquettes : %v, images : %+v", tags, deployed.Images)
	}
	if state, _ := f.service.State(context.Background(), "shop"); state.RollbackID != "" || state.RollbackBlockedID != deployed.ID {
		t.Fatalf("retour arrière proposé : %+v", state)
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
	if record.Result != ResultCancelled || statuses(record) != "check=done fetch=done merge=done images=done build=failed up=skipped verify=skipped prune=skipped" {
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
			f.runner.builds["shop-web"] = imageWebNew
			f.stacks.set("shop", func(target *dockerui.DeployTarget) { tc.change(&target.Services[1]) })
			record := f.run(t, "shop")
			if record.Result != ResultFailed || statuses(record) != "check=done fetch=done merge=done images=done build=done up=done verify=failed prune=skipped" {
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
	if len(build.Output) > maxStepOutput || !build.Truncated || !strings.Contains(build.Output, "\nlast line\n") {
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
