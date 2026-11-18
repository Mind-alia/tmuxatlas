package server

import (
	"net/http"

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
//   - accounts: the account's "admin" role;
//   - passkey: the single owner, i.e. any valid session (the auth
//     middleware already ran);
//   - no authentication: nobody, so the sudo terminal is off.
type defaultSudoAdmins struct {
	accounts bool
	enabled  bool
}

func newSudoAdmins(opts *Options) SudoAdmins {
	return defaultSudoAdmins{accounts: opts.AccountsAuth != nil, enabled: opts.AuthEnabled}
}

func (a defaultSudoAdmins) Admin(r *http.Request) (sudo.Actor, bool) {
	if !a.enabled {
		return sudo.Actor{}, false
	}
	if a.accounts {
		identity, ok := auth.AccountFromContext(r.Context())
		if !ok || identity.User.Role != "admin" || identity.User.Disabled {
			return sudo.Actor{}, false
		}
		return sudo.Actor{ID: identity.User.ID, Name: identity.User.Username}, true
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

// sessionDone returns the channel closed when the administrator's browser
// session ends, with accounts as with passkeys.
func sessionDone(r *http.Request) (<-chan struct{}, bool) {
	return auth.SessionDone(r.Context())
}
