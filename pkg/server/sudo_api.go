package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// sudoAdminView is a request as administrators see it, with the peers it
// may run on.
type sudoAdminView struct {
	sudo.Request
	Original string `json:"original"`
}

func adminView(request sudo.Request) sudoAdminView {
	return sudoAdminView{Request: request, Original: request.Original()}
}

func decodeSudoBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, sudoLocalBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeSudoError(w, sudo.ErrInvalidText)
		return false
	}
	return true
}

// registerSudoAPI adds the administrator routes. Callers wrap them with
// authentication, Origin and CSRF checks; every handler re-checks admin.
func (s *sudoService) registerSudoAPI(r chi.Router, mutations chi.Router) {
	admin := sudoAdminOnly(s.admins)
	actor := func(r *http.Request) sudo.Actor {
		a, _ := s.admins.Admin(r)
		return a
	}
	respond := func(w http.ResponseWriter, request sudo.Request, err error) {
		if err != nil {
			writeSudoError(w, err)
			return
		}
		writeSudoJSON(w, http.StatusOK, adminView(request))
	}
	r.With(admin).Get("/sudo/requests", func(w http.ResponseWriter, _ *http.Request) {
		requests := s.store.List()
		views := make([]sudoAdminView, 0, len(requests))
		for _, request := range requests {
			views = append(views, adminView(request))
		}
		writeSudoJSON(w, http.StatusOK, views)
	})
	r.With(admin).Get("/sudo/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		request, ok := s.store.Get(chi.URLParam(r, "id"))
		if !ok {
			writeSudoError(w, sudo.ErrNotFound)
			return
		}
		writeSudoJSON(w, http.StatusOK, adminView(request))
	})
	r.With(admin).Get("/sudo/peers", func(w http.ResponseWriter, _ *http.Request) {
		peers := s.Peers()
		if peers == nil {
			peers = []sudoPeerView{}
		}
		writeSudoJSON(w, http.StatusOK, peers)
	})
	mutations.With(admin).Post("/sudo/requests/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version int    `json:"version"`
			Command string `json:"command"`
		}
		if !decodeSudoBody(w, r, &body) {
			return
		}
		request, err := s.store.Edit(chi.URLParam(r, "id"), body.Version, body.Command, actor(r))
		respond(w, request, err)
	})
	mutations.With(admin).Post("/sudo/requests/{id}/refuse", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Note string `json:"note"`
		}
		if !decodeSudoBody(w, r, &body) {
			return
		}
		request, err := s.store.Refuse(chi.URLParam(r, "id"), actor(r), body.Note)
		respond(w, request, err)
	})
	mutations.With(admin).Post("/sudo/requests/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Version int    `json:"version"`
			Peer    string `json:"peer"`
		}
		if !decodeSudoBody(w, r, &body) {
			return
		}
		current, ok := s.store.Get(chi.URLParam(r, "id"))
		if !ok {
			writeSudoError(w, sudo.ErrNotFound)
			return
		}
		if _, err := s.terminalPeer(body.Peer, current.Origin.HostID); err != nil {
			writeSudoJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "peer"})
			return
		}
		_, grant, err := s.store.Approve(current.ID, body.Version, body.Peer, actor(r))
		if err != nil {
			writeSudoError(w, err)
			return
		}
		writeSudoJSON(w, http.StatusOK, grant)
	})
	mutations.With(admin).Post("/sudo/requests/{id}/done", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Note string `json:"note"`
		}
		if !decodeSudoBody(w, r, &body) {
			return
		}
		request, err := s.store.Done(chi.URLParam(r, "id"), actor(r), body.Note)
		respond(w, request, err)
	})
}

var sudoUpgrader = websocket.Upgrader{
	// The route's Origin middleware already required an exact public
	// origin; a missing Origin never reaches this point.
	CheckOrigin:     func(r *http.Request) bool { return r.Header.Get("Origin") != "" },
	ReadBufferSize:  1024,
	WriteBufferSize: 16 << 10,
}

func sudoWSError(conn *websocket.Conn, message string) {
	data, _ := json.Marshal(map[string]string{"type": "error", "message": message})
	_ = conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, ""), time.Now().Add(time.Second))
}

// HandleTerminal serves /ws/sudo. The grant travels in the first frame so it
// never appears in a URL or an access log.
func (s *sudoService) HandleTerminal(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.admins.Admin(r)
	if !ok {
		http.Error(w, "réservé aux administrateurs", http.StatusForbidden)
		return
	}
	browser, err := sudoUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer browser.Close()
	browser.SetReadLimit(4096)
	_ = browser.SetReadDeadline(time.Now().Add(10 * time.Second))
	var open struct {
		Type  string `json:"type"`
		Grant string `json:"grant"`
		Cols  uint16 `json:"cols"`
		Rows  uint16 `json:"rows"`
	}
	if err := browser.ReadJSON(&open); err != nil || open.Type != "open" || open.Grant == "" {
		sudoWSError(browser, "première trame invalide")
		return
	}
	_ = browser.SetReadDeadline(time.Time{})
	request, err := s.store.Redeem(open.Grant, actor)
	open.Grant = ""
	if err != nil {
		sudoWSError(browser, err.Error())
		return
	}
	log := logrus.WithFields(logrus.Fields{"request": request.ID, "version": request.Version, "approver": actor.Name})
	connection, err := s.terminalPeer(request.Peer, request.Origin.HostID)
	if err != nil {
		_, _ = s.store.Reopen(request.ID, err.Error())
		sudoWSError(browser, err.Error())
		return
	}
	if identity, ok := sessionDone(r); ok {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-identity:
				_ = browser.Close()
			case <-stop:
			}
		}()
	}
	target := peer.SessionTarget{HostID: request.Peer, Session: peer.SudoSession(request.ID)}
	owner, err := s.opts.PTYRelay.RegisterPending(connection, target, browser)
	if err != nil {
		_, _ = s.store.Reopen(request.ID, "peer sudo hors ligne")
		sudoWSError(browser, "peer sudo hors ligne")
		return
	}
	defer owner.Teardown("sudo-handler-exit")
	defer s.track(request.ID, func() { owner.Teardown("sudo-decided") })()
	message, _ := peer.NewMessage(peer.MsgSudoOpen, peer.SudoOpenPayload{
		StreamID: owner.StreamID, AttachToken: owner.AttachToken, Generation: connection.Generation,
		RequestID: request.ID, Version: request.Version, Command: request.Command,
		Approver: actor.Name, Origin: request.Origin, Cols: open.Cols, Rows: open.Rows,
	})
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	err = connection.Send(ctx, message)
	cancel()
	if err != nil {
		_, _ = s.store.Reopen(request.ID, "peer sudo injoignable")
		sudoWSError(browser, "peer sudo injoignable")
		return
	}
	log.Info("sudo terminal requested")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-owner.Ready():
			ready, _ := json.Marshal(map[string]string{"type": "ready"})
			_ = browser.WriteMessage(websocket.TextMessage, ready)
			owner.Bridge()
			return
		case <-ticker.C:
			// The peer refused before attaching (audit, busy…).
			if current, ok := s.store.Get(request.ID); ok && current.Status != sudo.StatusRunning {
				sudoWSError(browser, current.Note)
				return
			}
		case <-timeout.C:
			_, _ = s.store.Reopen(request.ID, "le peer sudo n'a pas ouvert le terminal")
			sudoWSError(browser, "le peer sudo n'a pas ouvert le terminal")
			return
		case <-connection.Done():
			_, _ = s.store.Reopen(request.ID, "peer sudo déconnecté")
			sudoWSError(browser, "peer sudo déconnecté")
			return
		}
	}
}
