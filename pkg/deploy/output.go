package deploy

import (
	"regexp"
	"strings"
	"sync"
)

// Bounds of the command output kept for one step.
const (
	// maxStepOutput is what a record keeps of one step: its end, where the
	// error of a failed build is.
	maxStepOutput = 32 << 10
	// recentOutput is what the polled state carries of each step.
	recentOutput = 6 << 10
)

var (
	// secretAssignment matches NAME=value when NAME evokes a secret.
	secretAssignment = regexp.MustCompile(`(?i)([A-Z0-9_.-]*(?:TOKEN|SECRET|PASSWORD|PASSWD|PASSPHRASE|CREDENTIAL|KEY)[A-Z0-9_.-]*\s*=\s*)("[^"\n]*"|'[^'\n]*'|[^\s"']+)`)
	// urlCredentials matches the "user:password@" of a URL.
	urlCredentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)
	// authorization matches "Bearer xyz" and "Basic xyz".
	authorization = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// ansiEscape matches the colour and cursor sequences of a terminal.
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-Z\\-_]`)
)

// Redact masks what looks like a secret in command output: NAME=value for
// names that evoke one, credentials in URLs, authorization headers. It is a
// best effort: a secret printed without such a shape goes through.
func Redact(text string) string {
	text = secretAssignment.ReplaceAllString(text, "${1}***")
	text = urlCredentials.ReplaceAllString(text, "${1}***@")
	return authorization.ReplaceAllString(text, "${1} ***")
}

// clean makes raw command output displayable: valid UTF-8, no terminal
// escape, one line per carriage return, secrets masked.
func clean(raw []byte) string {
	text := strings.ToValidUTF8(string(raw), "?")
	text = ansiEscape.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	return Redact(text)
}

// lastBytes keeps the end of text, from the start of a line: a cut in the
// middle of NAME=value would leave the end of the value unmasked.
func lastBytes(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	text = text[len(text)-limit:]
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[i+1:]
	} else {
		text = ""
	}
	return strings.ToValidUTF8(text, ""), true
}

// tail is a writer that keeps the end of what it receives.
type tail struct {
	mu        sync.Mutex
	buf       []byte
	truncated bool
}

// rawLimit leaves room for escapes that clean removes.
const rawLimit = 4 * maxStepOutput

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*rawLimit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-rawLimit:]...)
		t.truncated = true
	}
	return len(p), nil
}

// text returns the cleaned end of the output, at most limit bytes, and
// whether its beginning was dropped.
func (t *tail) text(limit int) (string, bool) {
	t.mu.Lock()
	raw := append([]byte(nil), t.buf...)
	truncated := t.truncated
	t.mu.Unlock()
	if truncated {
		// The buffer may start in the middle of a line.
		if i := strings.IndexByte(string(raw), '\n'); i >= 0 {
			raw = raw[i+1:]
		}
	}
	text, cut := lastBytes(clean(raw), limit)
	return text, truncated || cut
}
