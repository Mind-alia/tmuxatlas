package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm"
)

// maxSudoResultJSON keeps a result below the hub's control read limit.
const maxSudoResultJSON = 200 << 10

// sudoTerminal runs approved commands on the sudo peer. It exists only
// when the agent was started with --sudo-terminal.
type sudoTerminal struct {
	client *Client
	runner *sudoterm.Runner
	audit  *sudoterm.Audit
	user   string
}

// EnableSudoTerminal makes this peer announce the sudo-terminal capability
// and accept sudo-open requests from its hub. Call it before Run.
func (c *Client) EnableSudoTerminal(runner *sudoterm.Runner, audit *sudoterm.Audit) {
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	c.sudoTerminal = &sudoTerminal{client: c, runner: runner, audit: audit, user: name}
}

func (c *Client) helloCapabilities() []string {
	capabilities := append([]string(nil), RuntimeCapabilities...)
	capabilities = append(capabilities, CapabilitySudoRequests)
	if c.sudoTerminal != nil {
		capabilities = append(capabilities, CapabilitySudoTerminal)
	}
	return capabilities
}

func (t *sudoTerminal) record(entry sudoterm.AuditEntry, open SudoOpenPayload) error {
	entry.RequestID, entry.Version, entry.Command = open.RequestID, open.Version, open.Command
	entry.Approver, entry.UnixUser = open.Approver, t.user
	entry.OriginHost, entry.OriginSession = open.Origin.HostName, open.Origin.Session
	if entry.OriginHost == "" {
		entry.OriginHost = open.Origin.HostID
	}
	return t.audit.Append(entry)
}

func (t *sudoTerminal) report(result SudoResultPayload) {
	for {
		data, err := json.Marshal(result)
		if err != nil {
			return
		}
		if len(data) <= maxSudoResultJSON || result.Output == "" {
			break
		}
		result.Output = result.Output[:len(result.Output)/2]
		result.Truncated = true
	}
	if message, err := NewMessage(MsgSudoResult, result); err == nil {
		if err := t.client.sendControl(message); err != nil {
			logrus.WithField("request", result.RequestID).Warn("could not report sudo result to hub")
		}
	}
}

// Open runs one sudo-open request end to end.
func (t *sudoTerminal) Open(open SudoOpenPayload) {
	log := logrus.WithFields(logrus.Fields{"request": open.RequestID, "version": open.Version})
	fail := func(reason string) {
		_ = t.record(sudoterm.AuditEntry{Event: "refused", Reason: reason}, open)
		t.report(SudoResultPayload{RequestID: open.RequestID, Version: open.Version, StreamID: open.StreamID, Error: reason})
		log.WithField("reason", reason).Warn("sudo terminal refused")
	}
	if open.StreamID == "" || open.AttachToken == "" || open.RequestID == "" || open.Version <= 0 ||
		open.Generation == 0 || open.Generation != t.client.RuntimeGeneration() {
		log.Warn("rejected invalid or stale sudo-open")
		return
	}
	if err := sudo.ValidateCommand(open.Command); err != nil {
		fail("commande invalide")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := t.runner.Preflight(ctx)
	cancel()
	if err != nil {
		fail(err.Error())
		return
	}
	if err := t.record(sudoterm.AuditEntry{Event: "open"}, open); err != nil {
		fail("journal d'audit inaccessible")
		return
	}
	conn, err := t.connect(open)
	if err != nil {
		fail("canal du terminal indisponible")
		return
	}
	screen := &wsScreen{conn: conn}
	run, err := t.runner.Start(open.Command, open.Cols, open.Rows, screen)
	if err != nil {
		_ = conn.Close()
		fail(err.Error())
		return
	}
	log.Info("sudo terminal opened")

	go func() {
		var inputSequence, controlSequence PTYSequence
		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				run.Stop("terminal fermé côté navigateur")
				return
			}
			switch messageType {
			case websocket.BinaryMessage:
				frame, err := DecodePTYDataFrame(data)
				if err != nil || frame.Direction != PTYDataInput {
					run.Stop("trame invalide")
					return
				}
				if duplicate, err := inputSequence.Accept(frame.Sequence); err != nil {
					run.Stop("trame invalide")
					return
				} else if !duplicate {
					_ = run.Write(frame.Payload)
					sudo.Wipe(frame.Payload)
				}
			case websocket.TextMessage:
				frame, err := DecodePTYControlFrame(data)
				if err != nil {
					run.Stop("trame invalide")
					return
				}
				if duplicate, err := controlSequence.Accept(frame.Sequence); err != nil || duplicate {
					continue
				}
				switch frame.Type {
				case "resize":
					_ = run.Resize(frame.Cols, frame.Rows)
				case "close", "error":
					run.Stop("terminal fermé")
					return
				}
			}
		}
	}()

	result := run.Wait()
	screen.close("sudo-finished")
	code := result.ExitCode
	_ = t.record(sudoterm.AuditEntry{
		Event: "finished", ExitCode: &code, DurationMS: result.Duration.Milliseconds(),
		OutputBytes: len(result.Output), Truncated: result.Truncated, Redactions: result.Redactions, Reason: result.Error,
	}, open)
	t.report(SudoResultPayload{
		RequestID: open.RequestID, Version: open.Version, StreamID: open.StreamID, Started: true,
		ExitCode: result.ExitCode, Output: string(result.Output), Truncated: result.Truncated,
		Redactions: result.Redactions, DurationMS: result.Duration.Milliseconds(), Error: result.Error,
	})
	log.WithField("summary", sudoterm.Describe(result)).Info("sudo terminal closed")
}

func (t *sudoTerminal) connect(open SudoOpenPayload) (*websocket.Conn, error) {
	u, err := hubWebSocketURL(t.client.HubURL(), "/ws/peer-pty")
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("stream", open.StreamID)
	query.Set("host", t.client.peerMgr.LocalID())
	query.Set("session", SudoSession(open.RequestID))
	query.Set("generation", strconv.FormatUint(open.Generation, 10))
	query.Set("token", open.AttachToken)
	u.RawQuery = query.Encode()
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("connect sudo data channel: %w", err)
	}
	conn.SetReadLimit(maxPTYFrameData + ptyDataHeaderSize)
	return conn, nil
}

// wsScreen sends the terminal's output to the hub relay as PTY frames.
type wsScreen struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	output   uint64
	controls uint64
	closed   bool
}

func (s *wsScreen) Output(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	for len(data) > 0 {
		chunk := data[:min(len(data), maxPTYFrameData)]
		data = data[len(chunk):]
		s.output++
		frame, err := EncodePTYDataFrame(PTYDataOutput, s.output, chunk)
		if err != nil {
			return err
		}
		if err := s.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			return err
		}
	}
	return nil
}

func (s *wsScreen) control(kind, reason string) error {
	s.controls++
	frame, err := EncodePTYControlFrame(PTYControlFrame{Version: PTYFrameVersion, Type: kind, Sequence: s.controls, Reason: reason})
	if err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.TextMessage, frame)
}

func (s *wsScreen) PasswordMode(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	state := "on"
	if on {
		state = "off" // echo is off while a password is read
	}
	return s.control("echo", state)
}

func (s *wsScreen) close(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_ = s.control("close", reason)
	s.closed = true
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = s.conn.Close()
}
