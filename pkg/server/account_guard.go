package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/state"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// accountUserRoutes are the routes a guest may call. Every other protected
// route is denied to guests by default. Routes in accountHostScopedRoutes must
// also name a host granted to the guest.
var accountUserRoutes = map[string]bool{
	"GET /api/preferences": true, "POST /api/auth/change-password": true, "GET /api/accounts/directory": true,
	"GET /api/sessions": true, "GET /api/hosts": true, "GET /api/hosts/trust": true, "GET /api/tool-events": true,
	"GET /api/activity": true, "GET /api/session/directories": true,
	"POST /api/session/new": true, "POST /api/session/claim": true, "POST /api/session/release": true,
	"GET /api/session/preview": true, "POST /api/session/reply": true,
	"POST /api/session/rename": true, "POST /api/session/kill": true,
	"POST /api/session/select-window": true, "POST /api/session/select-pane": true,
	"DELETE /api/tool-event": true, "GET /ws/session": true, "GET /ws/events": true,
	"GET /api/files/hosts": true, "GET /api/files/info": true, "GET /api/files/list": true,
	"GET /api/files/content": true, "GET /api/files/raw": true, "PUT /api/files/content": true,
	"POST /api/files/upload": true, "POST /api/files/mkdir": true, "POST /api/files/rename": true,
	"POST /api/files/delete": true, "GET /api/voice": true, "GET /ws/mic": true,
}

// accountQueryHostRoutes name their host in the host_id query parameter, for
// any method: their bodies are file contents, not JSON targets.
var accountQueryHostRoutes = map[string]bool{
	"/api/files/info": true, "/api/files/list": true, "/api/files/content": true,
	"/api/files/raw": true, "/api/files/upload": true, "/api/files/mkdir": true,
	"/api/files/rename": true, "/api/files/delete": true,
}

// accountTargetedRoutes act on an existing session, which must exist on a
// host granted to the guest.
var accountTargetedRoutes = map[string]bool{
	"/ws/session": true, "/api/session/preview": true, "/api/session/reply": true,
	"/api/session/rename": true, "/api/session/kill": true, "/api/session/select-window": true,
	"/api/session/select-pane": true, "/api/session/release": true, "/api/tool-event": true,
}

// accountHostScopedRoutes name a host but no existing session.
var accountHostScopedRoutes = map[string]bool{
	"/api/session/new": true, "/api/session/claim": true, "/api/session/directories": true,
	"/api/voice": true, "/ws/mic": true,
}

// accountMicRoutes feed the microphone of the host named by ?host= (empty =
// the hub's own host). A guest may use the microphone of a host they may use
// in a terminal, and never the hub's own host; an admin may use any host.
var accountMicRoutes = map[string]bool{"/api/voice": true, "/ws/mic": true}

// Routes outside the user allow-list are recorded explicitly so a router walk
// can detect newly introduced routes that have not received a security review.
var accountOtherRoutes = map[string]string{
	"GET /api/auth/status": "public", "POST /api/auth/login": "public",
	"POST /api/auth/logout": "authenticated", "GET /api/version": "public",
	"GET /api/admin/users": "admin", "POST /api/admin/users": "admin",
	"PATCH /api/admin/users/{id}": "admin", "DELETE /api/admin/users/{id}": "admin",
	"GET /api/admin/sessions": "admin", "GET /api/peers": "admin", "GET /api/admin/hosts": "admin",
	"GET /api/ops": "admin", "POST /api/ops/notifications/test": "admin",
	"POST /api/admin/users/{id}/invitation": "admin", "DELETE /api/admin/users/{id}/invitation": "admin",
	"POST /api/admin/users/{id}/pairing": "admin",
	"POST /api/auth/invitation/inspect":  "public", "POST /api/auth/invitation/accept": "public",
	"GET /api/monitor": "admin", "GET /api/stats": "admin",
	"GET /api/agent-status": "admin", "DELETE /api/tool-events": "admin",
	"GET /api/push/vapid-key": "admin", "POST /api/push/subscribe": "admin",
	"POST /api/push/unsubscribe": "admin", "PUT /api/preferences": "admin", "POST /api/pair": "admin",
	"GET /ws/peer": "peer", "GET /ws/peer-pty": "peer", "GET /ws/peer-mic": "peer",
	"GET /api/sudo/requests": "admin", "GET /api/sudo/requests/{id}": "admin", "GET /api/sudo/peers": "admin",
	"POST /api/sudo/requests/{id}/edit": "admin", "POST /api/sudo/requests/{id}/refuse": "admin",
	"POST /api/sudo/requests/{id}/approve": "admin", "POST /api/sudo/requests/{id}/done": "admin",
	"GET /ws/sudo": "admin", "POST /api/pair/complete": "peer", "GET /*": "public",
	"CONNECT /api/*": "denied", "DELETE /api/*": "denied", "GET /api/*": "denied",
	"HEAD /api/*": "denied", "OPTIONS /api/*": "denied", "PATCH /api/*": "denied",
	"POST /api/*": "denied", "PUT /api/*": "denied", "TRACE /api/*": "denied",
}

// accountPolicy checks guest requests and tracks guest WebSocket connections.
// Its mutex only guards the connection table: it is never held while a
// handler runs or while a peer is asked anything, so a slow or silent peer
// cannot stall other requests.
type accountPolicy struct {
	opts        *Options
	router      runtimeActionExecutor
	mu          sync.Mutex
	connections map[uint64]accountConnection
	nextID      uint64
	owners      *ownerCache
}

type accountConnection struct {
	accountID string
	host      string
	session   string
	cancel    context.CancelFunc
	done      chan struct{}
}

func newAccountPolicy(opts *Options) *accountPolicy {
	return &accountPolicy{opts: opts, connections: make(map[uint64]accountConnection), owners: newOwnerCache(ownerCacheTTL, ownerRate, ownerBurst)}
}

// remoteOwner asks the agent for current ownership instead of trusting a
// potentially delayed discovery snapshot. Older agents fail closed. Answers
// are cached briefly and calls are rate limited per host (see ownerCache).
func (p *accountPolicy) remoteOwner(host, name string) (tmux.Owner, bool) {
	if p.router == nil {
		return "", false
	}
	return p.owners.get(host, name, func() (tmux.Owner, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := p.router.Execute(ctx, "owner", peer.SessionTarget{HostID: host, Session: name}, json.RawMessage(`{}`))
		if err != nil {
			return "", false
		}
		var response struct {
			Owner tmux.Owner `json:"owner"`
		}
		if json.Unmarshal(result.Result, &response) != nil {
			return "", false
		}
		return response.Owner, true
	})
}

// guestMayUseHost reports whether a guest may use a host: it must be granted
// to them and must not be the hub's own host, which runs under the hub's Unix
// account.
func (p *accountPolicy) guestMayUseHost(user accounts.User, host string) bool {
	if user.IsAdmin() {
		return true
	}
	if host == "" || !user.CanUseHost(host) {
		return false
	}
	return p.opts.GuestLocalHost || p.opts.PeerMgr == nil || !p.opts.PeerMgr.IsLocal(host)
}

// grantableHost rejects hosts that must never be granted to a guest.
func (p *accountPolicy) grantableHost(host string) error {
	if p.opts.PeerMgr != nil && p.opts.PeerMgr.IsLocal(host) && !p.opts.GuestLocalHost {
		return accounts.ErrHostForbidden
	}
	return nil
}

// hostFilter returns the hosts an account may see in state projections.
func (p *accountPolicy) hostFilter(user accounts.User) func(string) bool {
	return func(host string) bool { return p.guestMayUseHost(user, host) }
}

// currentUser reloads an account, so that grants changed after a WebSocket
// connected apply to its next snapshot.
func (p *accountPolicy) currentUser(id string) (accounts.User, bool) {
	if p.opts.AccountsAuth == nil {
		return accounts.User{}, false
	}
	users, err := p.opts.AccountsAuth.Store.List()
	if err != nil {
		return accounts.User{}, false
	}
	for _, user := range users {
		if user.ID == id && !user.Disabled {
			return user, true
		}
	}
	return accounts.User{}, false
}

// accountProjection is the state a non-admin account receives: granted hosts
// only, with verified ownership. An unknown account receives nothing.
func (p *accountPolicy) accountProjection(projection state.Projection, accountID string) state.AccountProjection {
	user, ok := p.currentUser(accountID)
	if !ok {
		return projection.ForHosts(func(string) bool { return false }).ForAccount("")
	}
	return p.verifiedProjection(projection.ForHosts(p.hostFilter(user))).ForAccount(accountID)
}

// owns reports whether an account may use a session. Ownership is a label, so
// any account may use any existing session except Atlas's internal one; the
// pane must still belong to that session.
func (p *accountPolicy) owns(host, name, pane, _ string) bool {
	if p.opts.PeerMgr == nil || host == "" || name == "" || name == "tmuxatlas-ctrl" {
		return false
	}
	var sessions []*tmux.Session
	if p.opts.PeerMgr.IsLocal(host) {
		if p.opts.Client == nil || p.opts.StateMgr == nil {
			return false
		}
		// Read ownership directly from tmux, without using stale cached state.
		current, err := p.opts.Client.ListSessions()
		if err != nil {
			return false
		}
		for _, session := range current {
			if session.Name == name {
				if pane == "" {
					return true
				}
				sessions = p.opts.StateMgr.GetSessions()
				break
			}
		}
	} else {
		if _, ok := p.remoteOwner(host, name); !ok {
			return false
		}
		for _, h := range p.opts.PeerMgr.StateHostSnapshots() {
			if h.ID == host && h.Online {
				sessions = h.Sessions
				break
			}
		}
	}
	for _, session := range sessions {
		if session == nil || session.Name != name {
			continue
		}
		if pane == "" {
			return true
		}
		for _, window := range session.Windows {
			for _, candidate := range window.Panes {
				if candidate.ID == pane {
					return true
				}
			}
		}
	}
	return false
}

// verifiedProjection replaces cached ownership with current host state before
// filtering. An unavailable host fails closed rather than exposing its cache.
func (p *accountPolicy) verifiedProjection(projection state.Projection) state.Projection {
	owners := make(map[state.SessionKey]tmux.Owner)
	// Only the hosts left in the projection, i.e. those the account may see,
	// are asked: a guest never makes the hub wait for someone else's peer.
	visible := make(map[string]bool)
	for _, session := range projection.Sessions {
		visible[session.HostID] = true
	}
	type remoteTarget struct {
		host string
		name string
	}
	type remoteResult struct {
		key   state.SessionKey
		owner tmux.Owner
		ok    bool
	}
	var remote []remoteTarget
	if p.opts.PeerMgr != nil {
		for _, host := range p.opts.PeerMgr.StateHostSnapshots() {
			if !host.Online || !visible[host.ID] {
				continue
			}
			sessions := host.Sessions
			if host.Local {
				if p.opts.Client == nil {
					continue
				}
				var err error
				sessions, err = p.opts.Client.ListSessions()
				if err != nil {
					continue
				}
			}
			for _, session := range sessions {
				if session == nil || session.Name == "tmuxatlas-ctrl" {
					continue
				}
				if !host.Local {
					remote = append(remote, remoteTarget{host: host.ID, name: session.Name})
					continue
				}
				owners[state.NewSessionKey(host.ID, session.Name)] = session.Owner
			}
		}
	}
	if len(remote) > 0 {
		jobs := make(chan remoteTarget)
		results := make(chan remoteResult, len(remote))
		var workers sync.WaitGroup
		for range min(8, len(remote)) {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for target := range jobs {
					owner, ok := p.remoteOwner(target.host, target.name)
					results <- remoteResult{key: state.NewSessionKey(target.host, target.name), owner: owner, ok: ok}
				}
			}()
		}
		for _, target := range remote {
			jobs <- target
		}
		close(jobs)
		workers.Wait()
		close(results)
		for result := range results {
			if result.ok {
				owners[result.key] = result.owner
			}
		}
	}
	for key, session := range projection.Sessions {
		owner, ok := owners[key]
		if !ok {
			delete(projection.Sessions, key)
			continue
		}
		session.Owner = owner
		projection.Sessions[key] = session
	}
	return projection
}

// watchOwnership closes active terminals when discovery reports an owner
// change outside an Atlas request, including changes reported by an agent.
func (p *accountPolicy) watchOwnership(ctx context.Context) {
	if p.opts.PeerMgr == nil {
		return
	}
	// Only sessions with an open guest terminal are checked, outside the
	// lock: asking a silent peer must not block requests or other checks.
	localSessions := func() (map[string]bool, bool) {
		if p.opts.Client == nil {
			return nil, false
		}
		sessions, err := p.opts.Client.ListSessions()
		if err != nil {
			return nil, false
		}
		names := make(map[string]bool, len(sessions))
		for _, session := range sessions {
			if session != nil && session.Name != "tmuxatlas-ctrl" {
				names[session.Name] = true
			}
		}
		return names, true
	}
	changes := p.opts.PeerMgr.Subscribe()
	defer p.opts.PeerMgr.Unsubscribe(changes)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
		case <-ticker.C:
		}
		p.mu.Lock()
		watched := make([]accountConnection, 0, len(p.connections))
		for _, connection := range p.connections {
			if connection.session != "" {
				watched = append(watched, connection)
			}
		}
		p.mu.Unlock()
		var local map[string]bool
		localRead, localOK := false, false
		for _, connection := range watched {
			exists := false
			if p.opts.PeerMgr.IsLocal(connection.host) {
				if !localRead {
					local, localOK = localSessions()
					localRead = true
				}
				// An unreadable local tmux keeps terminals, as before.
				exists = !localOK || local[connection.session]
			} else {
				_, exists = p.remoteOwner(connection.host, connection.session)
			}
			if !exists {
				connection.cancel()
			}
		}
	}
}

func (p *accountPolicy) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := auth.AccountFromContext(r.Context())
		if !ok {
			http.Error(w, "authentification requise", http.StatusUnauthorized)
			return
		}
		// Guests get the allow-list only. The role is reloaded on each request.
		user := !identity.User.IsAdmin()
		if user && !accountUserRoutes[r.Method+" "+r.URL.Path] {
			http.Error(w, "accès interdit", http.StatusForbidden)
			return
		}
		if revoked(identity) {
			http.Error(w, "authentification requise", http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		if accountQueryHostRoutes[path] || path == "/api/files/hosts" {
			if user && accountQueryHostRoutes[path] && !p.guestMayUseHost(identity.User, r.URL.Query().Get("host_id")) {
				http.Error(w, "accès interdit", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		targeted := accountTargetedRoutes[path]
		if user && (targeted || accountHostScopedRoutes[path]) {
			host, session, pane := r.URL.Query().Get("host_id"), r.URL.Query().Get("session"), r.URL.Query().Get("pane")
			if path == "/ws/session" {
				host, session = r.URL.Query().Get("host"), r.URL.Query().Get("name")
			} else if accountMicRoutes[path] {
				host = r.URL.Query().Get("host")
			} else if r.Method != http.MethodGet {
				controller := http.NewResponseController(w)
				if controller.SetReadDeadline(time.Now().Add(10*time.Second)) == nil {
					defer controller.SetReadDeadline(time.Time{})
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
				if err != nil || len(body) > 4096 {
					http.Error(w, "requête invalide", http.StatusBadRequest)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				var target struct {
					Host    string `json:"host"`
					HostID  string `json:"host_id"`
					Session string `json:"session"`
					Pane    string `json:"pane"`
				}
				if json.Unmarshal(body, &target) != nil {
					http.Error(w, "requête invalide", http.StatusBadRequest)
					return
				}
				if target.Host != "" && target.HostID != "" && target.Host != target.HostID {
					http.Error(w, "cibles contradictoires", http.StatusBadRequest)
					return
				}
				host, session, pane = target.HostID, target.Session, target.Pane
				if host == "" {
					host = target.Host
				}
			}
			// The host must be granted to the guest; the hub's own host never is.
			if !p.guestMayUseHost(identity.User, host) || (targeted && !p.owns(host, session, pane, identity.User.ID)) {
				http.Error(w, "accès interdit", http.StatusForbidden)
				return
			}
		}
		if user && (path == "/ws/session" || path == "/ws/events" || path == "/ws/mic") {
			ctx, cancel := context.WithCancel(r.Context())
			done := make(chan struct{})
			connection := accountConnection{accountID: identity.User.ID, cancel: cancel, done: done}
			if path == "/ws/session" {
				connection.host = r.URL.Query().Get("host")
				connection.session = r.URL.Query().Get("name")
			}
			p.mu.Lock()
			p.nextID++
			id := p.nextID
			p.connections[id] = connection
			p.mu.Unlock()
			defer func() {
				cancel()
				close(done)
				p.mu.Lock()
				delete(p.connections, id)
				p.mu.Unlock()
			}()
			// Checked again once registered: a revocation that raced with the
			// checks above still closes this connection.
			if revoked(identity) {
				http.Error(w, "authentification requise", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// revoked reports whether the session behind identity was revoked after the
// authentication middleware resolved it.
func revoked(identity auth.AccountIdentity) bool {
	select {
	case <-identity.Done:
		return true
	default:
		return false
	}
}
