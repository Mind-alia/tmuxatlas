package peer

import "testing"

func TestTokenEqual(t *testing.T) {
	for _, tt := range []struct {
		expected, provided string
		want               bool
	}{
		{"abc123", "abc123", true},
		{"abc123", "abc124", false},
		{"abc123", "abc12", false},
		{"abc123", "", false},
		{"", "", false},
	} {
		if got := tokenEqual(tt.expected, tt.provided); got != tt.want {
			t.Errorf("tokenEqual(%q, %q) = %v", tt.expected, tt.provided, got)
		}
	}
}
