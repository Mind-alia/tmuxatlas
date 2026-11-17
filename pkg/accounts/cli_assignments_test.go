package accounts

import (
	"path/filepath"
	"testing"
)

func TestCLIAssignments(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	assignments := store.CLIAssignments()

	t.Run("empty", func(t *testing.T) {
		all, err := assignments.All()
		if err != nil || len(all) != 0 {
			t.Fatalf("All() = %v, %v; want empty", all, err)
		}
	})

	t.Run("set is case-insensitive", func(t *testing.T) {
		for _, person := range []string{"Aris", "aris2"} {
			if err := assignments.Set(person, "user-aris"); err != nil {
				t.Fatal(err)
			}
		}
		if err := assignments.Set("Novanns", "user-nova"); err != nil {
			t.Fatal(err)
		}
		persons, err := assignments.PersonsFor("user-aris")
		if err != nil {
			t.Fatal(err)
		}
		if !persons["aris"] || !persons["aris2"] || persons["novanns"] || len(persons) != 2 {
			t.Fatalf("PersonsFor(user-aris) = %v", persons)
		}
	})

	t.Run("persisted across instances", func(t *testing.T) {
		reopened := OpenCLIAssignments(assignments.path)
		all, err := reopened.All()
		if err != nil || all["novanns"] != "user-nova" {
			t.Fatalf("reopened All() = %v, %v", all, err)
		}
	})

	t.Run("unassign and remove user", func(t *testing.T) {
		if err := assignments.Set("ARIS2", ""); err != nil {
			t.Fatal(err)
		}
		if err := assignments.RemoveUser("user-nova"); err != nil {
			t.Fatal(err)
		}
		all, _ := assignments.All()
		if len(all) != 1 || all["aris"] != "user-aris" {
			t.Fatalf("All() = %v; want only aris", all)
		}
	})

	t.Run("invalid person", func(t *testing.T) {
		if err := assignments.Set("  ", "user-aris"); err != ErrInvalidCLIPerson {
			t.Fatalf("Set(blank) = %v; want ErrInvalidCLIPerson", err)
		}
	})

	t.Run("empty user id sees nothing", func(t *testing.T) {
		persons, _ := assignments.PersonsFor("")
		if len(persons) != 0 {
			t.Fatalf("PersonsFor(\"\") = %v", persons)
		}
	})
}
