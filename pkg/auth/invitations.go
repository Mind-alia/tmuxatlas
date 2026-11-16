package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
)

// UpdateAccess changes an account's role, host grants or Linux account name.
// A demotion or a removed host ends the account's sessions, which closes its
// WebSockets; added hosts apply from the next request.
func (a *AccountsAuth) UpdateAccess(id, actor string, role *string, hosts *[]string, unixUser *string, allow func(string) error) (accounts.User, error) {
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	users, err := a.Store.List()
	if err != nil {
		return accounts.User{}, err
	}
	var before *accounts.User
	for i := range users {
		if users[i].ID == id {
			before = &users[i]
		}
	}
	if before == nil {
		return accounts.User{}, accounts.ErrNotFound
	}
	user := *before
	if role != nil {
		if user, err = a.Store.SetRole(id, actor, *role); err != nil {
			return accounts.User{}, err
		}
	}
	if hosts != nil || unixUser != nil {
		nextHosts, nextUnix := user.Hosts, user.UnixUser
		if hosts != nil {
			nextHosts = *hosts
		}
		if unixUser != nil {
			nextUnix = *unixUser
		}
		if user, err = a.Store.SetAccess(id, nextHosts, nextUnix, allow); err != nil {
			return accounts.User{}, err
		}
	}
	lost := before.IsAdmin() && !user.IsAdmin()
	for _, host := range before.Hosts {
		if !slices.Contains(user.Hosts, host) {
			lost = true
		}
	}
	if lost {
		a.Sessions.RevokeAccount(id, "")
	}
	return user, nil
}

// CancelInvitation removes a pending invitation link.
func (a *AccountsAuth) CancelInvitation(id string) error {
	return a.Store.CancelInvitation(id)
}

// InspectInvitation tells the invitation page which account a link is for.
// The token travels in the request body: in the browser it stays in the URL
// fragment, which is never sent to the server or written to access logs.
func (a *AccountsAuth) InspectInvitation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	key := "invite:" + ClientIP(r)
	if a.blocked(key) {
		writeError(w, http.StatusTooManyRequests, "trop de tentatives")
		return
	}
	user, err := a.Store.InspectInvitation(request.Token)
	if err != nil {
		a.failed(key)
		writeError(w, http.StatusNotFound, accounts.ErrInvitationInvalid.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"username": user.Username, "expires_at": user.Invite.ExpiresAt, "has_password": user.PasswordHash != "",
	})
}

// AcceptInvitation sets the password chosen by the invited person and consumes
// the link. Existing sessions of the account end, as with a password reset.
func (a *AccountsAuth) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeError(w, http.StatusBadRequest, "requête invalide")
		return
	}
	key := "invite:" + ClientIP(r)
	if a.blocked(key) {
		writeError(w, http.StatusTooManyRequests, "trop de tentatives")
		return
	}
	hash, err := accounts.HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.lifecycle.Lock()
	defer a.lifecycle.Unlock()
	user, err := a.Store.AcceptInvitation(request.Token, hash)
	if err != nil {
		if errors.Is(err, accounts.ErrInvitationInvalid) {
			a.failed(key)
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "erreur interne")
		return
	}
	a.Sessions.RevokeAccount(user.ID, "")
	w.WriteHeader(http.StatusNoContent)
}
