package dockerui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxLogLines and MaxLogBytes bound one logs response; the newest lines
	// are the ones kept.
	MaxLogLines = 2000
	MaxLogBytes = 2 << 20
	// maxLogRead bounds the bytes read from Docker for one response. A tail
	// of 2000 lines is far below it unless lines average over 4 KiB.
	maxLogRead = 8 << 20
	// DefaultLogTail is the number of lines of a first load.
	DefaultLogTail = 500
	// maxLogLineBytes cuts pathological lines so one cannot fill a response.
	maxLogLineBytes = 16 << 10
)

// LogLine is one log line. T is Docker's RFC 3339 timestamp, S is "out" or
// "err" (always "out" for a TTY container, whose streams are merged).
type LogLine struct {
	T string `json:"t,omitempty"`
	S string `json:"s"`
	M string `json:"m"`
}

// LogsResponse is the payload of GET /api/docker/containers/{id}/logs.
type LogsResponse struct {
	Lines []LogLine `json:"lines"`
	// NextSince is the since value of the next poll: just after the last line.
	NextSince string `json:"next_since,omitempty"`
	// Truncated means lines were left out: the byte cap was reached, or a
	// poll returned as many lines as it asked for.
	Truncated bool `json:"truncated"`
}

var sincePattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,9})?$`)

// ansiPattern matches CSI and OSC escape sequences.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[@-Z\\\\-_]")

// parseLogStream reads a logs body and keeps its newest MaxLogLines lines
// and MaxLogBytes bytes. A container without TTY sends a multiplexed stream:
// frames of an 8-byte header (stream, 0, 0, 0, size as a big-endian uint32)
// followed by size bytes. At most maxLogRead bytes are read, and no buffer is
// ever sized from a header.
func parseLogStream(r io.Reader, multiplexed bool) ([]LogLine, bool, error) {
	kept := &logWindow{lines: make([]LogLine, 0, 64)}
	if !multiplexed {
		data, err := io.ReadAll(io.LimitReader(r, maxLogRead+1))
		if len(data) > maxLogRead {
			// Drop the partial last line rather than show half of it.
			data = data[:maxLogRead]
			if i := strings.LastIndexByte(string(data), '\n'); i >= 0 {
				data = data[:i+1]
			}
			kept.truncated = true
		}
		kept.add(data, "out")
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return kept.result(), true, err
		}
		return kept.result(), kept.truncated, nil
	}

	var header [8]byte
	var payload bytes.Buffer
	budget := int64(maxLogRead)
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return kept.result(), true, err
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		want := min(size, budget)
		// The buffer grows with the bytes received, never with the header.
		payload.Reset()
		n, err := io.CopyN(&payload, r, want)
		budget -= n
		if n == want && want < size {
			// The read bound ends inside this frame: leave it out rather
			// than show half a line. The next poll resumes after the last
			// line kept.
			kept.truncated = true
			break
		}
		kept.add(payload.Bytes(), streamName(header[0]))
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return kept.result(), true, err
		}
	}
	return kept.result(), kept.truncated, nil
}

// logWindow keeps the newest lines within MaxLogLines and MaxLogBytes.
// Old lines are skipped by moving start, and the slice is compacted once in
// a while, so each line costs O(1).
type logWindow struct {
	lines     []LogLine
	start     int
	bytes     int
	truncated bool
}

func (w *logWindow) add(payload []byte, stream string) {
	before := len(w.lines)
	w.lines = appendLogLines(w.lines, payload, stream)
	for _, line := range w.lines[before:] {
		w.bytes += len(line.T) + len(line.M)
	}
	for len(w.lines)-w.start > MaxLogLines || (w.bytes > MaxLogBytes && w.start < len(w.lines)) {
		w.bytes -= len(w.lines[w.start].T) + len(w.lines[w.start].M)
		w.lines[w.start] = LogLine{}
		w.start++
		w.truncated = true
	}
	if w.start > 4096 && w.start > len(w.lines)/2 {
		w.lines = append(w.lines[:0], w.lines[w.start:]...)
		w.start = 0
	}
}

func (w *logWindow) result() []LogLine {
	return w.lines[w.start:]
}

func streamName(kind byte) string {
	switch kind {
	case 2, 3: // stderr, and the daemon's own errors (API 1.42+)
		return "err"
	default:
		return "out"
	}
}

// appendLogLines splits a payload into lines. Each line starts with the
// RFC 3339 timestamp requested with timestamps=1.
func appendLogLines(lines []LogLine, payload []byte, stream string) []LogLine {
	text := string(payload)
	for len(text) > 0 {
		segment := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			segment, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		segment = strings.TrimSuffix(segment, "\r")
		if segment == "" {
			continue
		}
		line := LogLine{S: stream, M: segment}
		if ts, rest, ok := strings.Cut(segment, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				line.T, line.M = ts, rest
			}
		} else if _, err := time.Parse(time.RFC3339Nano, segment); err == nil {
			line.T, line.M = segment, ""
		}
		line.M = cleanLogText(line.M)
		lines = append(lines, line)
	}
	return lines
}

// cleanLogText keeps what a terminal would show: colours and other escape
// sequences go, a carriage return keeps only the text written after it, and
// control characters other than tabs are dropped.
func cleanLogText(text string) string {
	if strings.IndexByte(text, 0x1b) >= 0 {
		text = ansiPattern.ReplaceAllString(text, "")
	}
	if i := strings.LastIndexByte(text, '\r'); i >= 0 {
		text = text[i+1:]
	}
	text = strings.ToValidUTF8(text, "�")
	text = strings.Map(func(r rune) rune {
		if r == '\t' || r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) {
			return r
		}
		return -1
	}, text)
	if len(text) > maxLogLineBytes {
		cut := maxLogLineBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + " …"
	}
	return text
}

// nextSince returns the since value that excludes every line already sent:
// one nanosecond after the newest timestamp (Docker's since is inclusive).
func nextSince(lines []LogLine, previous string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].T == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, lines[i].T)
		if err != nil {
			continue
		}
		t = t.Add(time.Nanosecond)
		return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
	}
	return previous
}
