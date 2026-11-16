package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

// parseFingerprints splits a comma- or space-separated list of host
// fingerprints, as in TMUXATLAS_SUDO_PEERS.
func parseFingerprints(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
}

// hostTrust is what the browser needs to warn on untrusted hosts. Only the
// hub and the pinned sudo peers run under accounts that agents and guests do
// not control: anything else may draw a fake password prompt.
type hostTrust struct {
	Hub     string   `json:"hub,omitempty"` // the hub's own host ID, when visible
	Local   bool     `json:"local"`         // the hub's own host is visible, hence trusted
	Trusted []string `json:"trusted"`
}

// trustedHosts lists the trusted hosts among those the account may see.
func trustedHosts(opts *Options, visible func(string) bool) hostTrust {
	trust := hostTrust{Trusted: []string{}}
	if opts.PeerMgr == nil {
		// Single host: the only terminal is the hub's own.
		trust.Local = visible("")
		return trust
	}
	local := opts.PeerMgr.LocalID()
	if visible(local) {
		trust.Hub, trust.Local = local, true
		trust.Trusted = append(trust.Trusted, local)
	}
	for _, id := range parseFingerprints(opts.SudoPeers) {
		if id != local && visible(id) {
			trust.Trusted = append(trust.Trusted, id)
		}
	}
	return trust
}

func handleHostTrust(opts *Options, policy *accountPolicy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		visible := func(string) bool { return true }
		if identity, ok := auth.AccountFromContext(r.Context()); ok && !identity.User.IsAdmin() {
			visible = policy.hostFilter(identity.User)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(trustedHosts(opts, visible))
	}
}
