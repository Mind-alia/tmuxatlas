package peer

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/activity"
	"github.com/LosFurina/tmuxatlas/pkg/clipboardimage"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm"
	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

type PTYManager struct {
	mu       sync.RWMutex
	sessions map[string]*ActivePTY
	tmuxPath string
	activity *activity.Tracker
	client   *Client
	openPTY  func(string, string, uint16, uint16) (ptyDevice, error)
	// paneTTY finds the device of a session's active pane, to warn the
	// browser when it reads a password. Nil disables the warning.
	paneTTY func(string, string) (string, error)
}

type ptyDevice interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(uint16, uint16) error
	Close()
}

type ActivePTY struct {
	StreamID string
	Target   SessionTarget
	PTY      ptyDevice
	HubWS    *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup
}

func NewPTYManager(tmuxPath string, actTracker *activity.Tracker, client *Client) *PTYManager {
	manager := &PTYManager{
		sessions: make(map[string]*ActivePTY),
		tmuxPath: tmuxPath,
		activity: actTracker,
		client:   client,
	}
	manager.openPTY = func(path, session string, cols, rows uint16) (ptyDevice, error) {
		return tmux.NewPTYSession(path, session, cols, rows)
	}
	manager.paneTTY = tmux.ActivePaneTTY
	return manager
}

func (pm *PTYManager) Open(request PTYOpenPayload) {
	log := logrus.WithFields(logrus.Fields{"stream": request.StreamID, "session": request.Target.Session})
	if request.StreamID == "" || request.AttachToken == "" || request.Generation == 0 ||
		request.Cols == 0 || request.Rows == 0 || request.Target.Validate() != nil ||
		request.Target.Session == tmux.ControlSessionName() ||
		request.Target.HostID != pm.client.peerMgr.LocalID() ||
		request.Generation != pm.client.RuntimeGeneration() ||
		(pm.client.peerMgr.localMgr != nil && !pm.client.peerMgr.HasSession(request.Target.HostID, request.Target.Session)) {
		log.Warn("rejected invalid or stale PTY open")
		return
	}
	ptySession, err := pm.openPTY(pm.tmuxPath, request.Target.Session, request.Cols, request.Rows)
	if err != nil {
		log.WithError(err).Error("failed to spawn PTY")
		return
	}
	hubWS, err := pm.connectPTYWebSocket(request)
	if err != nil {
		log.WithError(err).Error("failed to attach PTY data channel")
		ptySession.Close()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &ActivePTY{
		StreamID: request.StreamID, Target: request.Target, PTY: ptySession,
		HubWS: hubWS, ctx: ctx, cancel: cancel,
	}
	pm.mu.Lock()
	if previous := pm.sessions[request.StreamID]; previous != nil {
		pm.mu.Unlock()
		active.Teardown("duplicate-stream")
		return
	}
	active.wg.Add(2)
	pm.sessions[request.StreamID] = active
	pm.mu.Unlock()

	var writeMu sync.Mutex
	var images clipboardimage.Receiver
	done := make(chan struct{}, 2)
	go func() {
		defer active.wg.Done()
		defer func() { done <- struct{}{} }()
		var outputSequence uint64
		buffer := make([]byte, 32*1024)
		for {
			n, err := ptySession.Read(buffer)
			if err != nil {
				return
			}
			if pm.activity != nil {
				pm.activity.Record(request.Target.Session, n)
			}
			outputSequence++
			frame, err := EncodePTYDataFrame(PTYDataOutput, outputSequence, buffer[:n])
			writeMu.Lock()
			if err == nil {
				err = hubWS.WriteMessage(websocket.BinaryMessage, frame)
			}
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer active.wg.Done()
		defer func() { done <- struct{}{} }()
		var inputSequence, controlSequence PTYSequence
		var resultSequence uint64
		for {
			messageType, data, err := hubWS.ReadMessage()
			if err != nil {
				return
			}
			switch messageType {
			case websocket.BinaryMessage:
				frame, err := DecodePTYDataFrame(data)
				if err != nil || frame.Direction != PTYDataInput {
					return
				}
				duplicate, err := inputSequence.Accept(frame.Sequence)
				if err != nil {
					return
				}
				if duplicate {
					continue
				}
				if _, err := ptySession.Write(frame.Payload); err != nil {
					return
				}
			case websocket.TextMessage:
				frame, err := DecodePTYControlFrame(data)
				if err != nil {
					return
				}
				duplicate, err := controlSequence.Accept(frame.Sequence)
				if err != nil {
					return
				}
				if duplicate {
					continue
				}
				switch frame.Type {
				case "image":
					path, imageErr := images.Add(frame.Data, frame.Final)
					if imageErr == nil && path != "" {
						_, imageErr = ptySession.Write([]byte("\x1b[200~" + path + "\x1b[201~"))
					}
					if imageErr != nil || path != "" {
						reason := ""
						if imageErr != nil {
							reason = imageErr.Error()
						}
						resultSequence++
						result, _ := EncodePTYControlFrame(PTYControlFrame{Version: PTYFrameVersion, Type: "image-result", Sequence: resultSequence, Reason: reason})
						writeMu.Lock()
						err := hubWS.WriteMessage(websocket.TextMessage, result)
						writeMu.Unlock()
						if err != nil {
							return
						}
					}
				case "resize":
					if err := ptySession.Resize(frame.Cols, frame.Rows); err != nil {
						return
					}
				case "close", "error":
					return
				}
			default:
				return
			}
		}
	}()

	if pm.paneTTY != nil {
		active.wg.Add(1)
		go func() {
			defer active.wg.Done()
			var controlSequence uint64
			pm.watchPasswordMode(ctx, request.Target.Session, func(off bool) error {
				state := "on"
				if off {
					state = "off"
				}
				writeMu.Lock()
				defer writeMu.Unlock()
				controlSequence++
				frame, err := EncodePTYControlFrame(PTYControlFrame{Version: PTYFrameVersion, Type: "echo", Sequence: controlSequence, Reason: state})
				if err != nil {
					return err
				}
				return hubWS.WriteMessage(websocket.TextMessage, frame)
			})
		}()
	}

	<-done
	active.Teardown("relay-ended")
	active.wg.Wait()
	pm.mu.Lock()
	if pm.sessions[request.StreamID] == active {
		delete(pm.sessions, request.StreamID)
	}
	pm.mu.Unlock()
	log.Info("PTY relay stopped")
}

func (active *ActivePTY) Teardown(_ string) {
	active.once.Do(func() {
		active.cancel()
		_ = active.HubWS.Close()
		active.PTY.Close()
	})
}

func (pm *PTYManager) Close(streamID string) {
	pm.mu.RLock()
	active := pm.sessions[streamID]
	pm.mu.RUnlock()
	if active != nil {
		active.Teardown("control-close")
	}
}

func (pm *PTYManager) CloseAll(reason string) {
	pm.mu.RLock()
	active := make([]*ActivePTY, 0, len(pm.sessions))
	for _, session := range pm.sessions {
		active = append(active, session)
	}
	pm.mu.RUnlock()
	for _, session := range active {
		session.Teardown(reason)
	}
	for _, session := range active {
		session.wg.Wait()
	}
}

func (pm *PTYManager) Resize(streamID string, cols, rows uint16) {
	pm.mu.RLock()
	active := pm.sessions[streamID]
	pm.mu.RUnlock()
	if active != nil && cols > 0 && rows > 0 {
		_ = active.PTY.Resize(cols, rows)
	}
}

func (pm *PTYManager) connectPTYWebSocket(request PTYOpenPayload) (*websocket.Conn, error) {
	u, err := hubWebSocketURL(pm.client.HubURL(), "/ws/peer-pty")
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("stream", request.StreamID)
	query.Set("host", request.Target.HostID)
	query.Set("session", request.Target.Session)
	query.Set("generation", strconv.FormatUint(request.Generation, 10))
	query.Set("token", request.AttachToken)
	u.RawQuery = query.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("connect PTY data channel: %w", err)
	}
	conn.SetReadLimit(maxPTYFrameData + ptyDataHeaderSize)
	return conn, nil
}

// watchPasswordMode polls the active pane's terminal. When it reads a line
// without echo, the shape of a password prompt, the browser is told to stop
// sending keystrokes: an agent could fake a sudo prompt in its own pane.
// Full-screen programs run in raw mode and are not reported.
func (pm *PTYManager) watchPasswordMode(ctx context.Context, session string, report func(bool) error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	reported := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		off := false
		if path, err := pm.paneTTY(pm.tmuxPath, session); err == nil {
			if file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0); err == nil {
				off, _ = sudoterm.PasswordMode(file)
				file.Close()
			}
		}
		if off != reported {
			if report(off) != nil {
				return
			}
			reported = off
		}
	}
}
