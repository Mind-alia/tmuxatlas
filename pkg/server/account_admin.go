package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"github.com/go-chi/chi/v5"
)

type accountView struct {
	ID                  string     `json:"id"`
	Username            string     `json:"username"`
	Role                string     `json:"role"`
	Disabled            bool       `json:"disabled"`
	CreatedAt           time.Time  `json:"created_at"`
	PasswordChangedAt   time.Time  `json:"password_changed_at"`
	Hosts               []string   `json:"hosts"`
	UnixUser            string     `json:"unix_user"`
	Pending             bool       `json:"pending"`
	InvitationExpiresAt *time.Time `json:"invitation_expires_at,omitempty"`
}

func viewAccount(user accounts.User) accountView {
	view := accountView{
		ID: user.ID, Username: user.Username, Role: user.Role, Disabled: user.Disabled,
		CreatedAt: user.CreatedAt, PasswordChangedAt: user.PasswordChangedAt,
		Hosts: append([]string{}, user.Hosts...), UnixUser: user.UnixUser, Pending: user.PasswordHash == "",
	}
	if user.Invite != nil && time.Now().Before(user.Invite.ExpiresAt) {
		expires := user.Invite.ExpiresAt
		view.InvitationExpiresAt = &expires
	}
	return view
}

type invitationView struct {
	Token     string    `json:"token"`
	Path      string    `json:"path"`
	ExpiresAt time.Time `json:"expires_at"`
}

// newInvitationView returns the link to give the person. The token is in the
// fragment, so the browser never sends it in a request line or a Referer.
func newInvitationView(token string, invitation accounts.Invitation) invitationView {
	return invitationView{Token: token, Path: "/invite#" + token, ExpiresAt: invitation.ExpiresAt}
}

func writeAccountError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, accounts.ErrProtectedAdmin), errors.Is(err, accounts.ErrDuplicateUsername),
		errors.Is(err, accounts.ErrHostTaken):
		status = http.StatusConflict
	case errors.Is(err, accounts.ErrHostForbidden):
		status = http.StatusForbidden
	case errors.Is(err, accounts.ErrInvalidUsername), errors.Is(err, accounts.ErrInvalidPassword),
		errors.Is(err, accounts.ErrInvalidRole), errors.Is(err, accounts.ErrInvalidHost),
		errors.Is(err, accounts.ErrInvalidUnixUser):
		status = http.StatusBadRequest
	}
	message := "erreur interne"
	if status != http.StatusInternalServerError {
		message = err.Error()
	}
	http.Error(w, message, status)
}

func handleAdminUsers(w http.ResponseWriter, r *http.Request, opts *Options) {
	if r.Method == http.MethodGet {
		users, err := opts.AccountsAuth.Store.List()
		if err != nil {
			writeAccountError(w, err)
			return
		}
		views := make([]accountView, 0, len(users))
		for _, user := range users {
			views = append(views, viewAccount(user))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"users": views})
		return
	}
	identity, _ := auth.AccountFromContext(r.Context())
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		UnixUser string `json:"unix_user"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	if request.Role == "" {
		request.Role = accounts.RoleGuest
	}
	if !accounts.ValidUnixUser(request.UnixUser) {
		writeAccountError(w, accounts.ErrInvalidUnixUser)
		return
	}
	var user accounts.User
	var invitation *invitationView
	if request.Password == "" {
		// No password: the person chooses it from a one-time invitation link.
		var err error
		user, err = opts.AccountsAuth.Store.CreatePending(request.Username, request.Role, request.UnixUser)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		token, created, err := opts.AccountsAuth.Store.CreateInvitation(user.ID, identity.User.ID, accounts.DefaultInvitationTTL)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		view := newInvitationView(token, created)
		invitation = &view
		user.Invite = &created
	} else {
		hash, err := accounts.HashPassword(request.Password)
		if err != nil {
			writeAccountError(w, err)
			return
		}
		if user, err = opts.AccountsAuth.Store.Create(request.Username, hash, request.Role); err != nil {
			writeAccountError(w, err)
			return
		}
		if request.UnixUser != "" {
			if user, err = opts.AccountsAuth.Store.SetAccess(user.ID, nil, request.UnixUser, nil); err != nil {
				writeAccountError(w, err)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		accountView
		Invitation *invitationView `json:"invitation,omitempty"`
	}{viewAccount(user), invitation})
}

// handleAdminInvitation creates (POST) or cancels (DELETE) the one-time link
// with which an account chooses a new password.
func handleAdminInvitation(w http.ResponseWriter, r *http.Request, opts *Options) {
	identity, _ := auth.AccountFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if r.Method == http.MethodDelete {
		if err := opts.AccountsAuth.CancelInvitation(id); err != nil {
			writeAccountError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var request struct {
		Hours int `json:"hours"`
	}
	if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	token, invitation, err := opts.AccountsAuth.Store.CreateInvitation(id, identity.User.ID, time.Duration(request.Hours)*time.Hour)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(newInvitationView(token, invitation))
}

// handleAdminPairing creates a pairing code for an account's own peer. The
// host paired with it is granted to that account automatically.
func handleAdminPairing(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.PairingMgr == nil {
		http.Error(w, "appairage indisponible", http.StatusServiceUnavailable)
		return
	}
	id := chi.URLParam(r, "id")
	users, err := opts.AccountsAuth.Store.List()
	if err != nil {
		writeAccountError(w, err)
		return
	}
	found := false
	for _, user := range users {
		found = found || user.ID == id
	}
	if !found {
		writeAccountError(w, accounts.ErrNotFound)
		return
	}
	code, err := opts.PairingMgr.GenerateFor(id)
	if err != nil {
		http.Error(w, "trop de codes en attente", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code.Code, "expires_at": code.ExpiresAt})
}

// handleAdminHosts lists the hosts an administrator can grant, with the guest
// that already has each one.
func handleAdminHosts(w http.ResponseWriter, opts *Options) {
	users, err := opts.AccountsAuth.Store.List()
	if err != nil {
		writeAccountError(w, err)
		return
	}
	guestOf := make(map[string]string)
	for _, user := range users {
		if !user.IsAdmin() {
			for _, host := range user.Hosts {
				guestOf[host] = user.ID
			}
		}
	}
	type hostView struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Online    bool   `json:"online"`
		Local     bool   `json:"local"`
		Grantable bool   `json:"grantable"`
		Guest     string `json:"guest,omitempty"`
	}
	views := make([]hostView, 0)
	if opts.PeerMgr != nil {
		for _, host := range opts.PeerMgr.StateHostSnapshots() {
			views = append(views, hostView{
				ID: host.ID, Name: host.DisplayName, Online: host.Online, Local: host.Local,
				Grantable: !host.Local || opts.GuestLocalHost, Guest: guestOf[host.ID],
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"hosts": views})
}

func handleAdminUserUpdate(w http.ResponseWriter, r *http.Request, opts *Options) {
	identity, _ := auth.AccountFromContext(r.Context())
	var request struct {
		Password *string   `json:"password"`
		Disabled *bool     `json:"disabled"`
		Role     *string   `json:"role"`
		Hosts    *[]string `json:"hosts"`
		UnixUser *string   `json:"unix_user"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil ||
		(request.Password == nil && request.Disabled == nil && request.Role == nil && request.Hosts == nil && request.UnixUser == nil) {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	hash := ""
	if request.Password != nil {
		var err error
		hash, err = accounts.HashPassword(*request.Password)
		if err != nil {
			writeAccountError(w, err)
			return
		}
	}
	id := chi.URLParam(r, "id")
	var user accounts.User
	var err error
	if request.Role != nil || request.Hosts != nil || request.UnixUser != nil {
		policy := newAccountPolicy(opts)
		if user, err = opts.AccountsAuth.UpdateAccess(id, identity.User.ID, request.Role, request.Hosts, request.UnixUser, policy.grantableHost); err != nil {
			writeAccountError(w, err)
			return
		}
	}
	if hash != "" || request.Disabled != nil {
		if user, err = opts.AccountsAuth.UpdateAccount(id, identity.User.ID, hash, request.Disabled); err != nil {
			writeAccountError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(viewAccount(user))
}

func handleAdminUserDelete(w http.ResponseWriter, r *http.Request, opts *Options, router runtimeActionExecutor) {
	identity, _ := auth.AccountFromContext(r.Context())
	id := chi.URLParam(r, "id")
	err := opts.AccountsAuth.DeleteAccountWith(id, identity.User.ID, func() error {
		if router == nil || opts.PeerMgr == nil {
			return errors.New("routeur de sessions indisponible")
		}
		for _, host := range opts.PeerMgr.StateHostSnapshots() {
			if !host.Online {
				return errors.New("un hôte est hors ligne")
			}
			sessions := host.Sessions
			if host.Local && opts.Client != nil {
				var err error
				sessions, err = opts.Client.ListSessions()
				if err != nil {
					return err
				}
			}
			for _, session := range sessions {
				if session == nil || string(session.Owner) != id {
					continue
				}
				payload, _ := json.Marshal(map[string]string{"owner": id, "owner_name": ""})
				if _, err := router.Execute(r.Context(), "release", peer.SessionTarget{HostID: host.ID, Session: session.Name}, payload); err != nil {
					return err
				}
				if !host.Local {
					opts.PeerMgr.UpdateKnownOwner(host.ID, session.Name, "")
				}
				refreshLocalSessionState(opts, host.ID)
			}
		}
		if opts.StateCoordinator != nil {
			if _, err := opts.StateCoordinator.ReplaceHosts(r.Context(), opts.PeerMgr.StateHostSnapshots()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func accountAdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := auth.AccountFromContext(r.Context())
		if !ok || !identity.User.IsAdmin() {
			http.Error(w, "accès interdit", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleAccountOwnership(w http.ResponseWriter, r *http.Request, opts *Options, router runtimeActionExecutor, policy *accountPolicy, operation string) {
	identity, ok := auth.AccountFromContext(r.Context())
	if !ok || router == nil {
		http.Error(w, "authentification requise", http.StatusUnauthorized)
		return
	}
	var request struct {
		HostID  string `json:"host_id"`
		Session string `json:"session"`
		Owner   string `json:"owner"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostID == "" || request.Session == "" {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	owner := identity.User.ID
	ownerName := identity.User.Username
	// A guest can only take a session for themselves.
	if !identity.User.IsAdmin() && request.Owner != "" && request.Owner != identity.User.ID {
		http.Error(w, "accès interdit", http.StatusForbidden)
		return
	}
	// Any account may reassign or release any session. Only a plain claim, with
	// no explicit owner, is limited to a free session.
	force := identity.User.IsAdmin() || operation == "release" || request.Owner != ""
	if request.Owner != "" {
		owner = request.Owner
		if operation == "claim" {
			users, err := opts.AccountsAuth.Store.List()
			if err != nil {
				writeAccountError(w, err)
				return
			}
			found := false
			for _, user := range users {
				if user.ID == owner && !user.Disabled {
					found = true
					ownerName = user.Username
				}
			}
			if !found {
				writeAccountError(w, accounts.ErrNotFound)
				return
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{"owner": owner, "owner_name": ownerName, "force": force})
	result, err := router.Execute(r.Context(), operation, peer.SessionTarget{HostID: request.HostID, Session: request.Session}, payload)
	if err != nil {
		writeRuntimeError(w, err)
		return
	}
	if opts.PeerMgr != nil && !opts.PeerMgr.IsLocal(request.HostID) {
		if operation == "release" {
			owner = ""
		}
		opts.PeerMgr.UpdateKnownOwner(request.HostID, request.Session, tmux.Owner(owner))
		if opts.StateCoordinator != nil {
			if _, err := opts.StateCoordinator.ReplaceHosts(r.Context(), opts.PeerMgr.StateHostSnapshots()); err != nil {
				http.Error(w, "état indisponible", http.StatusServiceUnavailable)
				return
			}
		}
	}
	refreshLocalSessionState(opts, request.HostID)
	writeActionResponse(w, result)
}

// handleAccountDirectory lists the active accounts a session can be given to.
// It exposes names only: no role, date, hash or disabled account.
func handleAccountDirectory(w http.ResponseWriter, r *http.Request, opts *Options) {
	users, err := opts.AccountsAuth.Store.List()
	if err != nil {
		writeAccountError(w, err)
		return
	}
	// A guest does not learn who else has an account.
	if identity, ok := auth.AccountFromContext(r.Context()); ok && !identity.User.IsAdmin() {
		users = []accounts.User{identity.User}
	}
	type entry struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	directory := make([]entry, 0, len(users))
	for _, user := range users {
		if !user.Disabled {
			directory = append(directory, entry{ID: user.ID, Username: user.Username})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(directory)
}
