package dbbackup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Origins of a backup.
const (
	OriginManual     = "manual"
	OriginScheduled  = "scheduled"
	OriginPreRestore = "pre-restore"
)

// States of a stored backup.
const (
	StateOK     = "ok"
	StateFailed = "failed"
	// StateMissing: the record says ok but the file is gone.
	StateMissing = "missing"
)

const (
	dumpSuffix   = ".sql.gz"
	recordSuffix = ".json"
	idLayout     = "20060102T150405Z"
	// keptFailures bounds the records of failed runs kept per database.
	keptFailures = 5
)

var (
	// namePattern is Docker's own shape of a container name. It has no
	// separator and cannot be "." or "..": a name is safe as a directory.
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// idPattern is the UTC start time of a backup; it is also its file name.
	idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
)

// ValidName tells whether name can designate a container here.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidID tells whether id can designate a backup.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Record describes one backup run. It is stored next to the dump as
// <id>.json; a failed run keeps its record and no dump.
type Record struct {
	ID         string    `json:"id"`
	Container  string    `json:"container"`
	Engine     string    `json:"engine"`
	Origin     string    `json:"origin"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	// Size and SHA256 are those of the compressed file.
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Error  string `json:"error,omitempty"`
}

// store owns <root>/<container>/<id>.sql.gz and their records. Paths are
// only ever built here, from a validated name and a validated ID.
type store struct {
	root string
}

var errInvalidName = errors.New("nom de conteneur invalide")
var errInvalidID = errors.New("identifiant de sauvegarde invalide")

// ErrNotFound means the backup does not exist.
var ErrNotFound = errors.New("sauvegarde introuvable")

func (s *store) dir(name string) (string, error) {
	if !ValidName(name) {
		return "", errInvalidName
	}
	return filepath.Join(s.root, name), nil
}

func (s *store) paths(name, id string) (dump, record string, err error) {
	dir, err := s.dir(name)
	if err != nil {
		return "", "", err
	}
	if !ValidID(id) {
		return "", "", errInvalidID
	}
	return filepath.Join(dir, id+dumpSuffix), filepath.Join(dir, id+recordSuffix), nil
}

// ensureDir creates the directory of a container, private to the hub.
func (s *store) ensureDir(name string) (string, error) {
	dir, err := s.dir(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("création du dossier de sauvegarde : %w", err)
	}
	// MkdirAll keeps the mode of directories that already exist.
	for _, path := range []string{s.root, dir} {
		if err := os.Chmod(path, 0o700); err != nil {
			return "", fmt.Errorf("droits du dossier de sauvegarde : %w", err)
		}
	}
	return dir, nil
}

// newID picks the ID of a run that starts at now, later by a second while
// one of that name exists.
func (s *store) newID(name string, now time.Time) (string, error) {
	for attempt := 0; attempt < 120; attempt++ {
		id := now.UTC().Add(time.Duration(attempt) * time.Second).Format(idLayout)
		dump, record, err := s.paths(name, id)
		if err != nil {
			return "", err
		}
		if !exists(dump) && !exists(record) && !exists(tempPath(dump)) {
			return id, nil
		}
	}
	return "", errors.New("trop de sauvegardes dans la même minute")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// tempPath is where a dump is written before it is complete.
func tempPath(dump string) string {
	return filepath.Join(filepath.Dir(dump), "."+filepath.Base(dump)+".tmp")
}

// writeRecord stores a record atomically.
func (s *store) writeRecord(rec Record) error {
	_, path, err := s.paths(rec.Container, rec.ID)
	if err != nil {
		return err
	}
	if _, err := s.ensureDir(rec.Container); err != nil {
		return err
	}
	return writeJSONFile(path, rec)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temp, 0o600); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// get returns one record and the path of its dump.
func (s *store) get(name, id string) (Record, string, error) {
	dump, recordPath, err := s.paths(name, id)
	if err != nil {
		return Record{}, "", err
	}
	rec, err := readRecord(recordPath, name, id)
	if err != nil {
		return Record{}, "", err
	}
	if rec.State == StateOK {
		if info, err := os.Lstat(dump); err != nil || !info.Mode().IsRegular() {
			rec.State = StateMissing
		}
	}
	return rec, dump, nil
}

func readRecord(path, name, id string) (Record, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return Record{}, ErrNotFound
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, ErrNotFound
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, ErrNotFound
	}
	// The directory and the file name are the identity, whatever the
	// content says.
	rec.Container, rec.ID = name, id
	if rec.State != StateOK {
		rec.State = StateFailed
	}
	return rec, nil
}

// list returns the records of a container, newest first.
func (s *store) list(name string) ([]Record, error) {
	dir, err := s.dir(name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), recordSuffix)
		if !ok || !ValidID(id) {
			continue
		}
		if rec, _, err := s.get(name, id); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// containers lists the names that have a directory here.
func (s *store) containers() []string {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && ValidName(entry.Name()) {
			out = append(out, entry.Name())
		}
	}
	return out
}

// remove deletes a backup: its dump, then its record.
func (s *store) remove(name, id string) error {
	dump, record, err := s.paths(name, id)
	if err != nil {
		return err
	}
	if !exists(record) && !exists(dump) {
		return ErrNotFound
	}
	if err := os.Remove(dump); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// prune keeps the keep newest complete backups of a container and the last
// few records of failed runs. It returns the IDs it deleted.
func (s *store) prune(name string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, nil
	}
	records, err := s.list(name)
	if err != nil {
		return nil, err
	}
	var deleted []string
	complete, failed := 0, 0
	for _, rec := range records {
		drop := false
		if rec.State == StateOK {
			complete++
			drop = complete > keep
		} else {
			failed++
			drop = failed > keptFailures
		}
		if !drop {
			continue
		}
		if err := s.remove(name, rec.ID); err != nil {
			return deleted, err
		}
		deleted = append(deleted, rec.ID)
	}
	return deleted, nil
}

// sweepTemp removes the unfinished dumps a stopped hub left behind.
func (s *store) sweepTemp() {
	for _, name := range s.containers() {
		dir, _ := s.dir(name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".tmp") {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
}

// freeBytes is the space an unprivileged process may still use under dir.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
