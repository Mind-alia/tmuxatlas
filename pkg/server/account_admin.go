package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/monitor"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
	"github.com/go-chi/chi/v5"
)

type accountView struct {
	ID                string    `json:"id"`
	Username          string    `json:"username"`
	Role              string    `json:"role"`
	Disabled          bool      `json:"disabled"`
	CreatedAt         time.Time `json:"created_at"`
	PasswordChangedAt time.Time `json:"password_changed_at"`
}

func viewAccount(user accounts.User) accountView {
	return accountView{user.ID, user.Username, user.Role, user.Disabled, user.CreatedAt, user.PasswordChangedAt}
}

func writeAccountError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, accounts.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, accounts.ErrProtectedAdmin), errors.Is(err, accounts.ErrDuplicateUsername):
		status = http.StatusConflict
	case errors.Is(err, accounts.ErrInvalidUsername), errors.Is(err, accounts.ErrInvalidPassword):
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
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		http.Error(w, "requête invalide", http.StatusBadRequest)
		return
	}
	hash, err := accounts.HashPassword(request.Password)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	user, err := opts.AccountsAuth.Store.Create(request.Username, hash, "user")
	if err != nil {
		writeAccountError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(viewAccount(user))
}

func handleAdminUserUpdate(w http.ResponseWriter, r *http.Request, opts *Options) {
	identity, _ := auth.AccountFromContext(r.Context())
	var request struct {
		Password *string `json:"password"`
		Disabled *bool   `json:"disabled"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || (request.Password == nil && request.Disabled == nil) {
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
	user, err := opts.AccountsAuth.UpdateAccount(chi.URLParam(r, "id"), identity.User.ID, hash, request.Disabled)
	if err != nil {
		writeAccountError(w, err)
		return
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
	// The account is gone: its CLI sessions become unassigned.
	_ = opts.AccountsAuth.Store.CLIAssignments().RemoveUser(id)
	w.WriteHeader(http.StatusNoContent)
}

type cliPersonProfileView struct {
	CLI       string `json:"cli"`
	Name      string `json:"name"`
	Account   string `json:"account,omitempty"`
	Connected bool   `json:"connected"`
}

type cliPersonView struct {
	Person   string                 `json:"person"`
	Owner    *string                `json:"owner"`
	Profiles []cliPersonProfileView `json:"profiles"`
	// Missing is true when an assignment remains for a person that no longer
	// has any ai-accounts profile on this host.
	Missing bool `json:"missing,omitempty"`
}

// listCLIProfiles is replaceable in tests.
var listCLIProfiles = monitor.ListCLIProfiles

// handleAdminCLIAssignments lists the ai-accounts CLI persons (Aris, Aris2…)
// with the TmuxAtlas account they belong to (GET), or changes one assignment
// (PUT {"person": "...", "owner": "<user id>" | ""}).
func handleAdminCLIAssignments(w http.ResponseWriter, r *http.Request, opts *Options) {
	store := opts.AccountsAuth.Store.CLIAssignments()
	if r.Method == http.MethodPut {
		var request struct {
			Person string `json:"person"`
			Owner  string `json:"owner"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || accounts.NormalizeCLIPerson(request.Person) == "" {
			http.Error(w, "requête invalide", http.StatusBadRequest)
			return
		}
		if request.Owner != "" {
			users, err := opts.AccountsAuth.Store.List()
			if err != nil {
				writeAccountError(w, err)
				return
			}
			found := false
			for _, user := range users {
				if user.ID == request.Owner {
					found = true
					break
				}
			}
			if !found {
				writeAccountError(w, accounts.ErrNotFound)
				return
			}
		}
		if err := store.Set(request.Person, request.Owner); err != nil {
			if errors.Is(err, accounts.ErrInvalidCLIPerson) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeAccountError(w, err)
			return
		}
	}
	assignments, err := store.All()
	if err != nil {
		writeAccountError(w, err)
		return
	}
	byPerson := make(map[string]*cliPersonView)
	order := make([]string, 0)
	for _, profile := range listCLIProfiles() {
		key := accounts.NormalizeCLIPerson(profile.Person)
		view, ok := byPerson[key]
		if !ok {
			view = &cliPersonView{Person: profile.Person, Profiles: []cliPersonProfileView{}}
			if owner, assigned := assignments[key]; assigned {
				owner := owner
				view.Owner = &owner
			}
			byPerson[key] = view
			order = append(order, key)
		}
		view.Profiles = append(view.Profiles, cliPersonProfileView{
			CLI: profile.CLI, Name: profile.Name, Account: profile.Account, Connected: profile.Connected,
		})
	}
	for key, owner := range assignments {
		if _, ok := byPerson[key]; ok {
			continue
		}
		owner := owner
		byPerson[key] = &cliPersonView{Person: key, Owner: &owner, Profiles: []cliPersonProfileView{}, Missing: true}
		order = append(order, key)
	}
	sort.Strings(order)
	persons := make([]cliPersonView, 0, len(order))
	for _, key := range order {
		persons = append(persons, *byPerson[key])
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"persons": persons})
}

func accountAdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := auth.AccountFromContext(r.Context())
		if !ok || identity.User.Role != "admin" {
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
	// Any account may reassign or release any session. Only a plain claim, with
	// no explicit owner, is limited to a free session.
	force := identity.User.Role == "admin" || operation == "release" || request.Owner != ""
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
func handleAccountDirectory(w http.ResponseWriter, opts *Options) {
	users, err := opts.AccountsAuth.Store.List()
	if err != nil {
		writeAccountError(w, err)
		return
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

// annotateCLIOwners fills snapshot.CLIUsers with active TmuxAtlas accounts and
// populates Owner/OwnerName on each CLI account so the monitoring view can
// filter by account. snapshot.CLIAccounts is a per-request copy.
func annotateCLIOwners(snapshot *monitor.Response, opts *Options) {
	if snapshot == nil || opts == nil || opts.AccountsAuth == nil {
		return
	}
	users, err := opts.AccountsAuth.Store.List()
	if err != nil {
		return
	}
	names := make(map[string]string, len(users))
	cliUsers := make([]monitor.CLIUser, 0, len(users))
	for _, user := range users {
		names[user.ID] = user.Username
		if !user.Disabled {
			cliUsers = append(cliUsers, monitor.CLIUser{ID: user.ID, Username: user.Username})
		}
	}
	sort.Slice(cliUsers, func(i, j int) bool {
		return strings.ToLower(cliUsers[i].Username) < strings.ToLower(cliUsers[j].Username)
	})
	snapshot.CLIUsers = cliUsers
	if len(snapshot.CLIAccounts) == 0 {
		return
	}
	assignments, err := opts.AccountsAuth.Store.CLIAssignments().All()
	if err != nil || len(assignments) == 0 {
		return
	}
	for i := range snapshot.CLIAccounts {
		owner, ok := assignments[accounts.NormalizeCLIPerson(snapshot.CLIAccounts[i].Person)]
		if !ok {
			continue
		}
		name, known := names[owner]
		if !known {
			continue
		}
		snapshot.CLIAccounts[i].Owner = owner
		snapshot.CLIAccounts[i].OwnerName = name
	}
}
