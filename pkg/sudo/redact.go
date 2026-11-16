package sudo

import (
	"bytes"
	"sort"
)

// MinRedactLength is the shortest typed segment that is redacted. Shorter
// answers ("y", "no") would erase too much ordinary output.
const MinRedactLength = 4

// RedactionMark replaces every redacted segment.
const RedactionMark = "[caviardé]"

// TypedSegments splits what a human typed into the terminal into the lines
// they entered, handling backspace the way a canonical terminal does.
func TypedSegments(typed []byte) [][]byte {
	var segments [][]byte
	var current []byte
	flush := func() {
		if len(bytes.TrimSpace(current)) >= MinRedactLength {
			segments = append(segments, append([]byte(nil), current...))
		}
		current = current[:0]
	}
	for _, b := range typed {
		switch b {
		case '\r', '\n', 0x03, 0x04: // enter, Ctrl-C, Ctrl-D
			flush()
		case 0x7f, 0x08:
			if len(current) > 0 {
				current = current[:len(current)-1]
			}
		case 0x15: // Ctrl-U
			current = current[:0]
		default:
			if b >= 0x20 || b == '\t' {
				current = append(current, b)
			}
		}
	}
	flush()
	return segments
}

// Redact removes from output every segment of MinRedactLength bytes or
// more. Longer segments are applied first so a short one cannot leave part
// of a longer one in place. It returns the redacted output and the count.
func Redact(output []byte, segments [][]byte) ([]byte, int) {
	ordered := make([][]byte, 0, len(segments))
	for _, segment := range segments {
		trimmed := bytes.TrimSpace(segment)
		if len(trimmed) >= MinRedactLength {
			ordered = append(ordered, trimmed)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	count := 0
	result := output
	for _, segment := range ordered {
		if n := bytes.Count(result, segment); n > 0 {
			count += n
			result = bytes.ReplaceAll(result, segment, []byte(RedactionMark))
		}
	}
	return result, count
}

// Wipe overwrites a buffer that held typed input.
func Wipe(buffer []byte) {
	for i := range buffer {
		buffer[i] = 0
	}
}
