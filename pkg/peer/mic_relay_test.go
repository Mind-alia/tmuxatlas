package peer

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeMicAgent answers mic-open like an agent: it dials /ws/peer-mic on hub and
// hands the connection to the test.
func fakeMicAgent(t *testing.T, hub *httptest.Server, hostID string, opened chan<- MicOpenPayload, agent chan<- *websocket.Conn) func(*Message) error {
	return func(message *Message) error {
		if message.Type != MsgMicOpen {
			return nil
		}
		var payload MicOpenPayload
		if err := json.Unmarshal(message.Payload, &payload); err != nil {
			return err
		}
		opened <- payload
		go func() {
			query := url.Values{
				"stream": {payload.StreamID}, "host": {hostID}, "token": {payload.AttachToken},
				"generation": {strconv.FormatUint(payload.Generation, 10)},
			}
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/ws/peer-mic?"+query.Encode(), nil)
			if err != nil {
				t.Errorf("agent dial: %v", err)
				return
			}
			agent <- conn
		}()
		return nil
	}
}

func TestMicRelayCarriesBrowserFramesToTheAgentAndItsCloseReasonBack(t *testing.T) {
	relay := NewMicRelay()
	hub := httptest.NewServer(http.HandlerFunc(relay.HandlePeerMic))
	t.Cleanup(hub.Close)
	opened := make(chan MicOpenPayload, 1)
	agent := make(chan *websocket.Conn, 1)
	connection := newPeerConnection(t.Context(), "agents-host", 7, RuntimeCapabilities, "instance", 4,
		fakeMicAgent(t, hub, "agents-host", opened, agent), nil)
	connection.Start()
	browserClient, browserServer := websocketPair(t)

	relayDone := make(chan error, 1)
	go func() { relayDone <- relay.Relay(t.Context(), connection, "tab-0123456789", browserServer) }()

	payload := <-opened
	if payload.MicID != "tab-0123456789" || payload.Generation != 7 || payload.StreamID == "" || payload.AttachToken == "" {
		t.Fatalf("mic-open payload = %#v", payload)
	}
	agentConn := <-agent
	defer agentConn.Close()

	frame := []byte{1, 2, 3, 4}
	if err := browserClient.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	agentConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	messageType, received, err := agentConn.ReadMessage()
	if err != nil || messageType != websocket.BinaryMessage || string(received) != string(frame) {
		t.Fatalf("agent received type=%d data=%v err=%v", messageType, received, err)
	}

	_ = agentConn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "start PulseAudio: no daemon"))
	browserClient.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = browserClient.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseInternalServerErr || closeErr.Text != "start PulseAudio: no daemon" {
		t.Fatalf("browser close = %v", err)
	}
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatalf("relay error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay leaked after the agent closed")
	}
	if len(relay.pending) != 0 {
		t.Fatal("attachment left pending")
	}
}

func TestMicRelayRejectsAgentsWithoutVoiceAndForeignAttachments(t *testing.T) {
	relay := NewMicRelay()
	withoutVoice := newPeerConnection(t.Context(), "host", 1, helloCapabilities(false), "instance", 1,
		func(*Message) error { t.Fatal("mic-open sent to an agent without voice"); return nil }, nil)
	if err := relay.Relay(t.Context(), withoutVoice, "tab-0123456789", nil); err == nil {
		t.Fatal("relay accepted an agent without voice")
	}

	hub := httptest.NewServer(http.HandlerFunc(relay.HandlePeerMic))
	t.Cleanup(hub.Close)
	relay.pending["stream"] = &micAttachment{hostID: "host", generation: 2, token: "secret", peer: make(chan *websocket.Conn, 1)}
	for name, query := range map[string]string{
		"wrong token":      "stream=stream&host=host&generation=2&token=guess",
		"wrong host":       "stream=stream&host=other&generation=2&token=secret",
		"stale generation": "stream=stream&host=host&generation=1&token=secret",
		"unknown stream":   "stream=other&host=host&generation=2&token=secret",
	} {
		t.Run(name, func(t *testing.T) {
			_, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hub.URL, "http")+"/?"+query, nil)
			if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
				t.Fatalf("attachment accepted: err=%v", err)
			}
		})
	}
}

func TestMicRelayTimesOutWhenTheAgentNeverAttaches(t *testing.T) {
	relay := NewMicRelay()
	relay.attachTimeout = 20 * time.Millisecond
	connection := newPeerConnection(t.Context(), "host", 1, RuntimeCapabilities, "instance", 1,
		func(*Message) error { return nil }, nil)
	connection.Start()
	if err := relay.Relay(t.Context(), connection, "tab-0123456789", nil); err == nil {
		t.Fatal("relay waited forever")
	}
	if len(relay.pending) != 0 {
		t.Fatal("attachment left pending")
	}
}

func TestHelloOffersVoiceOnlyWithAMicrophone(t *testing.T) {
	has := func(capabilities []string) bool {
		for _, capability := range capabilities {
			if capability == CapabilityVoice {
				return true
			}
		}
		return false
	}
	if has(helloCapabilities(false)) || !has(helloCapabilities(true)) {
		t.Fatal("voice capability does not follow the microphone")
	}
	if len(helloCapabilities(true)) != len(RuntimeCapabilities) {
		t.Fatal("hello with voice must offer every runtime capability")
	}
}
