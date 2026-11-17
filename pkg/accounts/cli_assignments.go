package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrInvalidCLIPerson is returned when a CLI person name is empty or malformed.
var ErrInvalidCLIPerson = errors.New("nom de session CLI invalide")

// CLIAssignments persists which TmuxAtlas account owns each ai-accounts CLI
// person (for example "Aris" or "Aris2"). Keys are matched case-insensitively.
type CLIAssignments struct {
	mu   sync.Mutex
	path string
}

type cliAssignmentsFile struct {
	// Assignments maps a lowercased CLI person to a TmuxAtlas user ID.
	Assignments map[string]string `json:"assignments"`
}

// OpenCLIAssignments opens the assignment file at path. The file is created on
// the first write.
func OpenCLIAssignments(path string) *CLIAssignments { return &CLIAssignments{path: path} }

// CLIAssignments returns the CLI person assignment store kept next to the
// users file.
func (s *Store) CLIAssignments() *CLIAssignments {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cliAssignments == nil {
		s.cliAssignments = OpenCLIAssignments(filepath.Join(filepath.Dir(s.path), "cli-assignments.json"))
	}
	return s.cliAssignments
}

// NormalizeCLIPerson returns the lookup key used for a CLI person.
func NormalizeCLIPerson(person string) string {
	return strings.ToLower(strings.TrimSpace(person))
}

func validCLIPerson(key string) bool {
	if key == "" || len(key) > 80 {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (c *CLIAssignments) read() (map[string]string, error) {
	raw, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var file cliAssignmentsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("lecture des affectations CLI : %w", err)
	}
	if file.Assignments == nil {
		file.Assignments = map[string]string{}
	}
	return file.Assignments, nil
}

func (c *CLIAssignments) write(assignments map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cliAssignmentsFile{Assignments: assignments}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".cli-assignments-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), c.path)
}

// All returns a copy of every assignment, keyed by lowercased CLI person.
func (c *CLIAssignments) All() (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read()
}

// PersonsFor returns the set of lowercased CLI persons assigned to userID.
func (c *CLIAssignments) PersonsFor(userID string) (map[string]bool, error) {
	all, err := c.All()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for person, owner := range all {
		if owner == userID && userID != "" {
			out[person] = true
		}
	}
	return out, nil
}

// Set assigns person to userID. An empty userID removes the assignment.
func (c *CLIAssignments) Set(person, userID string) error {
	key := NormalizeCLIPerson(person)
	if !validCLIPerson(key) {
		return ErrInvalidCLIPerson
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	all, err := c.read()
	if err != nil {
		return err
	}
	if userID == "" {
		if _, ok := all[key]; !ok {
			return nil
		}
		delete(all, key)
	} else {
		all[key] = userID
	}
	return c.write(all)
}

// RemoveUser drops every assignment pointing to userID.
func (c *CLIAssignments) RemoveUser(userID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	all, err := c.read()
	if err != nil {
		return err
	}
	changed := false
	for person, owner := range all {
		if owner == userID {
			delete(all, person)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return c.write(all)
}
