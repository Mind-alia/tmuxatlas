package sudo

import (
	"strings"
	"testing"
)

func TestTypedSegments(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  []string
	}{
		{"one line", "frappe-humaine-1234\r", []string{"frappe-humaine-1234"}},
		{"short answers are kept", "y\rno\r", nil},
		{"backspace", "abcdX\x7f\r", []string{"abcd"}},
		{"ctrl-u clears", "wrong-line\x15good-line\r", []string{"good-line"}},
		{"no trailing enter", "pending-input", []string{"pending-input"}},
		{"control bytes dropped", "ab\x1bcd\r", []string{"abcd"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TypedSegments([]byte(tt.typed))
			if len(got) != len(tt.want) {
				t.Fatalf("segments = %q, want %q", got, tt.want)
			}
			for i := range got {
				if string(got[i]) != tt.want[i] {
					t.Fatalf("segment %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		segments []string
		want     string
		count    int
	}{
		{"removes typed text", "echo: frappe-humaine-1234\n", []string{"frappe-humaine-1234"}, "echo: " + RedactionMark + "\n", 1},
		{"every occurrence", "abcd abcd", []string{"abcd"}, RedactionMark + " " + RedactionMark, 2},
		{"short segment ignored", "yes yes", []string{"yes"}, "yes yes", 0},
		{"longest first", "secret-long", []string{"secr", "secret-long"}, RedactionMark, 1},
		{"untouched output", "all good", []string{"nothere"}, "all good", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			segments := make([][]byte, len(tt.segments))
			for i, s := range tt.segments {
				segments[i] = []byte(s)
			}
			got, count := Redact([]byte(tt.output), segments)
			if string(got) != tt.want || count != tt.count {
				t.Fatalf("Redact = %q (%d), want %q (%d)", got, count, tt.want, tt.count)
			}
			if strings.Contains(string(got), "frappe-humaine") {
				t.Fatal("typed text survived")
			}
		})
	}
}
