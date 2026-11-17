package dockerui

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseLogStream(t *testing.T) {
	t.Run("multiplexed", func(t *testing.T) {
		var body []byte
		body = append(body, frame(1, "2026-11-17T08:00:00Z first\n")...)
		body = append(body, frame(2, "2026-11-17T08:00:01Z oops\r\n")...)
		body = append(body, frame(3, "2026-11-17T08:00:02Z daemon error\n")...)
		body = append(body, frame(1, "2026-11-17T08:00:03Z progress 10%\rprogress 100%\n2026-11-17T08:00:04Z two\n")...)
		body = append(body, frame(1, "not a timestamp\x07 bell")...)
		lines, truncated, err := parseLogStream(bytes.NewReader(body), true)
		if err != nil || truncated {
			t.Fatalf("err=%v truncated=%v", err, truncated)
		}
		want := []LogLine{
			{T: "2026-11-17T08:00:00Z", S: "out", M: "first"},
			{T: "2026-11-17T08:00:01Z", S: "err", M: "oops"},
			{T: "2026-11-17T08:00:02Z", S: "err", M: "daemon error"},
			{T: "2026-11-17T08:00:03Z", S: "out", M: "progress 100%"},
			{T: "2026-11-17T08:00:04Z", S: "out", M: "two"},
			{S: "out", M: "not a timestamp bell"},
		}
		if len(lines) != len(want) {
			t.Fatalf("lines: %+v", lines)
		}
		for i := range want {
			if lines[i] != want[i] {
				t.Errorf("line %d = %+v, want %+v", i, lines[i], want[i])
			}
		}
	})

	t.Run("a header never sizes a buffer", func(t *testing.T) {
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], 0xFFFFFFFF)
		body := append(header, []byte("2026-11-17T08:00:00Z short\n")...)
		lines, truncated, err := parseLogStream(bytes.NewReader(body), true)
		if err != nil || len(lines) != 1 || lines[0].M != "short" || truncated {
			t.Fatalf("lines=%+v truncated=%v err=%v", lines, truncated, err)
		}
	})

	t.Run("byte cap", func(t *testing.T) {
		line := "2026-11-17T08:00:00Z " + strings.Repeat("x", 1000) + "\n"
		var body []byte
		for len(body) < MaxLogBytes+50_000 {
			body = append(body, frame(1, line)...)
		}
		lines, truncated, err := parseLogStream(bytes.NewReader(body), true)
		if err != nil || !truncated || len(lines) > MaxLogLines {
			t.Fatalf("lines=%d truncated=%v err=%v", len(lines), truncated, err)
		}
		size := 0
		for _, l := range lines {
			size += len(l.M)
		}
		if size > MaxLogBytes {
			t.Fatalf("%d bytes kept", size)
		}
	})

	t.Run("line cap keeps the newest lines", func(t *testing.T) {
		var body bytes.Buffer
		for i := 0; i < MaxLogLines+10; i++ {
			body.WriteString("2026-11-17T08:00:00Z l\n")
		}
		body.WriteString("2026-11-17T08:00:09Z last\n")
		lines, truncated, err := parseLogStream(&body, false)
		if err != nil || !truncated || len(lines) != MaxLogLines || lines[len(lines)-1].M != "last" {
			t.Fatalf("lines=%d truncated=%v err=%v", len(lines), truncated, err)
		}
	})

	t.Run("long lines are cut", func(t *testing.T) {
		lines, _, _ := parseLogStream(strings.NewReader("2026-11-17T08:00:00Z "+strings.Repeat("é", maxLogLineBytes)+"\n"), false)
		if len(lines) != 1 || len(lines[0].M) > maxLogLineBytes+8 || !strings.HasSuffix(lines[0].M, " …") {
			t.Fatalf("len=%d", len(lines[0].M))
		}
	})
}

func TestNextSince(t *testing.T) {
	lines := []LogLine{{T: "2026-11-17T08:00:00.999999999Z"}, {M: "no time"}}
	if got := nextSince(lines, "1"); got != "1794902401.000000000" {
		t.Fatalf("got %q", got)
	}
	if got := nextSince(nil, "1794902401.5"); got != "1794902401.5" {
		t.Fatalf("no line keeps the previous since: %q", got)
	}
	if !sincePattern.MatchString("1794902401.000000000") || sincePattern.MatchString("-1") {
		t.Fatal("since pattern")
	}
}

func TestSanitizeInspect(t *testing.T) {
	doc := map[string]any{
		"Id":   webID,
		"Path": "/usr/local/bin/server",
		"Args": []any{"--db", "postgres://app:hunter2@db:5432/app", "--admin-password", "pa55", "--token=tok3n", "--port", "80"},
		"Config": map[string]any{
			"Env":        []any{"DATABASE_URL=postgres://u:p@db/x", "DEBUG=1", "NOVALUE", 42},
			"Cmd":        []any{"redis-server", "--requirepass", "r3dis"},
			"Entrypoint": []any{"sh", "-c", "PGPASSWORD=pgs3cret psql -h db"},
			"Healthcheck": map[string]any{
				"Test": []any{"CMD-SHELL", "curl -fsS http://admin:adm1n@localhost/health"},
			},
			"Labels": map[string]any{
				"traefik.http.middlewares.auth.basicauth.users": "admin:$apr1$hash",
				"com.example.api-key":                           "k3y",
				"org.opencontainers.image.authors":              "Jane",
				"com.docker.compose.service":                    "web",
			},
		},
		"HostConfig": map[string]any{
			"LogConfig": map[string]any{"Type": "splunk", "Config": map[string]any{"splunk-token": "spl", "splunk-url": "https://splunk"}},
			"Memory":    json.Number("0"),
		},
		"State": map[string]any{"Health": map[string]any{"Log": []any{map[string]any{"Output": "SECRET_KEY=leak ok"}}}},
	}
	raw, _ := json.Marshal(doc)
	sanitized, err := sanitizeInspect(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(sanitized)
	for _, secret := range []string{"hunter2", "pa55", "tok3n", "r3dis", "pgs3cret", "adm1n", "$apr1$hash", "k3y", "spl\"", "leak", "u:p@", "DEBUG=1"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q survived: %s", secret, text)
		}
	}
	for _, kept := range []string{
		`"DATABASE_URL=••••"`, `"DEBUG=••••"`, `"NOVALUE"`, `"--port","80"`, `"Jane"`, `"web"`, `"https://splunk"`,
		`postgres://app:••••@db:5432/app`, `"--admin-password","••••"`, `"--token=••••"`, `"--requirepass","••••"`,
		`PGPASSWORD=•••• psql -h db`, `"Memory":0`, `"Path":"/usr/local/bin/server"`,
	} {
		if !strings.Contains(text, kept) {
			t.Errorf("missing %s in %s", kept, text)
		}
	}
	if _, err := sanitizeInspect([]byte("not json")); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestComputeStats(t *testing.T) {
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	raw := apiStats{Read: now.Format(time.RFC3339Nano)}
	raw.CPUStats.CPUUsage.TotalUsage = 3_000_000_000
	raw.CPUStats.SystemUsage = 40_000_000_000
	raw.CPUStats.CPUUsage.PercpuUsage = []uint64{1, 2}
	raw.MemoryStats.Usage = 1000
	raw.MemoryStats.Stats = map[string]uint64{"total_inactive_file": 300, "inactive_file": 100}

	view, sample := computeStats(raw, nil, 8, now)
	if view.CPUPercent != nil || sample.online != 2 || view.MemoryUsage != 700 || view.At != now.UnixMilli() {
		t.Fatalf("first sample without Docker's previous one: %+v %+v", view, sample)
	}
	// An engine that ignores one-shot sends its own previous sample.
	twoCycles := raw
	twoCycles.PreCPUStats.CPUUsage.TotalUsage = 2_500_000_000
	twoCycles.PreCPUStats.SystemUsage = 35_000_000_000
	if view, _ := computeStats(twoCycles, nil, 8, now); view.CPUPercent == nil || *view.CPUPercent < 19.999 || *view.CPUPercent > 20.001 {
		t.Fatalf("Docker's previous sample: %v", view.CPUPercent)
	}
	prev := cpuSample{total: 2_000_000_000, system: 30_000_000_000, online: 2, at: now.Add(-3 * time.Second)}
	view, _ = computeStats(raw, &prev, 8, now)
	if view.CPUPercent == nil || *view.CPUPercent < 19.999 || *view.CPUPercent > 20.001 {
		t.Fatalf("1 s of CPU over 10 s of host time × 2 CPUs: %v", view.CPUPercent)
	}
	restarted := cpuSample{total: 9_000_000_000, system: 30_000_000_000, at: prev.at}
	if view, _ := computeStats(raw, &restarted, 8, now); view.CPUPercent != nil {
		t.Fatalf("a counter that went back must not give a value: %v", *view.CPUPercent)
	}
	raw.CPUStats.SystemUsage = 0
	noHost := cpuSample{total: 1_500_000_000, at: now.Add(-3 * time.Second)}
	if view, _ := computeStats(raw, &noHost, 8, now); view.CPUPercent == nil || *view.CPUPercent < 49.9 || *view.CPUPercent > 50.1 {
		t.Fatalf("wall-time fallback: %v", view.CPUPercent)
	}
	raw.CPUStats.CPUUsage.PercpuUsage = nil
	raw.MemoryStats.Stats = map[string]uint64{"inactive_file": 100}
	view, sample = computeStats(raw, nil, 8, now)
	if sample.online != 8 || view.MemoryUsage != 900 {
		t.Fatalf("fallbacks: %+v %+v", view, sample)
	}
}
