package server

import (
	"context"
	"net/http"
	"regexp"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/voice"
	"github.com/LosFurina/tmuxatlas/pkg/ws"
)

var micIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// remoteVoiceHost returns the peer connection when hostID names another host,
// whose agent then owns the virtual microphone. A local or empty host returns nil.
func remoteVoiceHost(opts *Options, hostID string) (*peer.PeerConnection, bool) {
	if hostID == "" || opts.PeerMgr == nil || opts.PeerMgr.IsLocal(hostID) {
		return nil, false
	}
	return opts.PeerMgr.GetPeerConnection(hostID), true
}

// voiceStatus reports whether the browser microphone can feed the given host:
// this host's own microphone, or an agent that advertises voice.
func voiceStatus(opts *Options, local *voice.Mic, hostID string) voice.Status {
	if connection, remote := remoteVoiceHost(opts, hostID); remote {
		supported := connection != nil && connection.Supports(peer.CapabilityVoice)
		return voice.Status{Enabled: supported, Ready: supported}
	}
	if local == nil {
		return voice.Status{}
	}
	return local.Status()
}

// micHandler serves /ws/mic. Without a host, or for this host, audio goes to
// this host's virtual microphone; for another host, it is relayed to its agent,
// because recorders can only hear the audio server of the user they run as.
func micHandler(opts *Options, local *voice.Mic, relay *peer.MicRelay) http.HandlerFunc {
	upgrader := websocket.Upgrader{CheckOrigin: ws.CheckSameOrigin, ReadBufferSize: 4 << 10, WriteBufferSize: 1 << 10}
	return func(w http.ResponseWriter, r *http.Request) {
		connection, remote := remoteVoiceHost(opts, r.URL.Query().Get("host"))
		if !remote {
			if local == nil {
				http.Error(w, "voice is off on this host", http.StatusNotFound)
				return
			}
			ctx, cancel := revocableContext(r)
			defer cancel()
			local.HandleStream(w, r.WithContext(ctx))
			return
		}
		micID := r.URL.Query().Get("id")
		if !micIDPattern.MatchString(micID) {
			http.Error(w, "invalid microphone id", http.StatusBadRequest)
			return
		}
		if connection == nil {
			http.Error(w, "peer not connected", http.StatusBadGateway)
			return
		}
		if !connection.Supports(peer.CapabilityVoice) {
			http.Error(w, "voice is off on this host's agent", http.StatusNotFound)
			return
		}
		browser, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer browser.Close()
		if identity, ok := auth.AccountFromContext(r.Context()); ok && identity.Done != nil {
			stop := make(chan struct{})
			defer close(stop)
			go func() {
				select {
				case <-identity.Done:
					_ = browser.Close()
				case <-stop:
				}
			}()
		}
		if err := relay.Relay(r.Context(), connection, micID, browser); err != nil {
			logrus.WithError(err).WithField("host", connection.HostID).Warn("remote microphone failed")
			_ = browser.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "the agent could not open its microphone"))
		}
	}
}

// revocableContext ends when the request ends or when the signed-in session
// is revoked (logout, account disabled, demoted or deleted).
func revocableContext(r *http.Request) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	if identity, ok := auth.AccountFromContext(r.Context()); ok && identity.Done != nil {
		go func() {
			select {
			case <-identity.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

// remoteMicID passes the tab's microphone to an agent that has voice, so the
// recorders of the session it opens hear that tab.
func remoteMicID(connection *peer.PeerConnection, micID string) string {
	if connection == nil || !connection.Supports(peer.CapabilityVoice) || !micIDPattern.MatchString(micID) {
		return ""
	}
	return micID
}
