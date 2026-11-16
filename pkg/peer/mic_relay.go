package peer

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const (
	micAttachDeadline = 15 * time.Second
	// maxMicFrame bounds one browser microphone message (20 ms is 640 bytes).
	maxMicFrame = 16 << 10
)

// MicRelay carries browser microphones to the agents that own the sessions,
// since an agent's recorders can only hear its own user's audio server:
//
//	browser /ws/mic?host=<agent> -> hub -> mic-open -> agent dials /ws/peer-mic -> agent's virtual mic
type MicRelay struct {
	mu            sync.Mutex
	pending       map[string]*micAttachment
	attachTimeout time.Duration
}

type micAttachment struct {
	hostID     string
	generation uint64
	token      string
	peer       chan *websocket.Conn
}

func NewMicRelay() *MicRelay {
	return &MicRelay{pending: make(map[string]*micAttachment), attachTimeout: micAttachDeadline}
}

// Relay asks the peer to open the tab's microphone and forwards the browser's
// frames to it until either side closes. A close from the agent, such as a
// failed audio setup, is passed on to the browser with its reason.
func (relay *MicRelay) Relay(ctx context.Context, connection *PeerConnection, micID string, browser *websocket.Conn) error {
	if connection == nil {
		return ErrPeerOffline
	}
	if !connection.Supports(CapabilityVoice) {
		return RuntimeError{Code: ErrorCapabilityUnsupported, Message: "the agent has no virtual microphone"}
	}
	streamID := randomPTYSecret()
	attachment := &micAttachment{
		hostID: connection.HostID, generation: connection.Generation,
		token: randomPTYSecret(), peer: make(chan *websocket.Conn, 1),
	}
	relay.mu.Lock()
	relay.pending[streamID] = attachment
	relay.mu.Unlock()
	defer func() {
		relay.mu.Lock()
		delete(relay.pending, streamID)
		relay.mu.Unlock()
	}()

	message, err := NewMessage(MsgMicOpen, MicOpenPayload{
		StreamID: streamID, AttachToken: attachment.token, Generation: connection.Generation, MicID: micID,
	})
	if err != nil {
		return err
	}
	if err := connection.Send(ctx, message); err != nil {
		return err
	}
	var peerWS *websocket.Conn
	select {
	case peerWS = <-attachment.peer:
	case <-time.After(relay.attachTimeout):
		return errors.New("the agent did not open its microphone")
	case <-connection.Done():
		return ErrPeerOffline
	case <-ctx.Done():
		return ctx.Err()
	}
	defer peerWS.Close()
	bridgeMic(browser, peerWS, connection.Done())
	return nil
}

// bridgeMic copies binary frames from the browser to the agent.
func bridgeMic(browser, peerWS *websocket.Conn, peerGone <-chan struct{}) {
	browser.SetReadLimit(maxMicFrame)
	peerWS.SetReadLimit(maxMicFrame)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		// The agent sends nothing: a read returns only when it closes.
		_, _, err := peerWS.ReadMessage()
		code, reason := websocket.CloseGoingAway, "the agent closed the microphone"
		var closeErr *websocket.CloseError
		if errors.As(err, &closeErr) && closeErr.Code != websocket.CloseNormalClosure {
			code, reason = closeErr.Code, closeErr.Text
		}
		_ = browser.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = browser.Close()
	}()
	go func() {
		select {
		case <-peerGone:
			_ = peerWS.Close()
		case <-stop:
		}
	}()
	for {
		messageType, frame, err := browser.ReadMessage()
		if err != nil {
			break
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		if err := peerWS.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			break
		}
	}
	_ = peerWS.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "browser microphone closed"), time.Now().Add(time.Second))
}

// HandlePeerMic accepts the microphone channel an agent opens after mic-open.
func (relay *MicRelay) HandlePeerMic(w http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	streamID, hostID, token := query.Get("stream"), query.Get("host"), query.Get("token")
	generation, err := strconv.ParseUint(query.Get("generation"), 10, 64)
	if streamID == "" || hostID == "" || token == "" || err != nil || generation == 0 {
		http.Error(w, "invalid microphone attachment identity", http.StatusBadRequest)
		return
	}
	relay.mu.Lock()
	attachment := relay.pending[streamID]
	if attachment == nil || attachment.hostID != hostID || attachment.generation != generation || attachment.token != token {
		relay.mu.Unlock()
		logrus.WithField("stream", streamID).Warn("rejected unmatched or late microphone attachment")
		http.Error(w, "unknown microphone attachment", http.StatusNotFound)
		return
	}
	delete(relay.pending, streamID)
	relay.mu.Unlock()
	conn, err := wsUpgrader.Upgrade(w, request, nil)
	if err != nil {
		return
	}
	attachment.peer <- conn
}
