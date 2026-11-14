package dictation

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Claude Code's screen, as `tmux capture-pane -e` returns it: its prompt sits
// between two rules, and its footer below the second one.
var (
	rule = regexp.MustCompile(`^─{10,}$`)
	// Footer of a dictation, as the Talk button reads it in the browser.
	recording  = regexp.MustCompile(`listening…|keep holding…`)
	processing = regexp.MustCompile(`Voice: processing…`)
	failed     = regexp.MustCompile(`No speech detected|No audio detected|Voice connection failed|Voice stream error|Dictation error|Voice mode requires|Voice input is failing`)
	// Startup screens met before the prompt: Claude's folder trust dialog, and the
	// account menu of a launcher such as ai-accounts, whose default is marked.
	trustDialog    = regexp.MustCompile(`Yes, I trust this folder`)
	dialogFooter   = regexp.MustCompile(`Enter to confirm|Esc to cancel`)
	launcherMenu   = regexp.MustCompile(`^Choix\s*:`)
	menuDefault    = regexp.MustCompile(`\(Entrée\)`)
	missingAccount = regexp.MustCompile(`n'a pas (encore )?de compte`)
)

type voiceStatus int

const (
	voiceIdle voiceStatus = iota
	voiceRecording
	voiceProcessing
	voiceFailed
)

type startupScreen int

const (
	startupUnknown startupScreen = iota
	startupReady
	startupTrust
	startupMenu
	startupMenuWithoutDefault
	startupNoAccount
)

// visible returns a captured line without its escape sequences. Dim text is
// left out with them: Claude prints its prompt's placeholder that way.
func visible(line string) (plain, shown string) {
	var all, bright strings.Builder
	dim := false
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			end := i + 1
			for end < len(line) && line[end] != 0x1b {
				end++
			}
			all.WriteString(line[i:end])
			if !dim {
				bright.WriteString(line[i:end])
			}
			i = end
			continue
		}
		// ESC [ parameters final-byte; a lone ESC or another sequence is skipped.
		if i+1 >= len(line) || line[i+1] != '[' {
			i += 2
			continue
		}
		end := i + 2
		for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
			end++
		}
		if end < len(line) && line[end] == 'm' {
			dim = applySGR(dim, line[i+2:end])
		}
		i = end + 1
	}
	return all.String(), bright.String()
}

// applySGR follows the dim attribute through one Select Graphic Rendition
// sequence, skipping the arguments of extended colors.
func applySGR(dim bool, params string) bool {
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		value, _ := strconv.Atoi(fields[i])
		switch value {
		case 0:
			dim = false
		case 2:
			dim = true
		case 22:
			dim = false
		case 38, 48, 58:
			if i+1 < len(fields) && fields[i+1] == "5" {
				i += 2
			} else if i+1 < len(fields) && fields[i+1] == "2" {
				i += 4
			}
		}
	}
	return dim
}

type screen struct {
	plain []string // each row without escape sequences
	shown []string // each row without dim text either
	rules []int    // rows holding a full rule
}

func parseScreen(capture string) screen {
	var s screen
	for _, line := range strings.Split(strings.TrimRight(capture, "\n"), "\n") {
		plain, shown := visible(line)
		if rule.MatchString(strings.TrimSpace(plain)) {
			s.rules = append(s.rules, len(s.plain))
		}
		s.plain = append(s.plain, plain)
		s.shown = append(s.shown, shown)
	}
	return s
}

// prompt returns the text typed in Claude's prompt, without its placeholder or
// the recording glyphs Claude draws there while it listens.
func (s screen) prompt() string {
	if len(s.rules) < 2 {
		return ""
	}
	top, bottom := s.rules[len(s.rules)-2], s.rules[len(s.rules)-1]
	var parts []string
	for _, row := range s.shown[top+1 : bottom] {
		row = strings.TrimLeftFunc(row, unicode.IsSpace)
		row = strings.TrimPrefix(row, "❯")
		row = strings.Map(func(r rune) rune {
			if r >= 0x2580 && r <= 0x259f { // block elements: cursor and level meter
				return -1
			}
			return r
		}, row)
		if row = strings.TrimSpace(row); row != "" {
			parts = append(parts, row)
		}
	}
	return strings.Join(parts, " ")
}

func (s screen) footer() string {
	if len(s.rules) == 0 {
		return ""
	}
	return strings.Join(s.plain[s.rules[len(s.rules)-1]+1:], "\n")
}

func (s screen) voice() voiceStatus {
	footer := s.footer()
	switch {
	case recording.MatchString(footer):
		return voiceRecording
	case processing.MatchString(footer):
		return voiceProcessing
	case failed.MatchString(footer):
		return voiceFailed
	}
	return voiceIdle
}

// startup tells what stands between a fresh hidden session and Claude's prompt.
func (s screen) startup() startupScreen {
	text := strings.Join(s.plain, "\n")
	var last string
	for i := len(s.plain) - 1; i >= 0; i-- {
		if strings.TrimSpace(s.plain[i]) != "" {
			last = strings.TrimSpace(s.plain[i])
			break
		}
	}
	switch {
	case missingAccount.MatchString(text):
		return startupNoAccount
	case launcherMenu.MatchString(last):
		if menuDefault.MatchString(text) {
			return startupMenu
		}
		return startupMenuWithoutDefault
	case trustDialog.MatchString(text):
		return startupTrust
	case len(s.rules) >= 2 && !dialogFooter.MatchString(text) &&
		strings.HasPrefix(strings.TrimSpace(s.plain[s.rules[len(s.rules)-2]+1]), "❯"):
		return startupReady
	}
	return startupUnknown
}
