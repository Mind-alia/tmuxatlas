// Package agentteams reads what Claude Code records on disk about its running
// sessions and the sub-agents they started, so the hub can show a session's
// team. It is read-only and only looks under the hub account's own ~/.claude.
package agentteams

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Agent states. Claude Code gives no progress figure, only whether the
// sub-agent has finished and when it last wrote to its transcript.
const (
	StateRunning = "running" // not finished, wrote recently
	StateQuiet   = "quiet"   // not finished, silent for a while (long command, or stuck)
	StateStopped = "stopped" // not finished and silent for so long it was most likely interrupted
	StateDone    = "done"
)

const (
	quietAfter   = 3 * time.Minute
	stoppedAfter = 30 * time.Minute
	// keepFor bounds how far back a session's agents are listed.
	keepFor      = 24 * time.Hour
	maxAgents    = 24
	maxLogLines  = 4
	maxLineBytes = 8 << 20
	maxActionLen = 96
)

// Agent is one sub-agent of a session.
type Agent struct {
	ID          string    `json:"id"`
	Parent      string    `json:"parent,omitempty"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	Model       string    `json:"model,omitempty"`
	State       string    `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Steps counts the tool calls made so far.
	Steps int `json:"steps"`
	// Action is the latest tool call, Log the few before it (oldest first).
	Action string   `json:"action"`
	Log    []string `json:"log"`
}

// Team is one live Claude Code session and the sub-agents it started.
type Team struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Cwd       string `json:"cwd"`
	// TmuxSession is empty when the session does not run inside tmux.
	TmuxSession string  `json:"tmux_session,omitempty"`
	Status      string  `json:"status"`
	WaitingFor  string  `json:"waiting_for,omitempty"`
	Agents      []Agent `json:"agents"`
}

// Reader lists teams. The zero value is not usable; call NewReader.
type Reader struct {
	claudeDir string
	now       func() time.Time
	alive     func(pid int) bool

	mu    sync.Mutex
	cache map[string]*transcript
}

// NewReader reads from claudeDir, normally ~/.claude.
func NewReader(claudeDir string) *Reader {
	return &Reader{
		claudeDir: claudeDir,
		now:       time.Now,
		alive:     processAlive,
		cache:     make(map[string]*transcript),
	}
}

func processAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

type sessionFile struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Tmux       string `json:"tmux"`
	WaitingFor string `json:"waitingFor"`
}

type metaFile struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Model       string `json:"model"`
	PickedModel string `json:"pickedEffortModel"`
	Parent      string `json:"parentAgentId"`
	Finished    bool   `json:"finished"`
}

var (
	sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)
	agentIDPattern   = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)
)

// Teams returns every live session, the ones with unfinished agents first.
func (r *Reader) Teams() ([]Team, error) {
	entries, err := os.ReadDir(filepath.Join(r.claudeDir, "sessions"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Team{}, nil
		}
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]bool)
	teams := []Team{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.claudeDir, "sessions", entry.Name()))
		if err != nil {
			continue
		}
		var s sessionFile
		if json.Unmarshal(raw, &s) != nil || s.PID <= 0 || !sessionIDPattern.MatchString(s.SessionID) {
			continue
		}
		if !r.alive(s.PID) {
			continue
		}
		team := Team{
			SessionID:   s.SessionID,
			Name:        s.Name,
			Cwd:         s.Cwd,
			TmuxSession: tmuxSessionName(s.Tmux),
			Status:      s.Status,
			WaitingFor:  s.WaitingFor,
			Agents:      r.agents(s.SessionID, seen),
		}
		teams = append(teams, team)
	}
	for path := range r.cache {
		if !seen[path] {
			delete(r.cache, path)
		}
	}
	sort.SliceStable(teams, func(i, j int) bool {
		a, b := activeCount(teams[i]), activeCount(teams[j])
		if a != b {
			return a > b
		}
		if len(teams[i].Agents) != len(teams[j].Agents) {
			return len(teams[i].Agents) > len(teams[j].Agents)
		}
		return teams[i].Name < teams[j].Name
	})
	return teams, nil
}

func activeCount(t Team) int {
	n := 0
	for _, a := range t.Agents {
		if a.State == StateRunning || a.State == StateQuiet {
			n++
		}
	}
	return n
}

// tmuxSessionName extracts "name" from Claude's "name:@window.%pane".
func tmuxSessionName(value string) string {
	if i := strings.LastIndex(value, ":@"); i >= 0 {
		return value[:i]
	}
	return ""
}

func (r *Reader) agents(sessionID string, seen map[string]bool) []Agent {
	agents := []Agent{}
	dirs, _ := filepath.Glob(filepath.Join(r.claudeDir, "projects", "*", sessionID, "subagents"))
	now := r.now()
	for _, dir := range dirs {
		metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
		for _, metaPath := range metas {
			id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(metaPath), "agent-"), ".meta.json")
			if !agentIDPattern.MatchString(id) {
				continue
			}
			logPath := filepath.Join(dir, "agent-"+id+".jsonl")
			info, err := os.Stat(logPath)
			if err != nil || now.Sub(info.ModTime()) > keepFor {
				continue
			}
			raw, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var meta metaFile
			if json.Unmarshal(raw, &meta) != nil {
				continue
			}
			if meta.Model == "" {
				meta.Model = meta.PickedModel
			}
			seen[logPath] = true
			t := r.cache[logPath]
			if t == nil {
				t = &transcript{}
				r.cache[logPath] = t
			}
			t.catchUp(logPath, info.Size())

			agent := Agent{
				ID:          id,
				Parent:      meta.Parent,
				Type:        meta.AgentType,
				Description: clip(meta.Description, maxActionLen),
				Model:       meta.Model,
				StartedAt:   t.started,
				UpdatedAt:   info.ModTime(),
				Steps:       t.steps,
				Log:         []string{},
			}
			if n := len(t.actions); n > 0 {
				agent.Action = t.actions[n-1]
				agent.Log = append(agent.Log, t.actions[:n-1]...)
			}
			if agent.StartedAt.IsZero() {
				agent.StartedAt = info.ModTime()
			}
			silent := now.Sub(info.ModTime())
			switch {
			case meta.Finished:
				agent.State = StateDone
			case silent > stoppedAfter:
				agent.State = StateStopped
			case silent > quietAfter:
				agent.State = StateQuiet
			default:
				agent.State = StateRunning
			}
			agents = append(agents, agent)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		ai, aj := stateRank(agents[i].State), stateRank(agents[j].State)
		if ai != aj {
			return ai < aj
		}
		return agents[i].UpdatedAt.After(agents[j].UpdatedAt)
	})
	if len(agents) > maxAgents {
		agents = agents[:maxAgents]
	}
	return agents
}

func stateRank(state string) int {
	switch state {
	case StateRunning:
		return 0
	case StateQuiet:
		return 1
	case StateDone:
		return 2
	}
	return 3
}

// transcript remembers how far a sub-agent's log was read, so each poll only
// parses what was appended since the previous one.
type transcript struct {
	offset  int64
	started time.Time
	steps   int
	actions []string
}

type transcriptLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type  string                     `json:"type"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

func (t *transcript) catchUp(path string, size int64) {
	if size < t.offset { // the file was replaced: start again
		*t = transcript{}
	}
	if size == t.offset {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, err := readLine(reader)
		if err != nil { // io.EOF: what is left is a line still being written
			return
		}
		t.offset += int64(line.length)
		if line.data != nil {
			t.consume(line.data)
		}
	}
}

type rawLine struct {
	data   []byte // nil when the line was longer than maxLineBytes
	length int
}

// readLine returns one complete line. A line without its final newline is
// reported as io.EOF and not consumed from the transcript's point of view.
func readLine(reader *bufio.Reader) (rawLine, error) {
	var line rawLine
	var buffer []byte
	tooLong := false
	for {
		chunk, err := reader.ReadSlice('\n')
		line.length += len(chunk)
		if !tooLong {
			if len(buffer)+len(chunk) > maxLineBytes {
				tooLong, buffer = true, nil
			} else {
				buffer = append(buffer, chunk...)
			}
		}
		if err == nil {
			if !tooLong {
				line.data = buffer
			}
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return rawLine{}, io.EOF
		}
	}
}

func (t *transcript) consume(data []byte) {
	isAssistant := bytes.Contains(data, []byte(`"type":"assistant"`))
	if !isAssistant && !t.started.IsZero() {
		return
	}
	var line transcriptLine
	if json.Unmarshal(data, &line) != nil {
		return
	}
	if t.started.IsZero() && !line.Timestamp.IsZero() {
		t.started = line.Timestamp
	}
	if line.Type != "assistant" {
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		if block.Type != "tool_use" {
			continue
		}
		t.steps++
		t.actions = append(t.actions, describe(block))
		if len(t.actions) > maxLogLines {
			t.actions = append([]string(nil), t.actions[len(t.actions)-maxLogLines:]...)
		}
	}
}

// describe turns a tool call into one short line: the tool and its main argument.
func describe(block contentBlock) string {
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if value := inputString(block.Input, key); value != "" {
			return clip(block.Name+" "+shortPath(value), maxActionLen)
		}
	}
	for _, key := range []string{"command", "pattern", "query", "url", "description", "skill"} {
		if value := inputString(block.Input, key); value != "" {
			return clip(block.Name+" "+firstLine(value), maxActionLen)
		}
	}
	return clip(block.Name, maxActionLen)
}

func inputString(input map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := input[key]; ok && json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	return ""
}

func shortPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return strings.Join(parts, "/")
}

func firstLine(value string) string {
	if i := strings.IndexAny(value, "\r\n"); i >= 0 {
		value = value[:i]
	}
	return strings.Join(strings.Fields(value), " ")
}

func clip(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
