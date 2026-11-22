package dbbackup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

// Invented data: no real container, name or password appears here.
const (
	pgID       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mariaID    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	webID      = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	pgSecret   = "pg-Secret-Value-42"
	mariaToken = "maria-Secret-Value-42"
	pgDump     = "-- PostgreSQL database cluster dump\nCREATE ROLE shop;\nCREATE TABLE orders (id int);\n"
)

// execCall is one program the fake was asked to run.
type execCall struct {
	container string
	argv      []string
	env       []string
	stdin     string
}

// fakeDocker stands for Docker in every test: nothing here reaches a socket.
type fakeDocker struct {
	mu         sync.Mutex
	problem    string
	containers []Container
	env        map[string][]string
	calls      []execCall
	// run answers an exec; the default dumps pgDump and accepts any restore.
	run func(call execCall, spec ExecSpec, ctx context.Context) (int, error)
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: []Container{
			{ID: pgID, Name: "shop-db-1", Image: "postgres:16"},
			{ID: mariaID, Name: "blog-db", Image: "mariadb:11"},
			{ID: webID, Name: "web", Image: "nginx:1.27"},
		},
		env: map[string][]string{
			pgID:    {"POSTGRES_USER=shop", "POSTGRES_PASSWORD=" + pgSecret, "PGDATA=/var/lib/postgresql/data"},
			mariaID: {"MARIADB_ROOT_PASSWORD=" + mariaToken},
			webID:   {"NGINX_VERSION=1.27"},
		},
	}
}

func (f *fakeDocker) Problem() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.problem
}

func (f *fakeDocker) Running(context.Context) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Container(nil), f.containers...), nil
}

func (f *fakeDocker) Env(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.env[id], nil
}

func (f *fakeDocker) Exec(ctx context.Context, id string, spec ExecSpec) (int, error) {
	call := execCall{container: id, argv: spec.Cmd, env: spec.Env}
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run != nil {
		code, err := run(call, spec, ctx)
		f.record(call)
		return code, err
	}
	if spec.Stdin != nil {
		data, _ := io.ReadAll(spec.Stdin)
		call.stdin = string(data)
	} else {
		_, _ = io.WriteString(spec.Stdout, pgDump)
	}
	f.record(call)
	return 0, nil
}

func (f *fakeDocker) record(call execCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDocker) executed() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execCall(nil), f.calls...)
}

func (f *fakeDocker) tools() string {
	var names []string
	for _, call := range f.executed() {
		names = append(names, call.argv[0])
	}
	return strings.Join(names, ",")
}

func (f *fakeDocker) setRun(run func(call execCall, spec ExecSpec, ctx context.Context) (int, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run = run
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var testZone = time.FixedZone("test", 3600)

func day(d, hour, minute int) time.Time { return time.Date(2026, 11, d, hour, minute, 0, 0, testZone) }

type fixture struct {
	*Manager
	docker *fakeDocker
	clock  *fakeClock
	dir    string
}

func newFixture(t *testing.T, change func(*Options)) *fixture {
	t.Helper()
	f := &fixture{docker: newFakeDocker(), clock: &fakeClock{now: day(22, 10, 0)}, dir: filepath.Join(t.TempDir(), "db-backups")}
	f.open(t, change)
	return f
}

// open builds the manager again on the same directory, as a restart does.
func (f *fixture) open(t *testing.T, change func(*Options)) {
	t.Helper()
	opts := Options{
		Dir: f.dir, Docker: f.docker, Now: f.clock.Now,
		FreeBytes: func(string) (uint64, error) { return 100 << 30, nil },
	}
	if change != nil {
		change(&opts)
	}
	manager, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	f.Manager = manager
}

func (f *fixture) backup(t *testing.T, name, origin string) Record {
	t.Helper()
	rec, err := f.Backup(context.Background(), name, origin)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	return rec
}

func (f *fixture) files(t *testing.T, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func gunzip(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBackupWritesACompressedDumpWithItsChecksum(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.backup(t, "shop-db-1", OriginManual)
	if rec.State != StateOK || rec.Engine != Postgres || rec.Tool != "pg_dumpall" || rec.Origin != OriginManual || rec.Error != "" {
		t.Fatalf("record: %+v", rec)
	}
	if rec.ID != "20261122T090000Z" {
		t.Errorf("ID = %s", rec.ID)
	}
	path := filepath.Join(f.dir, "shop-db-1", rec.ID+".sql.gz")
	if got := gunzip(t, path); got != pgDump {
		t.Errorf("dump content: %q", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if rec.SHA256 != hex.EncodeToString(sum[:]) || rec.Size != int64(len(raw)) {
		t.Errorf("checksum or size: %+v, file of %d bytes", rec, len(raw))
	}
	for path, mode := range map[string]os.FileMode{
		f.dir: 0o700, filepath.Join(f.dir, "shop-db-1"): 0o700,
		path: 0o600, filepath.Join(f.dir, "shop-db-1", rec.ID+".json"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: mode %v, %v", path, info.Mode().Perm(), err)
		}
	}
	if names := strings.Join(f.files(t, "shop-db-1"), ","); names != rec.ID+".json,"+rec.ID+".sql.gz" {
		t.Errorf("files left: %s", names)
	}

	// The password went through the environment of the exec, in the
	// container the name designates, and nowhere else.
	calls := f.docker.executed()
	if len(calls) != 1 || calls[0].container != pgID {
		t.Fatalf("exec calls: %+v", calls)
	}
	if strings.Contains(strings.Join(calls[0].argv, " "), pgSecret) {
		t.Errorf("password in the arguments: %v", calls[0].argv)
	}
	if len(calls[0].env) != 1 || calls[0].env[0] != "PGPASSWORD="+pgSecret {
		t.Errorf("exec environment has %d entries", len(calls[0].env))
	}
	stored, _ := os.ReadFile(filepath.Join(f.dir, "shop-db-1", rec.ID+".json"))
	view, _ := json.Marshal(f.List(context.Background()))
	for _, text := range []string{string(stored), string(view)} {
		if strings.Contains(text, pgSecret) || strings.Contains(text, mariaToken) || strings.Contains(text, "POSTGRES_PASSWORD") {
			t.Errorf("a secret is shown: %s", text)
		}
	}
}

func TestListDescribesTheDatabases(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.backup(t, "shop-db-1", OriginManual)
	view := f.List(context.Background())
	if !view.Available || len(view.Databases) != 2 {
		t.Fatalf("view: %+v", view)
	}
	blog, shop := view.Databases[0], view.Databases[1]
	if blog.Container != "blog-db" || blog.Engine != MariaDB || !blog.Present || len(blog.Backups) != 0 || blog.Settings.Keep != DefaultKeep {
		t.Errorf("blog: %+v", blog)
	}
	if shop.Container != "shop-db-1" || shop.Engine != Postgres || len(shop.Backups) != 1 || shop.Backups[0].ID != rec.ID {
		t.Errorf("shop: %+v", shop)
	}
	// A container that is gone keeps its backups, to download or delete.
	f.docker.mu.Lock()
	f.docker.containers = f.docker.containers[1:]
	f.docker.mu.Unlock()
	view = f.List(context.Background())
	shop = view.Databases[1]
	if shop.Present || shop.Engine != Postgres || len(shop.Backups) != 1 {
		t.Errorf("stopped database: %+v", shop)
	}
	// Docker closed: the stored backups are still listed, with the reason.
	f.docker.mu.Lock()
	f.docker.problem = "vue Docker désactivée"
	f.docker.mu.Unlock()
	view = f.List(context.Background())
	if view.Available || view.Error != "vue Docker désactivée" || len(view.Databases) != 1 {
		t.Errorf("view with Docker closed: %+v", view)
	}
}

func TestAFailedOrEmptyDumpIsRemovedAndMarkedFailed(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(call execCall, spec ExecSpec, ctx context.Context) (int, error)
		want string
	}{
		{"empty output", func(execCall, ExecSpec, context.Context) (int, error) { return 0, nil }, "n’a rien produit"},
		{"error exit after some output", func(_ execCall, spec ExecSpec, _ context.Context) (int, error) {
			_, _ = io.WriteString(spec.Stdout, "-- partial dump\n")
			_, _ = io.WriteString(spec.Stderr, "pg_dumpall: error: connection failed for password "+pgSecret+"\n")
			return 1, nil
		}, "pg_dumpall a échoué (code 1) : pg_dumpall: error: connection failed for password ***"},
		{"docker error", func(execCall, ExecSpec, context.Context) (int, error) {
			return -1, errors.New("Docker injoignable")
		}, "exécution de pg_dumpall impossible : Docker injoignable"},
		{"too long", func(_ execCall, spec ExecSpec, ctx context.Context) (int, error) {
			_, _ = io.WriteString(spec.Stdout, "-- partial dump\n")
			<-ctx.Done()
			return -1, ctx.Err()
		}, "n’a pas terminé"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t, func(o *Options) { o.DumpTimeout = 50 * time.Millisecond })
			f.docker.setRun(test.run)
			rec := f.backup(t, "shop-db-1", OriginManual)
			if rec.State != StateFailed || !strings.Contains(rec.Error, test.want) || rec.Size != 0 || rec.SHA256 != "" {
				t.Fatalf("record: %+v", rec)
			}
			if strings.Contains(rec.Error, pgSecret) {
				t.Errorf("password in the error: %s", rec.Error)
			}
			if names := strings.Join(f.files(t, "shop-db-1"), ","); names != rec.ID+".json" {
				t.Errorf("files left: %s", names)
			}
			listed := f.List(context.Background()).Databases[1].Backups
			if len(listed) != 1 || listed[0].State != StateFailed {
				t.Errorf("listing: %+v", listed)
			}
			if _, _, err := f.Open("shop-db-1", rec.ID); !errors.Is(err, ErrNotFound) {
				t.Errorf("a failed backup can be downloaded: %v", err)
			}
		})
	}
}

func TestBackupRefusals(t *testing.T) {
	t.Run("disk nearly full", func(t *testing.T) {
		f := newFixture(t, func(o *Options) {
			o.MinFreeBytes = 1 << 30
			o.FreeBytes = func(string) (uint64, error) { return 512 << 20, nil }
		})
		rec := f.backup(t, "shop-db-1", OriginManual)
		if rec.State != StateFailed || !strings.Contains(rec.Error, "espace disque insuffisant") || len(f.docker.executed()) != 0 {
			t.Fatalf("record: %+v, calls %v", rec, f.docker.tools())
		}
	})
	t.Run("not a database", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.StartBackup(context.Background(), "web"); !errors.Is(err, ErrNoDatabase) {
			t.Errorf("web: %v", err)
		}
		if err := f.StartBackup(context.Background(), "../etc"); err == nil {
			t.Error("a path was accepted as a container")
		}
		if _, err := os.Stat(filepath.Join(f.dir, "web")); !os.IsNotExist(err) {
			t.Error("a refused backup created a directory")
		}
	})
	t.Run("already running", func(t *testing.T) {
		f := newFixture(t, nil)
		release := make(chan struct{})
		f.docker.setRun(func(_ execCall, spec ExecSpec, _ context.Context) (int, error) {
			_, _ = io.WriteString(spec.Stdout, pgDump)
			<-release
			return 0, nil
		})
		if err := f.StartBackup(context.Background(), "shop-db-1"); err != nil {
			t.Fatal(err)
		}
		if err := f.StartBackup(context.Background(), "shop-db-1"); !errors.Is(err, ErrBusy) {
			t.Errorf("second backup: %v", err)
		}
		if running := f.List(context.Background()).Databases[1].Running; running == nil || running.Kind != "backup" || running.Origin != OriginManual {
			t.Errorf("running: %+v", running)
		}
		close(release)
		f.Wait()
		if got := f.docker.tools(); got != "pg_dumpall" {
			t.Errorf("programs run: %s", got)
		}
	})
}

func TestDumpFallsBackWhenTheToolIsNotInTheImage(t *testing.T) {
	f := newFixture(t, nil)
	f.docker.setRun(func(call execCall, spec ExecSpec, _ context.Context) (int, error) {
		if call.argv[0] == "mariadb-dump" {
			_, _ = io.WriteString(spec.Stderr, `OCI runtime exec failed: exec: "mariadb-dump": executable file not found in $PATH`)
			return 127, nil
		}
		_, _ = io.WriteString(spec.Stdout, "-- MariaDB dump\n")
		return 0, nil
	})
	rec := f.backup(t, "blog-db", OriginManual)
	if rec.State != StateOK || rec.Tool != "mysqldump" || f.docker.tools() != "mariadb-dump,mysqldump" {
		t.Fatalf("record: %+v, programs %s", rec, f.docker.tools())
	}
	if got := gunzip(t, filepath.Join(f.dir, "blog-db", rec.ID+".sql.gz")); got != "-- MariaDB dump\n" {
		t.Errorf("dump: %q", got)
	}
	// A dump that fails for another reason is not tried with another tool.
	f.docker.setRun(func(_ execCall, spec ExecSpec, _ context.Context) (int, error) {
		_, _ = io.WriteString(spec.Stderr, "Access denied")
		return 2, nil
	})
	if rec := f.backup(t, "blog-db", OriginManual); rec.State != StateFailed || f.docker.tools() != "mariadb-dump,mysqldump,mariadb-dump" {
		t.Fatalf("record: %+v, programs %s", rec, f.docker.tools())
	}
}

func okIDs(records []Record) (ok, failed []string) {
	for _, rec := range records {
		if rec.State == StateOK {
			ok = append(ok, rec.ID)
		} else {
			failed = append(failed, rec.ID)
		}
	}
	return ok, failed
}

func TestRetentionOnlyFollowsACompleteBackup(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeOff, Time: "03:00", Keep: 2}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, f.backup(t, "shop-db-1", OriginManual).ID)
	}
	records, _ := f.store.list("shop-db-1")
	if ok, _ := okIDs(records); strings.Join(ok, ",") != ids[2]+","+ids[1] {
		t.Fatalf("kept after three backups: %v", ok)
	}
	// Failures delete nothing, however many.
	f.docker.setRun(func(execCall, ExecSpec, context.Context) (int, error) { return 1, nil })
	for i := 0; i < keptFailures+3; i++ {
		f.backup(t, "shop-db-1", OriginScheduled)
	}
	records, _ = f.store.list("shop-db-1")
	ok, failed := okIDs(records)
	if strings.Join(ok, ",") != ids[2]+","+ids[1] || len(failed) != keptFailures+3 {
		t.Fatalf("after failures: ok %v, %d failed", ok, len(failed))
	}
	// A safety backup taken before a restore deletes nothing either.
	f.docker.setRun(nil)
	safety := f.backup(t, "shop-db-1", OriginPreRestore)
	records, _ = f.store.list("shop-db-1")
	if ok, _ := okIDs(records); len(ok) != 3 {
		t.Fatalf("after a safety backup: %v", ok)
	}
	// The next complete backup applies it, and trims the failed records.
	last := f.backup(t, "shop-db-1", OriginScheduled)
	records, _ = f.store.list("shop-db-1")
	ok, failed = okIDs(records)
	if strings.Join(ok, ",") != last.ID+","+safety.ID || len(failed) != keptFailures {
		t.Fatalf("after the next backup: ok %v, %d failed", ok, len(failed))
	}
	if names := f.files(t, "shop-db-1"); len(names) != 2*2+keptFailures {
		t.Errorf("files left: %v", names)
	}
}

func scheduledCount(f *fixture, name string) int {
	records, _ := f.store.list(name)
	count := 0
	for _, rec := range records {
		if rec.Origin == OriginScheduled {
			count++
		}
	}
	return count
}

func TestSchedulerRunsEachOccurrenceOnce(t *testing.T) {
	f := newFixture(t, nil) // Sunday 22, 10:00
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeDaily, Time: "03:15", Keep: 7}); err != nil {
		t.Fatal(err)
	}
	tick := func() {
		f.Tick()
		f.Wait()
	}
	// The time of today is before the schedule was set: nothing is owed.
	tick()
	f.clock.Set(day(23, 3, 14))
	tick()
	if n := scheduledCount(f, "shop-db-1"); n != 0 {
		t.Fatalf("%d backups before the first scheduled time", n)
	}
	if next := f.List(context.Background()).Databases[1].NextRun; next != day(23, 3, 15).Unix() {
		t.Errorf("next run = %d", next)
	}
	f.clock.Set(day(23, 3, 15))
	tick()
	f.clock.Advance(30 * time.Second)
	tick()
	f.clock.Set(day(23, 23, 0))
	tick()
	if n := scheduledCount(f, "shop-db-1"); n != 1 {
		t.Fatalf("%d backups for one scheduled time", n)
	}
	// The database that has no schedule is never touched.
	if n := scheduledCount(f, "blog-db"); n != 0 {
		t.Fatalf("%d backups of a database without a schedule", n)
	}

	// The hub is away for three scheduled times: it catches up one.
	f.clock.Set(day(26, 12, 0))
	f.open(t, nil)
	tick()
	tick()
	if n := scheduledCount(f, "shop-db-1"); n != 2 {
		t.Fatalf("%d backups after a restart, want 2", n)
	}
	// A restart after the day's run does not run it again.
	f.clock.Advance(time.Hour)
	f.open(t, nil)
	tick()
	if n := scheduledCount(f, "shop-db-1"); n != 2 {
		t.Fatalf("%d backups after a second restart, want 2", n)
	}

	// Weekly: Wednesday only.
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeWeekly, Time: "03:15", Weekday: 3, Keep: 7}); err != nil {
		t.Fatal(err)
	}
	for d := 27; d <= 34; d++ {
		f.clock.Set(day(d, 4, 0))
		tick()
	}
	if n := scheduledCount(f, "shop-db-1"); n != 3 {
		t.Fatalf("%d backups after one more week, want 3", n)
	}
	// Turned off: nothing more.
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeOff, Time: "03:15", Keep: 7}); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(day(45, 4, 0))
	tick()
	if n := scheduledCount(f, "shop-db-1"); n != 3 {
		t.Fatalf("%d backups with the schedule off", n)
	}
}

func TestSchedulerNeverRunsTwoBackupsOfOneDatabase(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeDaily, Time: "03:15", Keep: 7}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 8), make(chan struct{})
	f.docker.setRun(func(_ execCall, spec ExecSpec, _ context.Context) (int, error) {
		_, _ = io.WriteString(spec.Stdout, pgDump)
		started <- struct{}{}
		<-release
		return 0, nil
	})
	f.clock.Set(day(23, 3, 15))
	f.Tick()
	<-started
	// Still running a day later: the ticks start nothing, a manual backup
	// is refused.
	for i := 0; i < 3; i++ {
		f.Tick()
	}
	f.clock.Set(day(24, 3, 20))
	f.Tick()
	if err := f.StartBackup(context.Background(), "shop-db-1"); !errors.Is(err, ErrBusy) {
		t.Errorf("manual backup during a scheduled one: %v", err)
	}
	select {
	case <-started:
		t.Fatal("a second backup of the same database started")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	f.Wait()
	if n := scheduledCount(f, "shop-db-1"); n != 1 {
		t.Fatalf("%d backups", n)
	}
	// The run that was owed meanwhile comes next, once.
	f.Tick()
	f.Wait()
	f.Tick()
	f.Wait()
	if n := scheduledCount(f, "shop-db-1"); n != 2 {
		t.Fatalf("%d backups after the release, want 2", n)
	}
}

func TestAFailedScheduledBackupIsReportedUntilOneSucceeds(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.SetSettings("shop-db-1", Settings{Mode: ModeDaily, Time: "03:15", Keep: 7}); err != nil {
		t.Fatal(err)
	}
	f.docker.setRun(func(_ execCall, spec ExecSpec, _ context.Context) (int, error) {
		_, _ = io.WriteString(spec.Stderr, "connection refused, password "+pgSecret)
		return 1, nil
	})
	// A manual failure is seen by the admin who asked: it raises no alert.
	f.backup(t, "shop-db-1", OriginManual)
	if failures := f.Failures(); len(failures) != 0 {
		t.Fatalf("a manual failure raised an alert: %+v", failures)
	}
	f.clock.Set(day(23, 3, 15))
	f.Tick()
	f.Wait()
	failures := f.Failures()
	if failure, ok := failures["shop-db-1"]; !ok || !strings.Contains(failure.Detail, "connection refused") || strings.Contains(failure.Detail, pgSecret) {
		t.Fatalf("failures: %+v", failures)
	}
	if db := f.List(context.Background()).Databases[1]; db.Failure == nil {
		t.Error("the listing does not show the failure")
	}
	// It survives a restart.
	f.open(t, nil)
	if len(f.Failures()) != 1 {
		t.Fatal("the failure was forgotten by a restart")
	}
	// A complete backup, even a manual one, ends it.
	f.docker.setRun(nil)
	f.clock.Advance(time.Hour)
	f.backup(t, "shop-db-1", OriginManual)
	if failures := f.Failures(); len(failures) != 0 {
		t.Fatalf("failures after a success: %+v", failures)
	}
	if db := f.List(context.Background()).Databases[1]; db.Failure != nil {
		t.Error("the listing still shows the failure")
	}
}

func restoreFixture(t *testing.T, enabled bool) (*fixture, Record) {
	t.Helper()
	f := newFixture(t, func(o *Options) { o.RestoreEnabled = enabled })
	rec := f.backup(t, "shop-db-1", OriginManual)
	return f, rec
}

func (f *fixture) restoreAndWait(t *testing.T, name, id string) RestoreStatus {
	t.Helper()
	if err := f.StartRestore(name, id, name, "admin"); err != nil {
		t.Fatal(err)
	}
	f.Wait()
	status := f.List(context.Background()).Restore
	if status == nil {
		t.Fatal("no restore status")
	}
	return *status
}

func TestRestoreIsRefusedUpFront(t *testing.T) {
	t.Run("switch off", func(t *testing.T) {
		f, rec := restoreFixture(t, false)
		if err := f.StartRestore("shop-db-1", rec.ID, "shop-db-1", "admin"); !errors.Is(err, ErrRestoreDisabled) {
			t.Fatalf("restore with the switch off: %v", err)
		}
		if f.List(context.Background()).RestoreEnabled {
			t.Error("the listing says restores are allowed")
		}
		if got := f.docker.tools(); got != "pg_dumpall" {
			t.Errorf("programs run: %s", got)
		}
	})
	f, rec := restoreFixture(t, true)
	for _, test := range []struct {
		name            string
		container, id   string
		confirm         string
		want            error
		status, message string
	}{
		{"empty confirmation", "shop-db-1", rec.ID, "", ErrConfirm, "", ""},
		{"another name", "shop-db-1", rec.ID, "blog-db", ErrConfirm, "", ""},
		{"wrong case", "shop-db-1", rec.ID, "Shop-db-1", ErrConfirm, "", ""},
		{"surrounding space", "shop-db-1", rec.ID, " shop-db-1 ", ErrConfirm, "", ""},
		{"unknown backup", "shop-db-1", "20200101T000000Z", "shop-db-1", ErrNotFound, "", ""},
		{"backup of another container", "blog-db", rec.ID, "blog-db", ErrNotFound, "", ""},
		{"path as a container", "../shop-db-1", rec.ID, "../shop-db-1", nil, "", "invalide"},
		{"path as a backup", "shop-db-1", "../" + rec.ID, "shop-db-1", nil, "", "invalide"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := f.StartRestore(test.container, test.id, test.confirm, "admin")
			if err == nil || (test.want != nil && !errors.Is(err, test.want)) || !strings.Contains(err.Error(), test.message) {
				t.Errorf("error: %v", err)
			}
		})
	}
	f.Wait()
	if got := f.docker.tools(); got != "pg_dumpall" {
		t.Errorf("programs run by refused restores: %s", got)
	}
	if f.List(context.Background()).Restore != nil {
		t.Error("a refused restore left a status")
	}
}

func TestRestoreChecksTheFileBeforeAnything(t *testing.T) {
	f, rec := restoreFixture(t, true)
	path := filepath.Join(f.dir, "shop-db-1", rec.ID+".sql.gz")
	// A valid gzip of another content: only the checksum can tell.
	var other bytes.Buffer
	writer := gzip.NewWriter(&other)
	_, _ = io.WriteString(writer, "DROP DATABASE shop;\n")
	_ = writer.Close()
	if err := os.WriteFile(path, other.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	status := f.restoreAndWait(t, "shop-db-1", rec.ID)
	if status.State != "failed" || !strings.Contains(status.Error, "SHA-256") || status.SafetyBackup != "" {
		t.Fatalf("status: %+v", status)
	}
	if got := f.docker.tools(); got != "pg_dumpall" {
		t.Errorf("programs run: %s", got)
	}
}

func TestRestoreStopsWhenTheSafetyBackupFails(t *testing.T) {
	f, rec := restoreFixture(t, true)
	f.docker.setRun(func(call execCall, spec ExecSpec, _ context.Context) (int, error) {
		if call.argv[0] == "pg_dumpall" {
			_, _ = io.WriteString(spec.Stderr, "could not connect")
			return 1, nil
		}
		return 0, nil
	})
	status := f.restoreAndWait(t, "shop-db-1", rec.ID)
	if status.State != "failed" || !strings.Contains(status.Error, "sauvegarde de sécurité en échec") || !strings.Contains(status.Error, "could not connect") {
		t.Fatalf("status: %+v", status)
	}
	if got := f.docker.tools(); got != "pg_dumpall,pg_dumpall" {
		t.Errorf("programs run: %s", got)
	}
	// A failed safety backup is not a scheduled one: no alert.
	if len(f.Failures()) != 0 {
		t.Error("the safety backup raised an alert")
	}
}

func TestRestoreFeedsTheDumpToTheClient(t *testing.T) {
	f, rec := restoreFixture(t, true)
	status := f.restoreAndWait(t, "shop-db-1", rec.ID)
	if status.State != "ok" || status.Step != "done" || status.Error != "" || status.ExitCode == nil || *status.ExitCode != 0 || status.FinishedAt == 0 || status.Actor != "admin" {
		t.Fatalf("status: %+v", status)
	}
	calls := f.docker.executed()
	if len(calls) != 3 || calls[1].argv[0] != "pg_dumpall" || calls[2].argv[0] != "psql" {
		t.Fatalf("programs run: %s", f.docker.tools())
	}
	psql := calls[2]
	if psql.container != pgID || psql.stdin != pgDump {
		t.Errorf("psql got %q in %s", psql.stdin, psql.container)
	}
	if strings.Contains(strings.Join(psql.argv, " "), pgSecret) || len(psql.env) != 1 || psql.env[0] != "PGPASSWORD="+pgSecret {
		t.Errorf("psql arguments %v, %d variables", psql.argv, len(psql.env))
	}
	// The safety backup is a complete backup of its own.
	safety, _, err := f.store.get("shop-db-1", status.SafetyBackup)
	if err != nil || safety.State != StateOK || safety.Origin != OriginPreRestore || safety.ID == rec.ID {
		t.Fatalf("safety backup: %+v, %v", safety, err)
	}
	if running := f.List(context.Background()).Databases[1].Running; running != nil {
		t.Errorf("still running: %+v", running)
	}
}

func TestRestoreReportsTheClientFailureWithBoundedOutput(t *testing.T) {
	f, rec := restoreFixture(t, true)
	f.docker.setRun(func(call execCall, spec ExecSpec, _ context.Context) (int, error) {
		if call.argv[0] == "pg_dumpall" {
			_, _ = io.WriteString(spec.Stdout, pgDump)
			return 0, nil
		}
		_, _ = io.Copy(io.Discard, spec.Stdin)
		_, _ = io.WriteString(spec.Stderr, "ERROR: role \"shop\" already exists, password "+pgSecret+"\n\x1b[31m")
		_, _ = io.WriteString(spec.Stderr, strings.Repeat("ERROR: relation already exists\n", 2000))
		return 3, nil
	})
	status := f.restoreAndWait(t, "shop-db-1", rec.ID)
	if status.State != "failed" || status.ExitCode == nil || *status.ExitCode != 3 || !strings.Contains(status.Error, "psql a échoué (code 3)") {
		t.Fatalf("status: %+v", status)
	}
	if len(status.Output) > outputLimit || !status.OutputTruncated || !strings.Contains(status.Output, "already exists") {
		t.Errorf("output of %d bytes, truncated %v", len(status.Output), status.OutputTruncated)
	}
	if strings.Contains(status.Output, pgSecret) || strings.Contains(status.Output, "\x1b") {
		t.Error("the output shows the password or a control character")
	}
	if status.SafetyBackup == "" {
		t.Error("no safety backup named")
	}
}

func TestOneRestoreAtATime(t *testing.T) {
	f, rec := restoreFixture(t, true)
	other := f.backup(t, "blog-db", OriginManual)
	started, release := make(chan struct{}, 4), make(chan struct{})
	f.docker.setRun(func(call execCall, spec ExecSpec, _ context.Context) (int, error) {
		if spec.Stdin == nil {
			_, _ = io.WriteString(spec.Stdout, pgDump)
			return 0, nil
		}
		started <- struct{}{}
		<-release
		_, _ = io.Copy(io.Discard, spec.Stdin)
		return 0, nil
	})
	if err := f.StartRestore("shop-db-1", rec.ID, "shop-db-1", "admin"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.StartRestore("blog-db", other.ID, "blog-db", "admin"); !errors.Is(err, ErrRestoreBusy) {
		t.Errorf("second restore: %v", err)
	}
	if err := f.StartBackup(context.Background(), "shop-db-1"); !errors.Is(err, ErrBusy) {
		t.Errorf("backup during the restore: %v", err)
	}
	if err := f.Delete("shop-db-1", rec.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("deletion during the restore: %v", err)
	}
	view := f.List(context.Background())
	if view.Restore == nil || view.Restore.State != "running" || view.Restore.Step != "restore" || view.Databases[1].Running == nil || view.Databases[1].Running.Kind != "restore" {
		t.Errorf("view during the restore: %+v", view.Restore)
	}
	close(release)
	f.Wait()
	if err := f.StartRestore("blog-db", other.ID, "blog-db", "admin"); err != nil {
		t.Errorf("restore after the first one ended: %v", err)
	}
	f.Wait()
}

func TestDeleteRemovesOneBackup(t *testing.T) {
	f := newFixture(t, nil)
	first := f.backup(t, "shop-db-1", OriginManual)
	second := f.backup(t, "shop-db-1", OriginManual)
	if err := f.Delete("shop-db-1", first.ID); err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(f.files(t, "shop-db-1"), ","); names != second.ID+".json,"+second.ID+".sql.gz" {
		t.Errorf("files left: %s", names)
	}
	if err := f.Delete("shop-db-1", first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second deletion: %v", err)
	}
	for _, bad := range [][2]string{{"..", second.ID}, {"shop-db-1", "../" + second.ID}, {"shop-db-1", second.ID + ".sql.gz"}, {"", ""}} {
		if err := f.Delete(bad[0], bad[1]); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Delete(%q, %q): %v", bad[0], bad[1], err)
		}
	}
}

// HTTP.

var (
	httpUser  = &auth.AccountIdentity{User: accounts.User{ID: "u1", Username: "alice", Role: "user"}}
	httpAdmin = &auth.AccountIdentity{User: accounts.User{ID: "a1", Username: "root", Role: "admin"}}
)

func serve(h *Handler, identity *auth.AccountIdentity, method, target, body string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	h.Register(router, router)
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if identity != nil {
		request = request.WithContext(auth.WithAccount(request.Context(), *identity))
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func routeRequest(route, name, id string) (method, path string) {
	method, path, _ = strings.Cut(route, " ")
	return method, strings.NewReplacer("{container}", name, "{id}", id).Replace(path)
}

func TestRoutesAreAdminOnly(t *testing.T) {
	f, rec := restoreFixture(t, true)
	h := NewHandler(f.Manager, "", "")
	for _, route := range Routes {
		method, path := routeRequest(route, "shop-db-1", rec.ID)
		if response := serve(h, httpUser, method, path, `{"confirm":"shop-db-1"}`); response.Code != http.StatusForbidden {
			t.Errorf("user account, %s: %d %s", route, response.Code, response.Body)
		}
	}
	f.Wait()
	if got := f.docker.tools(); got != "pg_dumpall" {
		t.Errorf("programs run for a user account: %s", got)
	}
	// Every route is declared: the server classifies what Routes lists.
	router := chi.NewRouter()
	h.Register(router, router)
	declared := map[string]bool{}
	for _, route := range Routes {
		declared[route] = true
	}
	mounted := 0
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted++
		if !declared[method+" "+route] {
			t.Errorf("route not declared in Routes: %s %s", method, route)
		}
		return nil
	})
	if mounted != len(Routes) {
		t.Errorf("%d routes mounted, %d declared", mounted, len(Routes))
	}
}

func TestRoutesFollowTheDockerSwitches(t *testing.T) {
	f, rec := restoreFixture(t, true)
	f.docker.mu.Lock()
	f.docker.problem = "vue Docker désactivée sur ce serveur (TMUXATLAS_DOCKER=off)"
	f.docker.mu.Unlock()
	h := NewHandler(f.Manager, "", "")
	for _, route := range Routes[1:] {
		method, path := routeRequest(route, "shop-db-1", rec.ID)
		response := serve(h, httpAdmin, method, path, `{"confirm":"shop-db-1"}`)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "TMUXATLAS_DOCKER=off") {
			t.Errorf("%s with Docker off: %d %s", route, response.Code, response.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "shop-db-1", rec.ID+".sql.gz")); err != nil {
		t.Errorf("the backup was touched: %v", err)
	}
	// A handler without a manager refuses everything.
	closed := NewHandler(nil, "", "")
	for _, route := range Routes {
		method, path := routeRequest(route, "shop-db-1", rec.ID)
		if response := serve(closed, httpAdmin, method, path, `{}`); response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without a manager: %d", route, response.Code)
		}
	}
}

func TestHTTPFlow(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.RestoreEnabled = true })
	h := NewHandler(f.Manager, "", "")
	admin := func(method, path, body string, want int) string {
		t.Helper()
		response := serve(h, httpAdmin, method, path, body)
		if response.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body)
		}
		return response.Body.String()
	}
	admin(http.MethodPost, "/db-backups/shop-db-1/backup", "", http.StatusAccepted)
	f.Wait()
	admin(http.MethodPost, "/db-backups/web/backup", "", http.StatusNotFound)

	var view View
	listing := admin(http.MethodGet, "/db-backups", "", http.StatusOK)
	if err := json.Unmarshal([]byte(listing), &view); err != nil || len(view.Databases) != 2 || len(view.Databases[1].Backups) != 1 {
		t.Fatalf("listing: %s", listing)
	}
	if strings.Contains(listing, pgSecret) || strings.Contains(listing, mariaToken) {
		t.Fatal("the listing shows a password")
	}
	id := view.Databases[1].Backups[0].ID

	// Settings.
	admin(http.MethodPut, "/db-backups/shop-db-1/settings", `{"mode":"weekly","time":"04:30","weekday":2,"keep":3}`, http.StatusOK)
	admin(http.MethodPut, "/db-backups/shop-db-1/settings", `{"mode":"weekly","time":"25:00","weekday":2,"keep":3}`, http.StatusBadRequest)
	admin(http.MethodPut, "/db-backups/shop-db-1/settings", `{"mode":"daily","time":"04:30","weekday":0,"keep":0}`, http.StatusBadRequest)
	admin(http.MethodPut, "/db-backups/shop-db-1/settings", `{"mode":"daily","time":"04:30","weekday":0,"keep":3,"path":"/etc"}`, http.StatusBadRequest)
	admin(http.MethodPut, "/db-backups/web/settings", `{"mode":"daily","time":"04:30","weekday":0,"keep":3}`, http.StatusNotFound)
	_ = json.Unmarshal([]byte(admin(http.MethodGet, "/db-backups", "", http.StatusOK)), &view)
	if got := view.Databases[1].Settings; got != (Settings{Mode: ModeWeekly, Time: "04:30", Weekday: 2, Keep: 3}) || view.Databases[1].NextRun == 0 {
		t.Errorf("settings: %+v", view.Databases[1])
	}

	// Download: the file, never cached, and only by its identifier.
	response := serve(h, httpAdmin, http.MethodGet, "/db-backups/shop-db-1/files/"+id+"/download", "")
	raw, _ := os.ReadFile(filepath.Join(f.dir, "shop-db-1", id+".sql.gz"))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), raw) {
		t.Fatalf("download: %d, %d bytes", response.Code, response.Body.Len())
	}
	header := response.Header()
	if header.Get("Cache-Control") != "no-store" || header.Get("X-Content-Type-Options") != "nosniff" ||
		header.Get("Content-Disposition") != "attachment; filename=shop-db-1-"+id+".sql.gz" {
		t.Errorf("download headers: %v", header)
	}
	for _, path := range []string{
		"/db-backups/shop-db-1/files/20200101T000000Z/download",
		"/db-backups/blog-db/files/" + id + "/download",
	} {
		admin(http.MethodGet, path, "", http.StatusNotFound)
	}
	for _, path := range []string{
		"/db-backups/shop-db-1/files/config.json/download",
		"/db-backups/shop-db-1/files/" + id + ".json/download",
		"/db-backups/shop-db-1/files/..%2F..%2Fconfig/download",
		"/db-backups/..%2Fshop-db-1/files/" + id + "/download",
		"/db-backups/%2E%2E/files/" + id + "/download",
	} {
		if response := serve(h, httpAdmin, http.MethodGet, path, ""); response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, response.Code, response.Body)
		} else if strings.Contains(response.Body.String(), "databases") {
			t.Errorf("%s returned a file", path)
		}
	}

	// Restore: the name typed again is checked by the server.
	admin(http.MethodPost, "/db-backups/shop-db-1/files/"+id+"/restore", `{"confirm":"shop-db"}`, http.StatusBadRequest)
	admin(http.MethodPost, "/db-backups/shop-db-1/files/"+id+"/restore", `{}`, http.StatusBadRequest)
	admin(http.MethodPost, "/db-backups/shop-db-1/files/"+id+"/restore", `{"confirm":"shop-db-1"}`, http.StatusAccepted)
	f.Wait()
	_ = json.Unmarshal([]byte(admin(http.MethodGet, "/db-backups", "", http.StatusOK)), &view)
	if view.Restore == nil || view.Restore.State != "ok" || view.Restore.Actor != "root" || len(view.Databases[1].Backups) != 2 {
		t.Fatalf("after the restore: %+v", view.Restore)
	}

	// Delete.
	admin(http.MethodDelete, "/db-backups/shop-db-1/files/"+id, "", http.StatusOK)
	admin(http.MethodDelete, "/db-backups/shop-db-1/files/"+id, "", http.StatusNotFound)
	admin(http.MethodGet, "/db-backups/shop-db-1/files/"+id+"/download", "", http.StatusNotFound)

	// With the switch off, the restore route refuses.
	f.open(t, nil)
	h = NewHandler(f.Manager, "", "")
	_ = json.Unmarshal([]byte(admin(http.MethodGet, "/db-backups", "", http.StatusOK)), &view)
	remaining := view.Databases[1].Backups[0].ID
	body := admin(http.MethodPost, "/db-backups/shop-db-1/files/"+remaining+"/restore", `{"confirm":"shop-db-1"}`, http.StatusForbidden)
	if !strings.Contains(body, "TMUXATLAS_DB_RESTORE") {
		t.Errorf("refusal: %s", body)
	}
}
