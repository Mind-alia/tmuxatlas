package server

import (
	"net/http"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// SudoAdmins decides who may approve sudo requests. It is the only point
// where the sudo terminal depends on the account model, so roles can grow
// without touching it.
type SudoAdmins interface {
	// Admin returns the administrator behind an authenticated request.
	Admin(r *http.Request) (sudo.Actor, bool)
}

// defaultSudoAdmins follows the server's authentication mode:
//   - accounts: the account's admin role from feat/comptes-roles
//     (accounts.User.IsAdmin), re-read from the account store at each
//     decision, so a demoted or disabled admin loses the sudo terminal at
//     once even if the request context still carries the old role. Guests,
//     the legacy "user" role and unknown roles are refused;
//   - passkey: the single owner, i.e. any valid session (the auth
//     middleware already ran);
//   - no authentication: nobody, so the sudo terminal is off.
type defaultSudoAdmins struct {
	accounts bool
	enabled  bool
	// lookup reloads an account by ID; nil refuses every account.
	lookup func(id string) (accounts.User, bool)
}

func newSudoAdmins(opts *Options) SudoAdmins {
	admins := defaultSudoAdmins{accounts: opts.AccountsAuth != nil, enabled: opts.AuthEnabled}
	if opts.AccountsAuth != nil && opts.AccountsAuth.Store != nil {
		admins.lookup = storeLookup(opts.AccountsAuth.Store)
	}
	return admins
}

// storeLookup reads the current state of an account from the store.
func storeLookup(store *accounts.Store) func(string) (accounts.User, bool) {
	return func(id string) (accounts.User, bool) {
		users, err := store.List()
		if err != nil {
			return accounts.User{}, false
		}
		for _, user := range users {
			if user.ID == id {
				return user, true
			}
		}
		return accounts.User{}, false
	}
}

func (a defaultSudoAdmins) Admin(r *http.Request) (sudo.Actor, bool) {
	if !a.enabled {
		return sudo.Actor{}, false
	}
	if a.accounts {
		identity, ok := auth.AccountFromContext(r.Context())
		if !ok || identity.User.ID == "" || a.lookup == nil {
			return sudo.Actor{}, false
		}
		user, ok := a.lookup(identity.User.ID)
		if !ok || !user.IsAdmin() || user.Disabled {
			return sudo.Actor{}, false
		}
		return sudo.Actor{ID: user.ID, Name: user.Username}, true
	}
	if auth.SessionCookie(r) == "" {
		return sudo.Actor{}, false
	}
	return sudo.Actor{ID: "owner", Name: "propriétaire"}, true
}

// sudoAdminOnly rejects anyone the SudoAdmins check does not accept.
func sudoAdminOnly(admins SudoAdmins) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := admins.Admin(r); !ok {
				http.Error(w, "réservé aux administrateurs", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// accountDone returns the channel closed when the signed-in account's
// session ends, in accounts mode.
func accountDone(r *http.Request) (<-chan struct{}, bool) {
	identity, ok := auth.AccountFromContext(r.Context())
	if !ok || identity.Done == nil {
		return nil, false
	}
	return identity.Done, true
}
