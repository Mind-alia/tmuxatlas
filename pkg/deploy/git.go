package deploy

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	gitReadTimeout = 15 * time.Second
	// maxDirtyListed bounds the modified files named in a state or a refusal.
	maxDirtyListed = 30
	gitOutputLimit = 256 << 10
)

// Commit is one git commit.
type Commit struct {
	SHA     string `json:"sha"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	// Date is the committer date, RFC 3339.
	Date string `json:"date"`
}

// GitState is what git says of the working directory of a stack, without
// asking the remote anything: Ahead and Behind compare with the remote
// branch as the last fetch left it.
type GitState struct {
	Repo bool `json:"repo"`
	// Error says why the state could not be read.
	Error    string `json:"error,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Upstream is the remote branch the current one follows, such as origin/main.
	Upstream string `json:"upstream,omitempty"`
	// Tracking is true when Ahead and Behind are known.
	Tracking bool    `json:"tracking"`
	Ahead    int     `json:"ahead"`
	Behind   int     `json:"behind"`
	Commit   *Commit `json:"commit,omitempty"`
	// Dirty lists the tracked files with uncommitted changes ("M path").
	Dirty      []string `json:"dirty"`
	DirtyCount int      `json:"dirty_count"`
}

// limited is a writer that stops keeping bytes after its limit.
type limited struct {
	buf   bytes.Buffer
	limit int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

// The two read-only git commands. --no-optional-locks keeps a status from
// taking the index lock, which a concurrent git command would trip over.
var (
	gitStatusArgs = []string{"--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=no"}
	gitHeadArgs   = []string{"--no-optional-locks", "log", "-1", "--format=%H%x1f%h%x1f%cI%x1f%s"}
)

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return shorten(text, 300)
}

func shorten(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) && len(text) > 0 {
		text = text[:len(text)-1]
	}
	return text + "…"
}

// gitOutput runs one read-only git command and returns its raw output; the
// error it returns is displayable.
func gitOutput(ctx context.Context, runner Runner, dir string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()
	out := &limited{limit: gitOutputLimit}
	err := runner.Run(ctx, Command{Tool: ToolGit, Args: args, Dir: dir}, out)
	text := out.buf.String()
	if err != nil {
		if ctx.Err() != nil {
			return text, errors.New("git n’a pas répondu à temps")
		}
		if message := firstLine(clean(out.buf.Bytes())); message != "" {
			return text, errors.New(message)
		}
		return text, err
	}
	return text, nil
}

// parseStatus reads `git status --porcelain=v2 --branch`.
func parseStatus(output string) GitState {
	state := GitState{Repo: true, Dirty: []string{}}
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			state.Branch = shorten(clean([]byte(strings.TrimPrefix(line, "# branch.head "))), 200)
			if state.Branch == "(detached)" {
				state.Branch, state.Detached = "", true
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			state.Upstream = shorten(clean([]byte(strings.TrimPrefix(line, "# branch.upstream "))), 200)
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				ahead, errA := strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				behind, errB := strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
				if errA == nil && errB == nil {
					state.Ahead, state.Behind, state.Tracking = ahead, behind, true
				}
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			// "1 XY sub mH mI mW hH hI path", "2 … Xscore path<TAB>orig",
			// "u XY sub m1 m2 m3 mW h1 h2 h3 path".
			before := map[byte]int{'1': 8, '2': 9, 'u': 10}[line[0]]
			fields := strings.SplitN(line, " ", before+1)
			if len(fields) != before+1 {
				continue
			}
			path, _, _ := strings.Cut(fields[before], "\t")
			state.DirtyCount++
			if len(state.Dirty) < maxDirtyListed {
				state.Dirty = append(state.Dirty, strings.ReplaceAll(fields[1], ".", "")+" "+shorten(clean([]byte(path)), 200))
			}
		}
	}
	return state
}

func parseHead(output string) *Commit {
	fields := strings.SplitN(strings.TrimSpace(output), "\x1f", 4)
	if len(fields) != 4 || fields[0] == "" {
		return nil
	}
	return &Commit{SHA: fields[0], Short: fields[1], Date: fields[2], Subject: shorten(clean([]byte(fields[3])), 200)}
}

// readHead returns the current commit.
func readHead(ctx context.Context, runner Runner, dir string) (*Commit, error) {
	output, err := gitOutput(ctx, runner, dir, gitHeadArgs)
	if err != nil {
		return nil, err
	}
	commit := parseHead(output)
	if commit == nil {
		return nil, errors.New("commit courant illisible")
	}
	return commit, nil
}

// readGit reads the state of the repository dir is in. It changes nothing
// and contacts no remote.
func readGit(ctx context.Context, runner Runner, dir string) GitState {
	output, err := gitOutput(ctx, runner, dir, gitStatusArgs)
	if err != nil {
		state := GitState{Dirty: []string{}, Error: err.Error()}
		if strings.Contains(strings.ToLower(output), "not a git repository") {
			state.Error = "ce dossier n’est pas dans un dépôt git"
		}
		return state
	}
	state := parseStatus(output)
	commit, err := readHead(ctx, runner, dir)
	if err != nil {
		state.Error = err.Error()
		return state
	}
	state.Commit = commit
	return state
}
