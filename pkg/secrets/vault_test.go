package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const (
	testPassphrase = "correct horse battery staple"
	testValue      = "v4lue-that-must-never-leak"
)

// fakeClock is the injectable clock of the tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeRestarter records restarts; it never reaches Docker.
type fakeRestarter struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeRestarter) RestartContainer(actor, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, actor+":"+name)
	return f.err
}

type testVault struct {
	*Vault
	home      string
	dir       string
	clock     *fakeClock
	restarter *fakeRestarter
}

func newTestVault(t *testing.T) *testVault {
	t.Helper()
	// The home is resolved: on some systems the temporary directory is
	// itself behind a symbolic link.
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".local", "share", "tmuxatlas", "secrets")
	clock := &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	restarter := &fakeRestarter{}
	vault, err := New(Options{
		Dir: dir, Home: home, Forbidden: []string{filepath.Join(home, ".config", "tmuxatlas")},
		Restarter: restarter, Now: clock.Now, ScryptWorkFactor: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testVault{Vault: vault, home: home, dir: dir, clock: clock, restarter: restarter}
}

func (v *testVault) ready(t *testing.T, apps ...string) {
	t.Helper()
	if err := v.Init("root", testPassphrase); err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if _, err := v.CreateApp("root", app); err != nil {
			t.Fatal(err)
		}
	}
}

func (v *testVault) unlock(t *testing.T) {
	t.Helper()
	if _, err := v.Unlock("root", testPassphrase); err != nil {
		t.Fatal(err)
	}
}

// vaultBytes returns everything stored under the vault directory.
func (v *testVault) vaultBytes(t *testing.T) []byte {
	t.Helper()
	var all bytes.Buffer
	err := filepath.Walk(v.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		all.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return all.Bytes()
}

func TestRoundTripAndStorage(t *testing.T) {
	v := newTestVault(t)
	if status, err := v.Status(); err != nil || status.Initialized || status.Unlocked {
		t.Fatalf("fresh vault: %+v %v", status, err)
	}
	if _, err := v.Apps(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("apps before init: %v", err)
	}
	if _, err := v.Unlock("root", testPassphrase); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("unlock before init: %v", err)
	}
	v.ready(t, "shop")
	if err := v.Init("root", testPassphrase); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second init: %v", err)
	}

	// A value is stored while the vault is locked.
	app, err := v.SetVar("root", "shop", "API_TOKEN", testValue)
	if err != nil || len(app.Variables) != 1 || app.Variables[0].Name != "API_TOKEN" {
		t.Fatalf("set while locked: %+v %v", app, err)
	}
	if _, err := v.Reveal("root", "shop", "API_TOKEN"); !errors.Is(err, ErrLocked) {
		t.Fatalf("reveal while locked: %v", err)
	}
	if _, err := v.Export("root", "shop"); !errors.Is(err, ErrLocked) {
		t.Fatalf("export while locked: %v", err)
	}
	if _, err := v.Unlock("root", "not the right passphrase"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	v.unlock(t)
	for _, value := range []string{testValue, "", "multi\nline \"quoted\" $HOME 'x'", strings.Repeat("é", 1000)} {
		if _, err := v.SetVar("root", "shop", "API_TOKEN", value); err != nil {
			t.Fatal(err)
		}
		if got, err := v.Reveal("root", "shop", "API_TOKEN"); err != nil || got != value {
			t.Fatalf("round trip of %q: %q %v", value, got, err)
		}
	}
	if _, err := v.Reveal("root", "shop", "MISSING"); !errors.Is(err, ErrVarNotFound) {
		t.Fatalf("missing variable: %v", err)
	}
	if _, err := v.Reveal("root", "other", "API_TOKEN"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("missing app: %v", err)
	}

	// Nothing on disk is readable by another user, nothing is in clear.
	if _, err := v.SetVar("root", "shop", "API_TOKEN", testValue); err != nil {
		t.Fatal(err)
	}
	err = filepath.Walk(v.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", path, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := v.vaultBytes(t)
	for _, clear := range []string{testValue, testPassphrase, "AGE-SECRET-KEY-"} {
		if bytes.Contains(stored, []byte(clear)) {
			t.Fatalf("%q is stored in clear", clear)
		}
	}
	if !bytes.Contains(stored, []byte("API_TOKEN")) {
		t.Fatal("variable names are expected to stay readable")
	}
}

// The documented way out of the vault: plain age, with nothing of this
// package, decrypts the identity with the passphrase, then each value.
func TestValuesAreStandardAgeFiles(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	if _, err := v.SetVar("root", "shop", "API_TOKEN", testValue); err != nil {
		t.Fatal(err)
	}
	var vault struct {
		PublicKey string `json:"public_key"`
		Identity  string `json:"identity"`
	}
	raw, err := os.ReadFile(filepath.Join(v.dir, "vault.json"))
	if err != nil || json.Unmarshal(raw, &vault) != nil {
		t.Fatal(err)
	}
	passphrase, err := age.NewScryptIdentity(testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := age.Decrypt(armor.NewReader(strings.NewReader(vault.Identity)), passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var key bytes.Buffer
	if _, err := key.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	identities, err := age.ParseIdentities(&key)
	if err != nil || len(identities) != 1 {
		t.Fatalf("identity file: %v", err)
	}
	var app struct {
		Vars []struct{ Name, Value string }
	}
	raw, err = os.ReadFile(filepath.Join(v.dir, "apps", "shop.json"))
	if err != nil || json.Unmarshal(raw, &app) != nil || len(app.Vars) != 1 {
		t.Fatal(err)
	}
	reader, err = age.Decrypt(armor.NewReader(strings.NewReader(app.Vars[0].Value)), identities...)
	if err != nil {
		t.Fatal(err)
	}
	var value bytes.Buffer
	if _, err := value.ReadFrom(reader); err != nil || value.String() != testValue {
		t.Fatalf("value decrypted with age: %q %v", value.String(), err)
	}
}

func TestUnlockExpiresAndIsNotExtended(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	if _, err := v.SetVar("root", "shop", "A", testValue); err != nil {
		t.Fatal(err)
	}
	status, err := v.Unlock("root", testPassphrase)
	if err != nil || !status.Unlocked || status.ExpiresAt == nil || !status.ExpiresAt.Equal(v.clock.Now().Add(DefaultUnlockTTL)) {
		t.Fatalf("unlock: %+v %v", status, err)
	}
	v.clock.Advance(DefaultUnlockTTL - time.Second)
	// Using the vault does not push the expiry back.
	if _, err := v.Reveal("root", "shop", "A"); err != nil {
		t.Fatal(err)
	}
	if again, _ := v.Status(); !again.Unlocked || !again.ExpiresAt.Equal(*status.ExpiresAt) {
		t.Fatalf("expiry moved: %+v", again)
	}
	v.clock.Advance(time.Second)
	if status, _ := v.Status(); status.Unlocked || status.ExpiresAt != nil {
		t.Fatalf("still unlocked after the TTL: %+v", status)
	}
	if _, err := v.Reveal("root", "shop", "A"); !errors.Is(err, ErrLocked) {
		t.Fatalf("reveal after expiry: %v", err)
	}
	if v.identity != nil {
		t.Fatal("the private key is still in memory after expiry")
	}

	v.unlock(t)
	v.Lock("root")
	if v.identity != nil {
		t.Fatal("the private key is still in memory after Lock")
	}
	if _, err := v.Reveal("root", "shop", "A"); !errors.Is(err, ErrLocked) {
		t.Fatalf("reveal after lock: %v", err)
	}
}

func TestUnlockAttemptsAreLimited(t *testing.T) {
	v := newTestVault(t)
	v.ready(t)
	for i := 0; i < freeAttempts; i++ {
		if _, err := v.Unlock("root", "wrong passphrase "+fmt.Sprint(i)); !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Even the right passphrase waits now.
	var throttled *ThrottledError
	if _, err := v.Unlock("root", testPassphrase); !errors.As(err, &throttled) || throttled.RetryAfter != baseBlock {
		t.Fatalf("blocked attempt: %v", err)
	}
	if status, _ := v.Status(); status.RetryAfterSeconds != int(baseBlock/time.Second) || status.Unlocked {
		t.Fatalf("status while blocked: %+v", status)
	}
	v.clock.Advance(baseBlock)
	// One more failure doubles the wait.
	if _, err := v.Unlock("root", "still wrong"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatal(err)
	}
	if _, err := v.Unlock("root", testPassphrase); !errors.As(err, &throttled) || throttled.RetryAfter != 2*baseBlock {
		t.Fatalf("second block: %v", err)
	}
	v.clock.Advance(2 * baseBlock)
	v.unlock(t)
	// A success clears the count.
	v.Lock("root")
	if _, err := v.Unlock("root", "wrong again"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatal(err)
	}
	v.unlock(t)
}

func TestPassphraseRules(t *testing.T) {
	v := newTestVault(t)
	var validation *ValidationError
	for _, passphrase := range []string{"", "short", "onze-caract", strings.Repeat("x", MaxPassphraseLength+1)} {
		if err := v.Init("root", passphrase); !errors.As(err, &validation) {
			t.Fatalf("passphrase of %d characters: %v", len(passphrase), err)
		}
	}
	if _, err := os.Stat(filepath.Join(v.dir, "vault.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused passphrase created the vault")
	}
	// Twelve characters, counted as characters and not bytes.
	if err := v.Init("root", "éééééééééééé"); err != nil {
		t.Fatal(err)
	}
}

func TestNames(t *testing.T) {
	for name, want := range map[string]bool{
		"shop": true, "shop-api_2": true, "0x": true, strings.Repeat("a", 63): true,
		"": false, "Shop": false, "-shop": false, "_shop": false, "shop.api": false, "shop/api": false,
		"../shop": false, "shop ": false, "shop\n": false, "..": false, strings.Repeat("a", 64): false, "café": false,
	} {
		if got := ValidateAppName(name) == nil; got != want {
			t.Errorf("app name %q: valid=%v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"API_TOKEN": true, "_x": true, "a1": true, "lower_case": true, strings.Repeat("A", 128): true,
		"": false, "1A": false, "A-B": false, "A B": false, "A=B": false, "A.B": false, "É": false,
		"A\n": false, strings.Repeat("A", 129): false,
	} {
		if got := ValidateVarName(name) == nil; got != want {
			t.Errorf("variable name %q: valid=%v, want %v", name, got, want)
		}
	}
}

func TestAppsAndVariables(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop", "blog")
	var validation *ValidationError
	if _, err := v.CreateApp("root", "../evil"); !errors.As(err, &validation) {
		t.Fatalf("traversal in an app name: %v", err)
	}
	if _, err := v.CreateApp("root", "shop"); !errors.Is(err, ErrAppExists) {
		t.Fatalf("duplicate app: %v", err)
	}
	if _, err := v.SetVar("root", "missing", "A", "x"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("set in a missing app: %v", err)
	}
	if _, err := v.SetVar("root", "shop", "A-B", "x"); !errors.As(err, &validation) {
		t.Fatalf("invalid variable name: %v", err)
	}
	if _, err := v.SetVar("root", "shop", "A", strings.Repeat("x", MaxValueBytes+1)); !errors.As(err, &validation) {
		t.Fatalf("oversized value: %v", err)
	}
	if _, err := v.SetVar("root", "shop", "A", "nul\x00byte"); !errors.As(err, &validation) {
		t.Fatalf("NUL in a value: %v", err)
	}
	for _, name := range []string{"B", "A", "C"} {
		if _, err := v.SetVar("root", "shop", name, "value of "+name); err != nil {
			t.Fatal(err)
		}
	}
	// Replacing keeps the position; the order is the one of insertion.
	if _, err := v.SetVar("root", "shop", "A", "replaced"); err != nil {
		t.Fatal(err)
	}
	app, err := v.DeleteVar("root", "shop", "B")
	if err != nil || len(app.Variables) != 2 || app.Variables[0].Name != "A" || app.Variables[1].Name != "C" {
		t.Fatalf("after delete: %+v %v", app, err)
	}
	if _, err := v.DeleteVar("root", "shop", "B"); !errors.Is(err, ErrVarNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	apps, err := v.Apps()
	if err != nil || len(apps) != 2 || apps[0].Name != "blog" || apps[1].Name != "shop" || len(apps[0].Variables) != 0 {
		t.Fatalf("listing: %+v %v", apps, err)
	}
	// The listing has no field that could hold a value.
	listing, _ := json.Marshal(apps)
	if bytes.Contains(listing, []byte("replaced")) || bytes.Contains(listing, []byte("AGE ENCRYPTED")) {
		t.Fatalf("listing shows values: %s", listing)
	}
	if err := v.DeleteApp("root", "blog"); err != nil {
		t.Fatal(err)
	}
	if err := v.DeleteApp("root", "blog"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	v.unlock(t)
	if got, err := v.Reveal("root", "shop", "A"); err != nil || got != "replaced" {
		t.Fatalf("replaced value: %q %v", got, err)
	}
}

func TestConcurrentWritesLoseNothing(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop", "blog")
	v.unlock(t)
	// A second Vault on the same directory stands for another process.
	other, err := New(Options{Dir: v.dir, Home: v.home, Now: v.clock.Now, ScryptWorkFactor: 4})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 24
	var wg sync.WaitGroup
	failures := make(chan error, 4*writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			vault := v.Vault
			if i%2 == 1 {
				vault = other
			}
			name := fmt.Sprintf("VAR_%02d", i)
			if _, err := vault.SetVar("root", "shop", name, fmt.Sprintf("value-%02d", i)); err != nil {
				failures <- err
			}
			if _, err := vault.SetVar("root", "blog", "SHARED", fmt.Sprintf("value-%02d", i)); err != nil {
				failures <- err
			}
			if _, err := v.Reveal("root", "shop", name); err != nil {
				failures <- err
			}
			if _, err := vault.Apps(); err != nil {
				failures <- err
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	apps, err := v.Apps()
	if err != nil || len(apps) != 2 || len(apps[1].Variables) != writers || len(apps[0].Variables) != 1 {
		t.Fatalf("after concurrent writes: %v", err)
	}
	for i := 0; i < writers; i++ {
		name := fmt.Sprintf("VAR_%02d", i)
		if got, err := v.Reveal("root", "shop", name); err != nil || got != fmt.Sprintf("value-%02d", i) {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	entries, err := v.Audit(MaxAuditEntries)
	if err != nil || len(entries) < 2*writers {
		t.Fatalf("audit after concurrent writes: %d %v", len(entries), err)
	}
	// No temporary file is left behind.
	leftovers, _ := filepath.Glob(filepath.Join(v.dir, "apps", ".tmp-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}
}

func TestAuditNeverHoldsAValue(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	target := filepath.Join(v.home, "shop", ".env")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Unlock("alice", "a wrong passphrase attempt"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatal(err)
	}
	v.unlock(t)
	steps := []func() error{
		func() error { _, err := v.SetVar("root", "shop", "API_TOKEN", testValue); return err },
		func() error {
			_, err := v.Import("root", "shop", "DB_PASSWORD='"+testValue+"-db'\nOTHER="+testValue+"-other\n")
			return err
		},
		func() error { _, err := v.Reveal("root", "shop", "API_TOKEN"); return err },
		func() error { _, err := v.Export("root", "shop"); return err },
		func() error { _, err := v.SetTarget("root", "shop", target, "shop-web"); return err },
		func() error { _, err := v.Apply("root", "shop"); return err },
		func() error { _, err := v.DeleteVar("root", "shop", "OTHER"); return err },
		func() error { v.Lock("root"); return nil },
		func() error { return v.DeleteApp("root", "shop") },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		v.clock.Advance(time.Second)
	}
	raw, err := os.ReadFile(filepath.Join(v.dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testValue, testPassphrase, "a wrong passphrase attempt"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the audit trail holds %q", secret)
		}
	}
	if info, _ := os.Stat(filepath.Join(v.dir, "audit.log")); info.Mode().Perm() != 0o600 {
		t.Fatalf("audit trail mode: %o", info.Mode().Perm())
	}
	entries, err := v.Audit(0)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		actions = append(actions, entries[i].Action)
		if entries[i].Time.IsZero() || entries[i].Actor == "" {
			t.Fatalf("incomplete entry: %+v", entries[i])
		}
	}
	want := "init app_create unlock_failed unlock var_set import reveal export target_set apply restart var_delete lock app_delete"
	if got := strings.Join(actions, " "); got != want {
		t.Fatalf("audit actions:\n%s\nwant:\n%s", got, want)
	}
	if entries[len(entries)-3].Actor != "alice" {
		t.Fatalf("failed unlock actor: %+v", entries[len(entries)-3])
	}
	reveal := entries[len(entries)-7]
	if reveal.App != "shop" || reveal.Name != "API_TOKEN" || reveal.Actor != "root" || reveal.Detail != "" {
		t.Fatalf("reveal entry: %+v", reveal)
	}
	if limited, _ := v.Audit(3); len(limited) != 3 || limited[0].Action != "app_delete" {
		t.Fatalf("limited audit: %+v", limited)
	}
}

func TestAuditReadsTheTailOfALargeTrail(t *testing.T) {
	v := newTestVault(t)
	v.ready(t)
	v.mu.Lock()
	for i := 0; i < 4000; i++ {
		if err := v.appendAudit(Entry{Actor: "root", Action: "var_set", App: "shop", Name: fmt.Sprintf("VAR_%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	v.mu.Unlock()
	entries, err := v.Audit(MaxAuditEntries + 50)
	if err != nil || len(entries) != MaxAuditEntries || entries[0].Name != "VAR_3999" || entries[MaxAuditEntries-1].Name != "VAR_3800" {
		t.Fatalf("tail: %d %v", len(entries), err)
	}
}

func TestCorruptVaultIsRefused(t *testing.T) {
	v := newTestVault(t)
	v.ready(t, "shop")
	// A vault whose public key was swapped does not unlock: values encrypted
	// since would be lost.
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(v.dir, "vault.json")
	var doc vaultDoc
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.PublicKey = other.Recipient().String()
	if err := writeJSONFile(path, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Unlock("root", testPassphrase); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("swapped public key: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Status(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("broken vault file: %v", err)
	}
	if err := v.Init("root", testPassphrase); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("init over a broken vault must not replace it: %v", err)
	}
}
