package dockerui

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Masked replaces every secret value sent to the browser.
const Masked = "••••"

// sensitiveName matches keys, labels and flags that usually carry a secret.
var sensitiveName = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|credential|authorization|bearer|basicauth|digestauth|requirepass|masterauth|(^|[._/-])(pass|pwd|auth)($|[._/-]))`)

// urlCredentials matches the password of scheme://user:password@host.
var urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/?#\s:@]*):[^@/?#\s]*@`)

// inlineSecret matches NAME=value inside a string when NAME looks secret,
// such as PGPASSWORD=… in a healthcheck command.
var inlineSecret = regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:password|passwd|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|credential|bearer)[a-z0-9_.-]*)=([^\s'"&;]+)`)

// flagPattern splits --name=value and -name=value arguments.
var flagPattern = regexp.MustCompile(`^(--?[A-Za-z0-9_.-]+)(?:=(.*))?$`)

// userPassword matches curl's -u user:password and --user user:password.
var userPassword = regexp.MustCompile(`(^|\s)(-u|--user)(\s+|=)([^\s:'"]+):([^\s'"]+)`)

// A word of a shell command line: quoted (the closing quote may be missing at
// the end of the text) or not.
const shellWord = `"[^"]*"?|'[^']*'?|[^\s"']+`

// headerArgument matches the header given to curl's -H or wget's --header in
// a shell command line, attached, after "=" or after spaces.
var headerArgument = regexp.MustCompile(`(^|\s)(-H|--header)(=|[ \t]*)(` + shellWord + `)`)

// authorizationHeader matches the value of an Authorization or
// Proxy-Authorization header anywhere in a string, as in "Authorization:
// Bearer x" or {"Authorization": "Bearer x"}: a quoted value, or else a
// scheme with its credentials, or a single word.
var authorizationHeader = regexp.MustCompile(`(?i)((?:proxy-)?authorization["']?[ \t]*[:=][ \t]*)("[^"]*"?|'[^']*'?|(?:[a-z][a-z0-9_.+-]*[ \t]+)?[^\s"']+)`)

// flagValue matches a flag followed by its value in a shell command line,
// such as --oauth2-bearer x; maskString masks it when the flag looks secret.
var flagValue = regexp.MustCompile(`(^|\s)(--?[A-Za-z0-9][A-Za-z0-9_.-]*)([ \t]+)("[^"]*"?|'[^']*'?|[^\s"'-][^\s"']*)`)

// bearerToken and basicCredentials match the credentials that follow the
// Bearer and Basic schemes. Basic credentials are the base64 of
// user:password, at least 8 characters.
var (
	bearerToken      = regexp.MustCompile(`(?i)\b(bearer[ \t]+)[A-Za-z0-9._~+/-]+=*`)
	basicCredentials = regexp.MustCompile(`(?i)\b(basic[ \t]+)([A-Za-z0-9+/]{8,}=*)`)
)

// sanitizeInspect returns the inspect document without its secrets: every
// environment value is replaced by Masked (KEY=••••), then values whose key,
// label or flag name looks secret, URL passwords and HTTP credentials (the
// value of every -H or --header header, of Authorization and
// Proxy-Authorization, Bearer and Basic tokens) are masked anywhere in the
// document. Only the environment is masked unconditionally; the rest is a
// best effort for secrets passed outside the environment.
func sanitizeInspect(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if config, ok := doc["Config"].(map[string]any); ok {
		if env, ok := config["Env"].([]any); ok {
			for i, entry := range env {
				if text, ok := entry.(string); ok {
					env[i] = maskEnv(text)
				} else {
					env[i] = Masked
				}
			}
		}
		for _, key := range []string{"Cmd", "Entrypoint"} {
			maskArgs(config[key])
		}
		if healthcheck, ok := config["Healthcheck"].(map[string]any); ok {
			maskArgs(healthcheck["Test"])
		}
	}
	maskArgs(doc["Args"])
	walkMask(doc)
	return json.Marshal(doc)
}

// maskEnv keeps the variable name only.
func maskEnv(entry string) string {
	name, _, found := strings.Cut(entry, "=")
	if !found {
		return entry
	}
	return name + "=" + Masked
}

// maskArgs hides --password=x, PASSWORD=x, the argument after --password and
// the value of every header given to -H or --header.
func maskArgs(value any) {
	args, ok := value.([]any)
	if !ok {
		return
	}
	for i := 0; i < len(args); i++ {
		text, ok := args[i].(string)
		if !ok {
			continue
		}
		if (text == "-H" || text == "--header") && i+1 < len(args) {
			if next, ok := args[i+1].(string); ok {
				args[i+1] = maskHeader(next)
				i++
			}
			continue
		}
		if header, found := strings.CutPrefix(text, "--header="); found {
			args[i] = "--header=" + maskHeader(header)
			continue
		}
		if header, found := strings.CutPrefix(text, "-H"); found && header != "" {
			args[i] = "-H" + maskHeader(header)
			continue
		}
		if (text == "-u" || text == "--user") && i+1 < len(args) {
			if next, ok := args[i+1].(string); ok {
				if name, _, found := strings.Cut(next, ":"); found {
					args[i+1] = name + ":" + Masked
					i++
					continue
				}
			}
		}
		if match := flagPattern.FindStringSubmatch(text); match != nil && sensitiveName.MatchString(strings.TrimLeft(match[1], "-")) {
			if strings.Contains(text, "=") {
				args[i] = match[1] + "=" + Masked
			} else if i+1 < len(args) {
				if next, ok := args[i+1].(string); ok && !strings.HasPrefix(next, "-") {
					args[i+1] = Masked
					i++
				}
			}
			continue
		}
		// A whole NAME=secret argument; a script with spaces is left to
		// maskString, which masks the assignment and keeps the command.
		if name, _, found := strings.Cut(text, "="); found && !strings.ContainsAny(text, " \t\n") && sensitiveName.MatchString(name) {
			args[i] = name + "=" + Masked
		}
	}
}

// walkMask masks string values under a secret-looking key, and URL
// passwords or NAME=secret assignments inside every other string.
func walkMask(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, isString := child.(string); isString && sensitiveName.MatchString(key) {
				typed[key] = Masked
				continue
			}
			typed[key] = walkMask(child)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = walkMask(child)
		}
		return typed
	case string:
		return maskString(typed)
	default:
		return value
	}
}

func maskString(text string) string {
	// Only to skip the patterns that cannot match: masking never adds a word.
	lower := strings.ToLower(text)
	if strings.Contains(text, "://") {
		text = urlCredentials.ReplaceAllString(text, "${1}:"+Masked+"@")
	}
	if strings.Contains(text, "-u") {
		text = userPassword.ReplaceAllString(text, "${1}${2}${3}${4}:"+Masked)
	}
	if strings.Contains(text, "-H") || strings.Contains(text, "--header") {
		text = replaceSubmatches(headerArgument, text, func(group []string) string {
			return group[1] + group[2] + group[3] + maskWord(group[4], maskHeader)
		})
	}
	if strings.Contains(lower, "authorization") {
		text = replaceSubmatches(authorizationHeader, text, func(group []string) string {
			return group[1] + maskWord(group[2], maskAll)
		})
	}
	if strings.Contains(text, "-") {
		text = replaceSubmatches(flagValue, text, func(group []string) string {
			if !sensitiveName.MatchString(strings.TrimLeft(group[2], "-")) {
				return group[0]
			}
			return group[1] + group[2] + group[3] + maskWord(group[4], maskAll)
		})
	}
	if strings.Contains(text, "=") {
		text = inlineSecret.ReplaceAllString(text, "${1}="+Masked)
	}
	if strings.Contains(lower, "bearer") {
		text = bearerToken.ReplaceAllString(text, "${1}"+Masked)
	}
	if strings.Contains(lower, "basic") {
		text = replaceSubmatches(basicCredentials, text, func(group []string) string {
			// A word of prose ("Basic authentication") is lowercase after its
			// first letter; base64 of that length almost never is.
			if strings.IndexFunc(group[2][1:], func(r rune) bool { return r < 'a' || r > 'z' }) < 0 {
				return group[0]
			}
			return group[1] + Masked
		})
	}
	return text
}

func maskAll(string) string { return Masked }

// maskHeader keeps the name of a "Name: value" header and masks its value.
// Anything without a value, such as curl's -H @file, is kept.
func maskHeader(header string) string {
	name, value, found := strings.Cut(header, ":")
	rest := strings.TrimLeft(value, " \t")
	if !found || rest == "" {
		return header
	}
	return name + ":" + value[:len(value)-len(rest)] + Masked
}

// maskWord applies mask to the inside of a shell word, keeping its quotes.
func maskWord(word string, mask func(string) string) string {
	if word == "" || (word[0] != '"' && word[0] != '\'') {
		return mask(word)
	}
	if len(word) >= 2 && word[len(word)-1] == word[0] {
		return word[:1] + mask(word[1:len(word)-1]) + word[:1]
	}
	return word[:1] + mask(word[1:])
}

// replaceSubmatches rewrites every match of pattern in text by replace,
// which receives the whole match and its groups.
func replaceSubmatches(pattern *regexp.Regexp, text string, replace func(group []string) string) string {
	matches := pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		group := make([]string, len(match)/2)
		for i := range group {
			if match[2*i] >= 0 {
				group[i] = text[match[2*i]:match[2*i+1]]
			}
		}
		out.WriteString(text[last:match[0]])
		out.WriteString(replace(group))
		last = match[1]
	}
	out.WriteString(text[last:])
	return out.String()
}
