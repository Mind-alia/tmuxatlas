package loops

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultFallbackCooldown = 15 * time.Minute
	minRateLimitCooldown    = 30 * time.Second
	maxRateLimitCooldown    = 6 * time.Hour
)

var (
	// Strong banners that clearly indicate a CLI/provider limit even on exit 0.
	strongRateLimitBanner = regexp.MustCompile(`(?i)(?:you(?:'ve| have) hit your (?:\d+[- ]hour |usage )?limit|usage limit reached|5[- ]hour limit|rate_limit_error|resource_exhausted|exceeded your current quota)`)

	// General rate/usage limit signals checked when exitCode != 0.
	generalRateLimitSignal = regexp.MustCompile(`(?i)(?:you(?:'ve| have) hit your (?:\d+[- ]hour |usage )?limit|usage limit|5[- ]hour limit|five[- ]hour limit|rate[-_ ]limit|ratelimit|too many requests|resource_exhausted|quota exceeded|exceeded your current quota|\b(?:http|status|error)[: ]+429\b|\b429 too many requests\b)`)

	// Pipe-delimited unix epoch (e.g. "Claude AI usage limit reached|1731772800").
	pipeEpochPattern = regexp.MustCompile(`\|(\d{10})\b`)

	// Relative duration following reset/retry/wait keywords.
	relativePhrasePattern = regexp.MustCompile(`(?i)(?:resets?|try again|retry|wait|available)\s+(?:in\s+)?((?:\d+\s*(?:h(?:ours?|rs?)?|m(?:in(?:ute)?s?)?|s(?:ec(?:ond)?s?)?)\s*,?\s*)+)`)
	durationPartPattern   = regexp.MustCompile(`(?i)(\d+)\s*(h(?:ours?|rs?)?|m(?:in(?:ute)?s?)?|s(?:ec(?:ond)?s?)?)`)

	// Clock time following reset/at/until keywords, with optional "(Area/Location)" or TZ.
	clockPhrasePattern = regexp.MustCompile(`(?i)(?:resets?(?:\s+at)?|continuing automatically at|try again at|available at|until)\s+(\d{1,2}(?::\d{2})?\s*(?:am|pm)?)(?:\s*\(([^)]+)\)|\s+(UTC|GMT))?`)
)

// detectRateLimit reports whether CLI output indicates a 5-hour / usage / rate
// limit and returns the estimated reset timestamp.
func detectRateLimit(output string, exitCode int, changedFiles int, doneSignal bool, now time.Time) (bool, time.Time, string) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return false, time.Time{}, ""
	}
	// Look primarily at the tail so code printed earlier in a pass cannot false-trigger.
	tail := trimmed
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}

	if exitCode == 0 {
		// Only treat exit 0 as rate-limited if the output is short and purely a limit banner.
		if changedFiles > 0 || doneSignal || len(trimmed) > 1200 || !strongRateLimitBanner.MatchString(tail) {
			return false, time.Time{}, ""
		}
	} else if !generalRateLimitSignal.MatchString(tail) {
		return false, time.Time{}, ""
	}

	resetAt := parseResetTime(tail, now)
	reason := extractRateLimitLine(tail)
	return true, resetAt, reason
}

func extractRateLimitLine(tail string) string {
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && generalRateLimitSignal.MatchString(line) {
			if len(line) > 180 {
				return line[:180] + "…"
			}
			return line
		}
	}
	return "Limite d'utilisation / 5h atteinte"
}

func parseResetTime(text string, now time.Time) time.Time {
	if m := pipeEpochPattern.FindStringSubmatch(text); len(m) == 2 {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			t := time.Unix(sec, 0)
			if t.After(now) && t.Sub(now) <= maxRateLimitCooldown {
				return clampResetAt(t, now)
			}
		}
	}

	if m := relativePhrasePattern.FindStringSubmatch(text); len(m) >= 2 {
		if d := parseCompoundDuration(m[1]); d > 0 {
			return clampResetAt(now.Add(d), now)
		}
	}

	if m := clockPhrasePattern.FindStringSubmatch(text); len(m) >= 2 {
		clockStr := strings.TrimSpace(m[1])
		tzName := strings.TrimSpace(m[2])
		if tzName == "" {
			tzName = strings.TrimSpace(m[3])
		}
		if t, ok := parseClockTime(clockStr, tzName, now); ok {
			return clampResetAt(t, now)
		}
	}

	return now.Add(defaultFallbackCooldown)
}

func parseCompoundDuration(raw string) time.Duration {
	matches := durationPartPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0
	}
	var total time.Duration
	for _, m := range matches {
		val, err := strconv.Atoi(m[1])
		if err != nil || val < 0 {
			continue
		}
		unit := strings.ToLower(m[2])
		switch {
		case strings.HasPrefix(unit, "h"):
			total += time.Duration(val) * time.Hour
		case strings.HasPrefix(unit, "m"):
			total += time.Duration(val) * time.Minute
		case strings.HasPrefix(unit, "s"):
			total += time.Duration(val) * time.Second
		}
	}
	return total
}

func parseClockTime(clockStr, tzName string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	if tzName != "" {
		if loaded, err := time.LoadLocation(tzName); err == nil {
			loc = loaded
		}
	}

	norm := strings.ToLower(strings.Join(strings.Fields(clockStr), ""))
	var hour, minute int
	var err error
	isPM := strings.HasSuffix(norm, "pm")
	isAM := strings.HasSuffix(norm, "am")
	core := strings.TrimSuffix(strings.TrimSuffix(norm, "pm"), "am")

	if strings.Contains(core, ":") {
		parts := strings.SplitN(core, ":", 2)
		hour, err = strconv.Atoi(parts[0])
		if err != nil {
			return time.Time{}, false
		}
		minute, err = strconv.Atoi(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	} else {
		hour, err = strconv.Atoi(core)
		if err != nil {
			return time.Time{}, false
		}
		minute = 0
	}

	if minute < 0 || minute > 59 {
		return time.Time{}, false
	}
	if isAM || isPM {
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		if isAM && hour == 12 {
			hour = 0
		} else if isPM && hour != 12 {
			hour += 12
		}
	} else if hour < 0 || hour > 23 {
		return time.Time{}, false
	}

	ref := now.In(loc)
	candidate := time.Date(ref.Year(), ref.Month(), ref.Day(), hour, minute, 0, 0, loc)
	if !candidate.After(ref.Add(-1 * time.Minute)) {
		candidate = candidate.Add(24 * time.Hour)
	} else if !candidate.After(ref) {
		candidate = ref.Add(minRateLimitCooldown)
	}
	return candidate, true
}

func clampResetAt(t, now time.Time) time.Time {
	if t.Before(now.Add(minRateLimitCooldown)) {
		return now.Add(minRateLimitCooldown)
	}
	if t.After(now.Add(maxRateLimitCooldown)) {
		return now.Add(maxRateLimitCooldown)
	}
	return t
}
