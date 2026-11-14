package ws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/LosFurina/tmuxatlas/pkg/clipboardimage"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/activity"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// PTYTerminalHandler handles WebSocket connections backed by a PTY running tmux attach
type PTYTerminalHandler struct {
	tmuxPath        string
	activityTracker *activity.Tracker

	// OnSessionUse reports that a browser tab, identified by the id of its
	// microphone, is working in a tmux session.
	OnSessionUse func(session, micID string)

	// StartDictation transcribes a tab's microphone for an agent without
	// dictation of its own; nil disables it.
	// partial receives the transcript while the person speaks.
	StartDictation func(ctx context.Context, session, micID string, partial func(string)) (DictationTake, error)
}

// DictationTake is one dictation started by StartDictation.
type DictationTake interface {
	// Finish stops recording and returns the transcript, "" when nothing was heard.
	Finish(ctx context.Context) (string, error)
	// Cancel stops recording and drops the transcript.
	Cancel()
}

// NewPTYTerminalHandler creates a new PTY-based terminal handler
func NewPTYTerminalHandler(tmuxPath string, activityTracker *activity.Tracker) *PTYTerminalHandler {
	return &PTYTerminalHandler{
		tmuxPath:        tmuxPath,
		activityTracker: activityTracker,
	}
}

// resizeMsg is the JSON control message for resize events
type resizeMsg struct {
	Type  string `json:"type"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
	Data  string `json:"data"`
	Final bool   `json:"final"`
	On    bool   `json:"on"`
	// Action and Submit drive a dictation: start, stop (submitting the
	// transcript or not) or cancel.
	Action string `json:"action"`
	Submit bool   `json:"submit"`
}

// HandleSession handles a WebSocket connection for an entire tmux session via PTY.
// Query params: name=<session>, cols=<cols>, rows=<rows>
func (h *PTYTerminalHandler) HandleSession(w http.ResponseWriter, r *http.Request) {
	sessionName := r.URL.Query().Get("name")
	if sessionName == "" || tmux.IsInternalSession(sessionName) {
		http.Error(w, "missing session name", http.StatusBadRequest)
		return
	}

	cols, _ := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 16)
	rows, _ := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 16)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logrus.WithError(err).Warn("session ws upgrade failed")
		return
	}
	defer conn.Close()
	stopRevocation := closeOnAccountRevocation(conn, r)
	defer stopRevocation()
	conn.SetReadLimit(64 << 10)
	conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					conn.Close()
					return
				}
			case <-stopPing:
				return
			}
		}
	}()

	log := logrus.WithField("session", sessionName)
	log.Info("session ws client connected")

	micID := r.URL.Query().Get("mic")
	useSession := func() {
		if h.OnSessionUse != nil && micID != "" {
			h.OnSessionUse(sessionName, micID)
		}
	}
	useSession()

	// Spawn tmux attach in a PTY
	ptySess, err := tmux.NewPTYSession(h.tmuxPath, sessionName, uint16(cols), uint16(rows))
	if err != nil {
		log.WithError(err).Error("failed to start PTY session")
		conn.WriteMessage(websocket.TextMessage, []byte("\r\n[TmuxAtlas: failed to attach to session]\r\n"))
		return
	}
	defer ptySess.Close()

	var writeMu sync.Mutex
	var images clipboardimage.Receiver
	hold := newVoiceHold(ptySess)
	claim := newSizeClaim(ptySess.Claim)
	defer claim.stop()
	dictation := &dictationState{}
	defer dictation.cancel()
	sendJSON := func(value any) {
		data, _ := json.Marshal(value)
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}
	sendResult := func(err error) {
		message := ""
		if err != nil {
			message = err.Error()
		}
		data, _ := json.Marshal(map[string]string{"type": "image-result", "error": message})
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}

	// Read goroutine: PTY → WebSocket (binary messages)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptySess.Read(buf)
			if err != nil {
				return
			}
			// Track activity
			if h.activityTracker != nil {
				h.activityTracker.Record(sessionName, n)
			}
			writeMu.Lock()
			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	// Write goroutine: WebSocket → PTY
	// Text messages = JSON control, Binary messages = terminal I/O
	for {
		msgType, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Debug("session ws read error")
			}
			break
		}

		switch msgType {
		case websocket.TextMessage:
			var msg resizeMsg
			if err := json.Unmarshal(message, &msg); err != nil {
				log.WithError(err).Debug("invalid control message")
				continue
			}
			if msg.Type == "image" {
				path, err := images.Add(msg.Data, msg.Final)
				if err == nil && path != "" {
					_, err = ptySess.Write([]byte("\x1b[200~" + path + "\x1b[201~"))
				}
				if err != nil || path != "" {
					sendResult(err)
				}
			}
			if msg.Type == "dictation" {
				dictation.handle(h, msg, sessionName, micID, ptySess, sendJSON)
			}
			if msg.Type == "voice-hold" {
				if msg.On {
					useSession()
					hold.Start()
				} else {
					hold.Stop()
				}
			}
			if msg.Type == "use" {
				claim.request()
			}
			if msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				if err := ptySess.Resize(msg.Cols, msg.Rows); err != nil {
					log.WithError(err).Debug("resize failed")
				}
			}

		case websocket.BinaryMessage:
			useSession()
			if _, err := ptySess.Write(message); err != nil {
				log.WithError(err).Debug("PTY write failed")
				break
			}
		}
	}

	hold.Stop()
	ptySess.Close()
	<-done
	log.Info("session ws client disconnected")
}

// dictationState holds the dictation of one Terminal connection. generation
// changes with each start, stop and cancel, so a dictation that finishes
// starting after one of them is cancelled instead of left recording.
type dictationState struct {
	mu         sync.Mutex
	take       DictationTake
	generation int
}

// takeOver removes the current dictation and invalidates any start in progress.
func (d *dictationState) takeOver() DictationTake {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation++
	previous := d.take
	d.take = nil
	return previous
}

func (d *dictationState) cancel() {
	if take := d.takeOver(); take != nil {
		take.Cancel()
	}
}

func (d *dictationState) begin() int {
	d.cancel()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.generation
}

// started keeps take if nothing happened since begin, and cancels it otherwise.
func (d *dictationState) started(generation int, take DictationTake) bool {
	d.mu.Lock()
	current := generation == d.generation
	if current {
		d.take = take
	}
	d.mu.Unlock()
	if !current {
		take.Cancel()
	}
	return current
}

// handle runs a dictation message off the read loop, since starting the
// transcribing agent can take seconds. The transcript is typed in the pane the
// Terminal shows, and Enter follows when the message asks to submit it.
func (d *dictationState) handle(h *PTYTerminalHandler, msg resizeMsg, session, micID string,
	pty io.Writer, send func(any)) {
	reply := func(err error) {
		message := ""
		if err != nil {
			message = err.Error()
		}
		send(map[string]string{"type": "dictation", "action": msg.Action, "error": message})
	}
	switch msg.Action {
	case "start":
		if h.StartDictation == nil || micID == "" {
			reply(errDictationUnavailable)
			return
		}
		generation := d.begin()
		go func() {
			partial := func(text string) {
				send(map[string]string{"type": "dictation", "action": "partial", "text": text})
			}
			take, err := h.StartDictation(context.Background(), session, micID, partial)
			if err == nil && !d.started(generation, take) {
				err = errNoDictation
			}
			reply(err)
		}()
	case "stop":
		take := d.takeOver()
		if take == nil {
			reply(errNoDictation)
			return
		}
		go func() {
			text, err := take.Finish(context.Background())
			if err == nil && text == "" {
				err = errNothingTranscribed
			}
			if err == nil {
				_, err = pty.Write([]byte(sanitizeTranscript(text)))
			}
			if err == nil && msg.Submit {
				// Let the agent take the text in before Enter submits it.
				time.Sleep(150 * time.Millisecond)
				_, err = pty.Write([]byte("\r"))
			}
			reply(err)
		}()
	case "cancel":
		d.cancel()
	}
}

var (
	errDictationUnavailable = dictationError("La dictée n’est pas disponible pour ce terminal.")
	errNoDictation          = dictationError("Aucune dictée en cours.")
	errNothingTranscribed   = dictationError("Claude n’a rien transcrit. Rien n’a été envoyé. Vérifiez que la dictée de Claude est activée pour ce compte (/voice hold).")
)

type dictationError string

func (e dictationError) Error() string { return string(e) }

// sanitizeTranscript keeps a transcript to text: no control character can
// reach the pane as a key press.
func sanitizeTranscript(text string) string {
	var out []rune
	for _, r := range text {
		if r == '\t' || r == '\n' {
			r = ' '
		}
		if r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0) {
			out = append(out, r)
		}
	}
	return string(out)
}

// sizeClaim makes a tab's tmux client the one its windows take their size
// from when the person turns to that tab. Requests run one at a time off the
// read loop, and a burst of them collapses into a single tmux call.
type sizeClaim struct {
	pending chan struct{}
	done    chan struct{}
}

func newSizeClaim(claim func() error) *sizeClaim {
	c := &sizeClaim{pending: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		for {
			select {
			case <-c.done:
				return
			case <-c.pending:
				if err := claim(); err != nil {
					logrus.WithError(err).Debug("could not give the tab its size")
				}
			}
		}
	}()
	return c
}

func (c *sizeClaim) request() {
	select {
	case c.pending <- struct{}{}:
	default:
	}
}

func (c *sizeClaim) stop() { close(c.done) }
