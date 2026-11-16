package peer

import (
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

// A peer may run under another person's Unix account: the hub must never send
// it the sessions of other hosts, neither on connect nor on later changes.
func TestHubDoesNotForwardOtherHostsToPeer(t *testing.T) {
	conn, manager, _ := newNegotiationTestPeer(t, time.Second)
	hello, err := NewMessage(MsgHello, HelloPayload{
		MinVersion: RuntimeProtocolMin, MaxVersion: RuntimeProtocolMax,
		Capabilities: RuntimeCapabilities, BuildVersion: "test",
		AgentInstanceID: "isolation-test-instance",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	var ack Message
	if err := conn.ReadJSON(&ack); err != nil || ack.Type != MsgHelloAck {
		t.Fatalf("hello ack=%q err=%v", ack.Type, err)
	}
	manager.RegisterPeer("prod-host", "prod", "", nil)
	manager.UpdatePeerSessions("prod-host", []*tmux.Session{{Name: "secret-prod"}})

	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			return // timeout: nothing was forwarded
		}
		if msg.Type == MsgPeerState {
			t.Fatalf("hub forwarded other hosts' state: %s", msg.Payload)
		}
	}
}
