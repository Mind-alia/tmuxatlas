package sudo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

// Limits bounds how long each stage may last.
type Limits struct {
	TTL          time.Duration // pending/approved request lifetime
	GrantTTL     time.Duration // window to open the terminal after approval
	RunTTL       time.Duration // hard ceiling for a running terminal, seen from the hub
	ReviewTTL    time.Duration // unreviewed output is withheld after this
	Retention    time.Duration // final requests are forgotten after this
	MaxPerHost   int // open requests per host
	MaxOpen      int
	MaxRevisions int
	// CreateWindow and MaxCreatesPerWindow bound how many requests one host
	// may file, cancelled ones included, so a host cannot flood the panel
	// and the notifications by filing and cancelling in a loop.
	CreateWindow        time.Duration
	MaxCreatesPerWindow int
}

func DefaultLimits() Limits {
	return Limits{
		TTL: 15 * time.Minute, GrantTTL: 60 * time.Second, RunTTL: 16 * time.Minute,
		ReviewTTL: 30 * time.Minute, Retention: 30 * time.Minute,
		MaxPerHost: 4, MaxOpen: 32, MaxRevisions: 32,
		CreateWindow: 10 * time.Minute, MaxCreatesPerWindow: 12,
	}
}

// CreateInput is what an agent submits.
type CreateInput struct {
	Origin  Origin
	Command string
	Reason  string
}

// Grant lets one administrator open the terminal for one exact version, once.
type Grant struct {
	Token     string    `json:"grant"`
	RequestID string    `json:"request_id"`
	Version   int       `json:"version"`
	ExpiresAt time.Time `json:"expires_at"`
}

type grantRecord struct {
	digest    [32]byte
	version   int
	approver  string
	expiresAt time.Time
}

type entry struct {
	request Request
	grant   *grantRecord
	changed chan struct{} // closed and replaced on every change
	runEnds time.Time
}

// Store keeps requests in memory. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	limits   Limits
	now      func() time.Time
	entries  map[string]*entry
	created  map[string][]time.Time // per host, within CreateWindow
	onChange func(Request)
}

func NewStore(limits Limits, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{limits: limits, now: now, entries: make(map[string]*entry), created: make(map[string][]time.Time)}
}

// OnChange registers a callback run after each change, outside the lock.
func (s *Store) OnChange(callback func(Request)) {
	s.mu.Lock()
	s.onChange = callback
	s.mu.Unlock()
}

func randomToken(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic("system random source unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func newID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		panic("system random source unavailable: " + err.Error())
	}
	return "sudo-" + hex.EncodeToString(value)
}

// commit stamps the change, wakes waiters and returns a copy for callbacks.
func (s *Store) commit(e *entry, now time.Time) Request {
	e.request.UpdatedAt = now
	close(e.changed)
	e.changed = make(chan struct{})
	return e.request.clone()
}

func (s *Store) notify(changes ...Request) {
	s.mu.Lock()
	callback := s.onChange
	s.mu.Unlock()
	if callback == nil {
		return
	}
	for _, change := range changes {
		callback(change)
	}
}

// Create records a new pending request.
func (s *Store) Create(input CreateInput) (Request, error) {
	if input.Origin.HostID == "" {
		return Request{}, ErrForbidden
	}
	if err := ValidateCommand(input.Command); err != nil {
		return Request{}, err
	}
	if err := ValidateReason(input.Reason); err != nil {
		return Request{}, err
	}
	s.mu.Lock()
	now := s.now()
	expired := s.sweepLocked(now)
	open, perHost := 0, 0
	for _, e := range s.entries {
		if !e.request.Status.Final() {
			open++
			if e.request.Origin.HostID == input.Origin.HostID {
				perHost++
			}
		}
	}
	recent := s.recentCreatesLocked(input.Origin.HostID, now)
	if open >= s.limits.MaxOpen || perHost >= s.limits.MaxPerHost ||
		(s.limits.MaxCreatesPerWindow > 0 && len(recent) >= s.limits.MaxCreatesPerWindow) {
		s.mu.Unlock()
		s.notify(expired...)
		return Request{}, ErrTooMany
	}
	s.created[input.Origin.HostID] = append(recent, now)
	e := &entry{changed: make(chan struct{}), request: Request{
		ID: newID(), Status: StatusPending, Version: 1, Command: input.Command,
		Reason: input.Reason, Origin: input.Origin, CreatedAt: now, UpdatedAt: now,
		ExpiresAt: now.Add(s.limits.TTL),
		Revisions: []Revision{{Version: 1, Command: input.Command, By: "agent", At: now}},
	}}
	s.entries[e.request.ID] = e
	created := e.request.clone()
	s.mu.Unlock()
	s.notify(append(expired, created)...)
	return created, nil
}

// recentCreatesLocked returns the creation times of host within the window,
// forgetting older ones.
func (s *Store) recentCreatesLocked(host string, now time.Time) []time.Time {
	times := s.created[host]
	kept := times[:0]
	for _, at := range times {
		if now.Sub(at) < s.limits.CreateWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(s.created, host)
		return nil
	}
	s.created[host] = kept
	return kept
}

// Get returns one request.
func (s *Store) Get(id string) (Request, bool) {
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	e, ok := s.entries[id]
	var request Request
	if ok {
		request = e.request.clone()
	}
	s.mu.Unlock()
	s.notify(expired...)
	return request, ok
}

// List returns every known request, most recent first.
func (s *Store) List() []Request {
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	requests := make([]Request, 0, len(s.entries))
	for _, e := range s.entries {
		requests = append(requests, e.request.clone())
	}
	s.mu.Unlock()
	s.notify(expired...)
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.After(requests[j].CreatedAt) })
	return requests
}

// mutate applies change to one request under the lock.
func (s *Store) mutate(id string, change func(e *entry, now time.Time) error) (Request, error) {
	s.mu.Lock()
	now := s.now()
	expired := s.sweepLocked(now)
	e, ok := s.entries[id]
	if !ok {
		s.mu.Unlock()
		s.notify(expired...)
		return Request{}, ErrNotFound
	}
	if err := change(e, now); err != nil {
		s.mu.Unlock()
		s.notify(expired...)
		return Request{}, err
	}
	changed := s.commit(e, now)
	s.mu.Unlock()
	s.notify(append(expired, changed)...)
	return changed, nil
}

// Edit replaces the command of a pending request. expected must be the
// version the editor saw, so two administrators cannot overwrite each other.
func (s *Store) Edit(id string, expected int, command string, by Actor) (Request, error) {
	if err := ValidateCommand(command); err != nil {
		return Request{}, err
	}
	return s.mutate(id, func(e *entry, now time.Time) error {
		if e.request.Status != StatusPending {
			return ErrInvalidState
		}
		if e.request.Version != expected {
			return ErrVersionMismatch
		}
		if command == e.request.Command {
			return nil
		}
		if len(e.request.Revisions) >= s.limits.MaxRevisions {
			return ErrTooMany
		}
		e.request.Version++
		e.request.Command = command
		e.request.Revisions = append(e.request.Revisions, Revision{Version: e.request.Version, Command: command, By: by.Name, At: now})
		return nil
	})
}

// Refuse ends a request that has not started running.
func (s *Store) Refuse(id string, by Actor, note string) (Request, error) {
	if err := validateText(note, MaxNoteBytes); err != nil {
		return Request{}, err
	}
	return s.mutate(id, func(e *entry, _ time.Time) error {
		if e.request.Status != StatusPending && e.request.Status != StatusApproved && e.request.Status != StatusAwaitingReview {
			return ErrInvalidState
		}
		e.request.Status = StatusRefused
		e.request.Approver = &by
		e.request.Note = note
		e.request.Capture = nil
		e.grant = nil
		return nil
	})
}

// Approve issues a single-use grant for the exact version the administrator
// saw. The token is returned once and only its digest is kept.
func (s *Store) Approve(id string, expected int, peer string, by Actor) (Request, Grant, error) {
	var grant Grant
	request, err := s.mutate(id, func(e *entry, now time.Time) error {
		if e.request.Status != StatusPending {
			return ErrInvalidState
		}
		if e.request.Version != expected {
			return ErrVersionMismatch
		}
		token := randomToken(32)
		expires := now.Add(s.limits.GrantTTL)
		e.grant = &grantRecord{digest: sha256.Sum256([]byte(token)), version: expected, approver: by.ID, expiresAt: expires}
		e.request.Status = StatusApproved
		e.request.Approver = &by
		e.request.Peer = peer
		grant = Grant{Token: token, RequestID: id, Version: expected, ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return Request{}, Grant{}, err
	}
	return request, grant, nil
}

// Redeem consumes a grant and marks the request running. The same
// administrator who approved must redeem it.
func (s *Store) Redeem(token string, by Actor) (Request, error) {
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	now := s.now()
	expired := s.sweepLocked(now)
	var found *entry
	for _, e := range s.entries {
		if e.grant != nil && subtle.ConstantTimeCompare(e.grant.digest[:], digest[:]) == 1 {
			found = e
			break
		}
	}
	if found == nil || found.request.Status != StatusApproved || found.grant.approver != by.ID ||
		found.grant.version != found.request.Version || !now.Before(found.grant.expiresAt) {
		s.mu.Unlock()
		s.notify(expired...)
		return Request{}, ErrGrantInvalid
	}
	found.grant = nil
	found.request.Status = StatusRunning
	found.runEnds = now.Add(s.limits.RunTTL)
	changed := s.commit(found, now)
	s.mu.Unlock()
	s.notify(append(expired, changed)...)
	return changed, nil
}

// Finish stores the capture of a run and holds it for review.
func (s *Store) Finish(id string, capture Capture) (Request, error) {
	if len(capture.Output) > MaxOutputBytes {
		capture.Output = capture.Output[:MaxOutputBytes]
		capture.OutputTruncated = true
	}
	return s.mutate(id, func(e *entry, now time.Time) error {
		if e.request.Status != StatusRunning || capture.Version != e.request.Version {
			return ErrInvalidState
		}
		capture.Command = e.request.Command
		e.request.Capture = &capture
		e.request.Status = StatusAwaitingReview
		e.request.ExpiresAt = now.Add(s.limits.ReviewTTL)
		return nil
	})
}

// Fail ends an approved or running request that could not complete.
func (s *Store) Fail(id string, note string) (Request, error) {
	if len(note) > MaxNoteBytes {
		note = note[:MaxNoteBytes]
	}
	return s.mutate(id, func(e *entry, _ time.Time) error {
		if e.request.Status != StatusApproved && e.request.Status != StatusRunning {
			return ErrInvalidState
		}
		e.request.Status = StatusFailed
		e.request.Note = note
		e.grant = nil
		return nil
	})
}

// Review decides what the agent receives.
func (s *Store) Review(id string, decision Decision, by Actor, note string) (Request, error) {
	if err := validateText(note, MaxNoteBytes); err != nil {
		return Request{}, err
	}
	switch decision {
	case DecisionSend, DecisionExitCode, DecisionNothing:
	default:
		return Request{}, ErrInvalidText
	}
	return s.mutate(id, func(e *entry, _ time.Time) error {
		if e.request.Status != StatusAwaitingReview || e.request.Capture == nil {
			return ErrInvalidState
		}
		e.request.Decision = decision
		e.request.Note = note
		e.request.Approver = &by
		e.request.Status = StatusDelivered
		if decision == DecisionNothing {
			e.request.Status = StatusWithheld
		}
		return nil
	})
}

// Rerun discards a capture under review and returns the request to pending
// with a new version, so it must be approved again.
func (s *Store) Rerun(id string, expected int, command string, by Actor) (Request, error) {
	if err := ValidateCommand(command); err != nil {
		return Request{}, err
	}
	return s.mutate(id, func(e *entry, now time.Time) error {
		if e.request.Status != StatusAwaitingReview {
			return ErrInvalidState
		}
		if e.request.Version != expected {
			return ErrVersionMismatch
		}
		if len(e.request.Revisions) >= s.limits.MaxRevisions {
			return ErrTooMany
		}
		e.request.Version++
		e.request.Command = command
		e.request.Revisions = append(e.request.Revisions, Revision{Version: e.request.Version, Command: command, By: by.Name, At: now})
		e.request.Capture = nil
		e.request.Status = StatusPending
		e.request.ExpiresAt = now.Add(s.limits.TTL)
		return nil
	})
}

// Cancel lets the originating host give up before the terminal opens.
func (s *Store) Cancel(id, hostID string) (Request, error) {
	return s.mutate(id, func(e *entry, _ time.Time) error {
		if e.request.Origin.HostID != hostID {
			return ErrForbidden
		}
		if e.request.Status != StatusPending && e.request.Status != StatusApproved {
			return ErrInvalidState
		}
		e.request.Status = StatusCancelled
		e.grant = nil
		return nil
	})
}

// Wait blocks until the request is final or ctx ends, then returns its
// current state.
func (s *Store) Wait(ctx context.Context, id string) (Request, error) {
	for {
		s.mu.Lock()
		expired := s.sweepLocked(s.now())
		e, ok := s.entries[id]
		if !ok {
			s.mu.Unlock()
			s.notify(expired...)
			return Request{}, ErrNotFound
		}
		request, changed := e.request.clone(), e.changed
		s.mu.Unlock()
		s.notify(expired...)
		if request.Status.Final() {
			return request, nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return request, nil
		case <-changed:
		case <-timer.C: // lets expiry happen without other traffic
		}
		timer.Stop()
	}
}

// Sweep applies expirations; callers may run it periodically.
func (s *Store) Sweep() {
	s.mu.Lock()
	expired := s.sweepLocked(s.now())
	s.mu.Unlock()
	s.notify(expired...)
}

func (s *Store) sweepLocked(now time.Time) []Request {
	var changes []Request
	for id, e := range s.entries {
		switch e.request.Status {
		case StatusApproved:
			if e.grant != nil && !now.Before(e.grant.expiresAt) {
				// An unused grant returns the request to the queue.
				e.grant = nil
				e.request.Status = StatusPending
				changes = append(changes, s.commit(e, now))
			}
			if !now.Before(e.request.ExpiresAt) {
				e.grant = nil
				e.request.Status = StatusExpired
				changes = append(changes, s.commit(e, now))
			}
		case StatusPending:
			if !now.Before(e.request.ExpiresAt) {
				e.request.Status = StatusExpired
				changes = append(changes, s.commit(e, now))
			}
		case StatusRunning:
			if !now.Before(e.runEnds) {
				e.request.Status = StatusFailed
				e.request.Note = "terminal sudo sans réponse"
				changes = append(changes, s.commit(e, now))
			}
		case StatusAwaitingReview:
			if !now.Before(e.request.ExpiresAt) {
				// Nobody reviewed the output: nothing leaves.
				e.request.Status = StatusWithheld
				e.request.Decision = DecisionNothing
				e.request.Note = "relecture non faite à temps"
				changes = append(changes, s.commit(e, now))
			}
		default:
			if now.Sub(e.request.UpdatedAt) > s.limits.Retention {
				delete(s.entries, id)
			}
		}
	}
	return changes
}
