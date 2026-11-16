package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("mot de passe"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func TestLegacyAndUnknownRolesBecomeGuests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	raw := `[{"id":"a","username":"alice","role":"user"},{"id":"b","username":"bob","role":"root"},{"id":"c","username":"carol","role":"admin"}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	users, err := OpenStore(path).List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{RoleGuest, RoleGuest, RoleAdmin}
	for i, user := range users {
		if user.Role != want[i] {
			t.Fatalf("%s role=%q, want %q", user.Username, user.Role, want[i])
		}
	}
	if users[0].CanUseHost("h1") || !users[2].CanUseHost("h1") || users[0].CanUseHost("") {
		t.Fatal("host access does not follow the role")
	}
}

func TestSeveralAdminsAndLastAdminGuard(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	first, err := store.Create("first", testHash(t), RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create("second", testHash(t), RoleGuest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRole(first.ID, second.ID, RoleGuest); !errors.Is(err, ErrProtectedAdmin) {
		t.Fatalf("last admin demoted: %v", err)
	}
	if _, err := store.SetRole(second.ID, first.ID, "superuser"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("invalid role accepted: %v", err)
	}
	if user, err := store.SetRole(second.ID, first.ID, RoleAdmin); err != nil || !user.IsAdmin() {
		t.Fatalf("promotion: %v %v", user, err)
	}
	if _, err := store.SetRole(first.ID, first.ID, RoleGuest); !errors.Is(err, ErrProtectedAdmin) {
		t.Fatalf("self demotion accepted: %v", err)
	}
	pending, _ := store.CreatePending("pending", RoleAdmin, "")
	if _, err := store.SetRole(second.ID, pending.ID, RoleGuest); err == nil {
		// second is not the last admin: first still is one.
	} else {
		t.Fatalf("demotion with another admin: %v", err)
	}
	if _, err := store.SetRole(first.ID, pending.ID, RoleGuest); !errors.Is(err, ErrProtectedAdmin) {
		t.Fatalf("an admin without a password counted as the last admin: %v", err)
	}
	if _, err := store.SetRole(second.ID, first.ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if user, err := store.SetRole(first.ID, second.ID, RoleGuest); err != nil || user.IsAdmin() {
		t.Fatalf("demotion with another admin: %v %v", user, err)
	}
	if _, err := store.SetRole(second.ID, first.ID, RoleGuest); !errors.Is(err, ErrProtectedAdmin) {
		t.Fatalf("new last admin demoted: %v", err)
	}
}

func TestHostGrantsAreExclusiveBetweenGuests(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	admin, _ := store.Create("admin", testHash(t), RoleAdmin)
	alice, _ := store.CreatePending("alice", RoleGuest, "inv-alice")
	bob, _ := store.CreatePending("bob", RoleGuest, "inv-bob")
	if _, err := store.SetAccess(alice.ID, []string{"h-alice", "h-alice"}, "inv-alice", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantHost(bob.ID, "h-alice", nil); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("host shared between guests: %v", err)
	}
	if _, err := store.SetAccess(bob.ID, []string{"../x"}, "", nil); !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("invalid host accepted: %v", err)
	}
	if _, err := store.SetAccess(bob.ID, nil, "Root;", nil); !errors.Is(err, ErrInvalidUnixUser) {
		t.Fatalf("invalid unix user accepted: %v", err)
	}
	forbidden := func(host string) error {
		if host == "hub" {
			return ErrHostForbidden
		}
		return nil
	}
	if _, err := store.GrantHost(bob.ID, "hub", forbidden); !errors.Is(err, ErrHostForbidden) {
		t.Fatalf("forbidden host granted: %v", err)
	}
	// An administrator may keep a host that a guest also has.
	if _, err := store.GrantHost(admin.ID, "h-alice", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRole(admin.ID, alice.ID, RoleGuest); err == nil {
		t.Fatal("demotion created a shared guest host")
	}
	users, _ := store.List()
	if got := users[1].Hosts; len(got) != 1 || got[0] != "h-alice" {
		t.Fatalf("alice hosts=%v", got)
	}
}

func TestInvitationIsSingleUseAndExpires(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "users.json"))
	alice, err := store.CreatePending("alice", RoleGuest, "")
	if err != nil || alice.PasswordHash != "" {
		t.Fatalf("pending account: %v %v", alice, err)
	}
	token, invitation, err := store.CreateInvitation(alice.ID, "admin", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if invitation.ExpiresAt.After(time.Now().Add(MaxInvitationTTL + time.Minute)) {
		t.Fatal("invitation lifetime not capped")
	}
	raw, _ := os.ReadFile(store.path)
	if strings.Contains(string(raw), token) {
		t.Fatal("token stored in clear")
	}
	if user, err := store.InspectInvitation(token); err != nil || user.ID != alice.ID {
		t.Fatalf("inspect: %v %v", user, err)
	}
	if _, err := store.AcceptInvitation(token+"x", testHash(t)); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("wrong token accepted: %v", err)
	}
	if user, err := store.AcceptInvitation(token, testHash(t)); err != nil || user.PasswordHash == "" || user.Invite != nil {
		t.Fatalf("accept: %v %v", user, err)
	}
	if _, err := store.AcceptInvitation(token, testHash(t)); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("token reused: %v", err)
	}
	expired, _, _ := store.CreateInvitation(alice.ID, "admin", time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if _, err := store.InspectInvitation(expired); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatalf("expired token accepted: %v", err)
	}
	if _, err := store.InspectInvitation(""); !errors.Is(err, ErrInvitationInvalid) {
		t.Fatal("empty token accepted")
	}
}
