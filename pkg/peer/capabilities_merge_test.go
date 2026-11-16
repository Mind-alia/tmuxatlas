package peer

import (
	"slices"
	"testing"

	"github.com/gorilla/websocket"
)

type fakeVoice struct{}

func (fakeVoice) ServeConn(*websocket.Conn, string) {}
func (fakeVoice) UseSession(string, string)         {}

// The agent's capabilities add up: files-v1 always, voice with a
// microphone, sudo-requests always, sudo-terminal with --sudo-terminal; the
// hub accepts all of them.
func TestAgentCapabilitiesAddUp(t *testing.T) {
	for _, tc := range []struct {
		voice, sudo bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		client := &Client{}
		if tc.voice {
			client.SetVoice(fakeVoice{})
		}
		if tc.sudo {
			client.sudoTerminal = &sudoTerminal{client: client}
		}
		got := client.helloCapabilities()
		want := map[string]bool{
			CapabilityFiles: true, CapabilitySudoRequests: true, CapabilitySessionOwnerName: true,
			CapabilityVoice: tc.voice, CapabilitySudoTerminal: tc.sudo,
		}
		for capability, present := range want {
			if slices.Contains(got, capability) != present {
				t.Errorf("voice=%v sudo=%v: %s present = %v, want %v", tc.voice, tc.sudo, capability, !present, present)
			}
		}
		if accepted := intersectCapabilities(got, HubCapabilities); len(accepted) != len(got) {
			t.Errorf("voice=%v sudo=%v: hub accepts %v of %v", tc.voice, tc.sudo, accepted, got)
		}
	}
}
