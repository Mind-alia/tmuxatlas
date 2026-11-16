package peer

import "crypto/subtle"

// tokenEqual compares attachment tokens in constant time, so the time an
// answer takes reveals nothing about how much of a guess was right. An empty
// token never matches.
func tokenEqual(expected, provided string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}
