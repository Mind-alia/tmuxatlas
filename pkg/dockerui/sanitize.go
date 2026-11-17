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
var sensitiveName = regexp.MustCompile(`(?i)(password|passwd|passphrase|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|credential|basicauth|digestauth|requirepass|masterauth|(^|[._/-])(pass|pwd|auth)($|[._/-]))`)

// urlCredentials matches the password of scheme://user:password@host.
var urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/?#\s:@]*):[^@/?#\s]*@`)

// inlineSecret matches NAME=value inside a string when NAME looks secret,
// such as PGPASSWORD=… in a healthcheck command.
var inlineSecret = regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:password|passwd|secret|token|api[-_]?key|access[-_]?key|private[-_]?key|credential)[a-z0-9_.-]*)=([^\s'"&;]+)`)

// flagPattern splits --name=value and -name=value arguments.
var flagPattern = regexp.MustCompile(`^(--?[A-Za-z0-9_.-]+)(?:=(.*))?$`)

// userPassword matches curl's -u user:password and --user user:password.
var userPassword = regexp.MustCompile(`(^|\s)(-u|--user)(\s+|=)([^\s:'"]+):([^\s'"]+)`)

// sanitizeInspect returns the inspect document without its secrets: every
// environment value is replaced by Masked (KEY=••••), then values whose key,
// label or flag name looks secret and URL passwords are masked anywhere in
// the document. Only the environment is masked unconditionally; the rest is
// a best effort for secrets passed outside the environment.
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

// maskArgs hides --password=x, PASSWORD=x and the argument after --password.
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
	if strings.Contains(text, "://") {
		text = urlCredentials.ReplaceAllString(text, "${1}:"+Masked+"@")
	}
	if strings.Contains(text, "-u") {
		text = userPassword.ReplaceAllString(text, "${1}${2}${3}${4}:"+Masked)
	}
	if strings.Contains(text, "=") {
		text = inlineSecret.ReplaceAllString(text, "${1}="+Masked)
	}
	return text
}
