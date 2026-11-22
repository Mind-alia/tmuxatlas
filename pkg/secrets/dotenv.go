package secrets

import (
	"regexp"
	"strings"
)

// Pair is one variable in clear. It only lives in memory.
type Pair struct {
	Name  string
	Value string
}

// ParseDotenv reads a dotenv text: blank lines and # comments are skipped,
// an "export " prefix is accepted, a value may be bare (an inline " #"
// comment is cut), single-quoted (literal, may span lines) or double-quoted
// (\n \r \t \\ \" \$ escapes, may span lines). A name given twice keeps its
// last value. Errors name the line, never its content.
func ParseDotenv(text string) ([]Pair, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	var pairs []Pair
	index := make(map[string]int)
	line := 1
	for pos := 0; pos < len(text); {
		end := strings.IndexByte(text[pos:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += pos
		}
		current := strings.TrimLeft(text[pos:end], " \t")
		if current == "" || current[0] == '#' {
			pos, line = end+1, line+1
			continue
		}
		if rest, ok := strings.CutPrefix(current, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			current = strings.TrimLeft(rest, " \t")
		}
		equal := strings.IndexByte(current, '=')
		if equal < 0 {
			return nil, invalid("ligne %d : NOM=valeur attendu", line)
		}
		name := strings.TrimRight(current[:equal], " \t")
		if ValidateVarName(name) != nil {
			return nil, invalid("ligne %d : nom de variable invalide", line)
		}
		// valueStart is the offset of the value in text.
		valueStart := end - len(current) + equal + 1
		for valueStart < end && (text[valueStart] == ' ' || text[valueStart] == '\t') {
			valueStart++
		}
		var value string
		startLine := line
		if valueStart < end && (text[valueStart] == '"' || text[valueStart] == '\'') {
			parsed, after, ok := parseQuoted(text, valueStart)
			if !ok {
				return nil, invalid("ligne %d : guillemet non fermé", startLine)
			}
			value = parsed
			line += strings.Count(text[valueStart:after], "\n")
			end = strings.IndexByte(text[after:], '\n')
			if end < 0 {
				end = len(text)
			} else {
				end += after
			}
			if tail := strings.TrimLeft(text[after:end], " \t"); tail != "" && tail[0] != '#' {
				return nil, invalid("ligne %d : texte inattendu après le guillemet fermant", line)
			}
		} else {
			value = text[valueStart:end]
			for i := 1; i < len(value); i++ {
				if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
					value = value[:i]
					break
				}
			}
			value = strings.TrimRight(value, " \t")
		}
		if at, seen := index[name]; seen {
			pairs[at].Value = value
		} else {
			index[name] = len(pairs)
			pairs = append(pairs, Pair{Name: name, Value: value})
		}
		pos, line = end+1, line+1
	}
	return pairs, nil
}

// parseQuoted reads the quoted value starting at text[start] and returns it
// with the offset just after its closing quote.
func parseQuoted(text string, start int) (string, int, bool) {
	quote := text[start]
	var value strings.Builder
	for i := start + 1; i < len(text); i++ {
		c := text[i]
		switch {
		case c == quote:
			return value.String(), i + 1, true
		case c == '\\' && quote == '"' && i+1 < len(text):
			i++
			switch text[i] {
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case '\\', '"', '$':
				value.WriteByte(text[i])
			default:
				value.WriteByte('\\')
				value.WriteByte(text[i])
			}
		default:
			value.WriteByte(c)
		}
	}
	return "", 0, false
}

var bareValue = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]*$`)

// FormatDotenv writes pairs as a dotenv text that ParseDotenv reads back
// unchanged. A value is written bare when it only has harmless characters,
// single-quoted (literal for Compose and the dotenv libraries) when it can
// be, and double-quoted with escapes otherwise.
func FormatDotenv(pairs []Pair) string {
	var out strings.Builder
	for _, pair := range pairs {
		out.WriteString(pair.Name)
		out.WriteByte('=')
		switch {
		case bareValue.MatchString(pair.Value):
			out.WriteString(pair.Value)
		case !strings.ContainsAny(pair.Value, "'\n\r"):
			out.WriteByte('\'')
			out.WriteString(pair.Value)
			out.WriteByte('\'')
		default:
			out.WriteByte('"')
			out.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`).Replace(pair.Value))
			out.WriteByte('"')
		}
		out.WriteByte('\n')
	}
	return out.String()
}
