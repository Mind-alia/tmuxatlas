// Package ops reads operational status that another Unix account publishes
// as JSON files in a shared directory (Docker containers, backups) and turns
// it into admin-only views and alerts.
//
// The hub never talks to Docker and never runs anything in the account that
// owns Docker: a collector there writes the files, the hub only reads them.
// See docs/ops.md and contrib/ops/.
package ops

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// MaxFileSize bounds every status file. The collector writes a few KiB.
const MaxFileSize = 256 << 10

// ErrMissing means the status file does not exist (yet).
var ErrMissing = errors.New("fichier de statut absent")

// Source is a read-only view of the status directory.
type Source struct {
	dir string
}

// NewSource returns a source reading files from dir.
func NewSource(dir string) *Source { return &Source{dir: dir} }

// Dir is the status directory.
func (s *Source) Dir() string { return s.dir }

// ReadJSON decodes dir/name into v and returns the file's modification time.
// It refuses symlinks, non-regular files (FIFO, device) and files above
// MaxFileSize, and only ever opens the file read-only.
func (s *Source) ReadJSON(name string, v any) (time.Time, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return time.Time{}, fmt.Errorf("nom de fichier invalide : %q", name)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return time.Time{}, ErrMissing
		}
		return time.Time{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return time.Time{}, fmt.Errorf("%s n'est pas un fichier ordinaire", name)
	}
	if info.Size() > MaxFileSize {
		return time.Time{}, fmt.Errorf("%s dépasse %d octets", name, MaxFileSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return time.Time{}, err
	}
	if len(data) > MaxFileSize {
		return time.Time{}, fmt.Errorf("%s dépasse %d octets", name, MaxFileSize)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return time.Time{}, fmt.Errorf("%s : JSON invalide : %w", name, err)
	}
	return info.ModTime(), nil
}
