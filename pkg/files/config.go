// Package files serves the files of the Unix account that runs the process,
// inside a jail rooted at its home directory. It is only ever run by that
// account's own TmuxAtlas process (its peer, or the hub for the hub's own
// host): one account never reads another account's files.
package files

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
)

// Mode is the local ceiling set by the account that runs the process. The hub
// can ask for less, never for more.
type Mode string

const (
	ModeOff       Mode = "off"
	ModeReadOnly  Mode = "ro"
	ModeReadWrite Mode = "rw"
)

const (
	// ChunkSize keeps each runtime message under the 64 KiB protocol bound
	// once base64-encoded.
	ChunkSize = 32 << 10
	// MaxFileSize bounds a whole read or write, across chunks.
	MaxFileSize = 8 << 20
	// MaxListEntries bounds a directory listing.
	MaxListEntries = 2000
	// listPageSize keeps one listing page under the protocol bound.
	listPageSize = 200
	// maxConcurrent bounds the operations running at once in one process.
	maxConcurrent = 2
)

// DefaultDeny lists paths never served, even in rw mode. A pattern without a
// slash matches any path component; a pattern with a slash matches that path
// from the root and everything below it.
var DefaultDeny = []string{
	".ssh", ".gnupg", ".config/tmuxatlas", ".env*", "*.pem", "*.key", ".docker",
	".netrc", ".git-credentials", ".pgpass", ".aws", ".kube", ".password-store",
	".local/share/keyrings", ".config/rclone", ".claude", ".bash_history",
	".config/gh", ".npmrc", ".local/share/tmuxatlas", ".local/state",
}

// ErrHubReadWrite refuses TMUXATLAS_FILES=rw on the hub, whose account runs
// the production: writing there from a browser is never needed.
var ErrHubReadWrite = errors.New("TMUXATLAS_FILES=rw est refusé sur le hub : utilisez ro ou off")

// CheckHub rejects a ceiling the hub's own host must not have.
func (c Config) CheckHub() error {
	if c.Mode == ModeReadWrite {
		return ErrHubReadWrite
	}
	return nil
}

type Config struct {
	Mode Mode
	Root string
	Deny []string
}

// ConfigFromEnv reads the ceiling from the process environment, which comes
// from the account's own ~/.config/tmuxatlas/.env: the hub cannot change it.
// Files are off unless the account enables them.
func ConfigFromEnv() (Config, error) {
	config := Config{Mode: ModeOff, Deny: append([]string{}, DefaultDeny...)}
	switch value := strings.TrimSpace(os.Getenv("TMUXATLAS_FILES")); value {
	case "", "off":
	case "ro":
		config.Mode = ModeReadOnly
	case "rw":
		config.Mode = ModeReadWrite
	default:
		return config, fmt.Errorf("TMUXATLAS_FILES=%q : off, ro ou rw attendu", value)
	}
	config.Root = strings.TrimSpace(os.Getenv("TMUXATLAS_FILES_ROOT"))
	if config.Root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config, err
		}
		config.Root = home
	}
	for _, pattern := range strings.Split(os.Getenv("TMUXATLAS_FILES_DENY"), ",") {
		if pattern = strings.Trim(strings.TrimSpace(pattern), "/"); pattern != "" {
			config.Deny = append(config.Deny, pattern)
		}
	}
	return config, nil
}

// denied reports whether a cleaned relative path, or one of its parents, is
// excluded.
func denied(rel string, patterns []string) bool {
	if rel == "." || rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") {
			if rel == pattern || strings.HasPrefix(rel, pattern+"/") {
				return true
			}
			continue
		}
		for _, part := range parts {
			if ok, _ := path.Match(pattern, part); ok {
				return true
			}
		}
	}
	return false
}

// holdsDenied reports whether rel is a parent of a path excluded by a
// pattern with a slash. Those patterns are anchored at the root: moving such
// a parent (".config" to "c") would expose "c/tmuxatlas" under a new name.
func holdsDenied(rel string, patterns []string) bool {
	if rel == "." || rel == "" {
		return true
	}
	for _, pattern := range patterns {
		if strings.Contains(pattern, "/") && strings.HasPrefix(pattern, rel+"/") {
			return true
		}
	}
	return false
}

// cleanPath turns a client path ("/", "/a/b", "a/b") into a path relative to
// the jail root. ".." cannot climb above the root.
func cleanPath(value string) (string, error) {
	if len(value) > 4096 || strings.ContainsRune(value, 0) {
		return "", errInvalid("chemin invalide")
	}
	rel := strings.TrimPrefix(path.Clean("/"+value), "/")
	if rel == "" {
		return ".", nil
	}
	return rel, nil
}
