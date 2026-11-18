package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestMigrateOwnersWithoutTmuxServer(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-migrate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	client, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "migration")
	if err := MigrateOwners(client, marker, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClaimAndRename(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skip(err)
	}
	if err := client.NewSession("ownership-test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.KillSession("ownership-renamed") })
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, owner := range []string{"user1", "user2"} {
		wg.Add(1)
		go func(i int, owner string) {
			defer wg.Done()
			errs[i] = client.ChangeOwner("ownership-test", "", owner, false)
		}(i, owner)
	}
	wg.Wait()
	if !((errs[0] == nil && errors.Is(errs[1], ErrAlreadyOwned)) || (errs[1] == nil && errors.Is(errs[0], ErrAlreadyOwned))) {
		t.Fatalf("claims: %v", errs)
	}
	sessions, err := client.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Owner == "" {
		t.Fatalf("sessions: %v %v", sessions, err)
	}
	owner := sessions[0].Owner
	if err := client.RenameSession("ownership-test", "ownership-renamed"); err != nil {
		t.Fatal(err)
	}
	sessions, err = client.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Owner != owner {
		t.Fatalf("renamed sessions: %v %v", sessions, err)
	}
}

func TestMigrateOwnersOnce(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skip(err)
	}
	if err := client.NewSession("migration-before"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.KillSession("migration-before"); _ = client.KillSession("migration-after") })
	marker := filepath.Join(t.TempDir(), "migration")
	if err := MigrateOwners(client, marker, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := client.NewSession("migration-after"); err != nil {
		t.Fatal(err)
	}
	if err := MigrateOwners(client, marker, "admin"); err != nil {
		t.Fatal(err)
	}
	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]Owner{}
	for _, s := range sessions {
		owners[s.Name] = s.Owner
	}
	if owners["migration-before"] != "admin" || owners["migration-after"] != "" {
		t.Fatalf("owners: %v", owners)
	}
}

func TestNewOwnedSession(t *testing.T) {
	client, err := NewClient()
	if err != nil {
		t.Skip(err)
	}
	if err := client.NewOwnedSession("owned-new", "", "user1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.KillSession("owned-new") })
	sessions, err := client.ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Owner != "user1" {
		t.Fatalf("sessions: %v, %v", sessions, err)
	}
}

// A container shell carries DockerSessionOption and is AdminOnly, whatever
// the marker's value; the value never reaches the listing, so a marker set by
// hand cannot add a session line of its own.
func TestListSessionsMarksContainerShells(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "atlas-admin-only-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	client, err := NewClient()
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = client.Exec("kill-server") })
	for _, name := range []string{"plain", "shell", "zero", "forged"} {
		if err := client.NewOwnedSession(name, "", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	for session, marker := range map[string]string{
		"shell":  "abcdef0123456789",
		"zero":   "0",
		"forged": "x\n$99:plain:0:0:0:0:admin",
	} {
		if _, err := client.Exec("set-option", "-t", ExactWindow(session, ""), DockerSessionOption, marker); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := client.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, session := range sessions {
		if _, twice := got[session.Name]; twice {
			t.Fatalf("%s listée deux fois : %+v", session.Name, sessions)
		}
		got[session.Name] = session.AdminOnly
		if session.Owner != "admin" {
			t.Errorf("%s : propriétaire %q", session.Name, session.Owner)
		}
	}
	want := map[string]bool{"plain": false, "shell": true, "zero": true, "forged": true}
	if len(got) != len(want) {
		t.Fatalf("sessions : %v", got)
	}
	for name, adminOnly := range want {
		if got[name] != adminOnly {
			t.Errorf("%s : AdminOnly %v, attendu %v", name, got[name], adminOnly)
		}
	}
}
