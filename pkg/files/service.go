package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Error is a file operation failure with a stable code that the hub maps to
// an HTTP status. Messages are safe to show to the user.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

const (
	CodeDisabled = "disabled"
	CodeReadOnly = "read-only"
	CodeDenied   = "denied"
	CodeNotFound = "not-found"
	CodeInvalid  = "invalid"
	CodeConflict = "conflict"
	CodeTooLarge = "too-large"
	CodeBusy     = "busy"
	CodeOffline  = "offline"
	CodeFailed   = "failed"
)

func errInvalid(message string) *Error { return &Error{Code: CodeInvalid, Message: message} }

var (
	errDenied   = &Error{Code: CodeDenied, Message: "ce chemin est protégé et n'est jamais servi"}
	errNotFound = &Error{Code: CodeNotFound, Message: "fichier ou dossier introuvable"}
	errSymlink  = &Error{Code: CodeDenied, Message: "les liens symboliques ne sont pas suivis"}
	errSpecial  = &Error{Code: CodeDenied, Message: "seuls les fichiers ordinaires et les dossiers sont servis"}
	errTooLarge = &Error{Code: CodeTooLarge, Message: fmt.Sprintf("fichier trop volumineux (%d Mio au plus)", MaxFileSize>>20)}
)

var uploadIDPattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

// Service executes file operations for the account that runs this process.
type Service struct {
	config Config
	root   *os.Root
	slots  chan struct{}
	mu     sync.Mutex
}

// NewService opens the jail. With mode off it still answers "info", so the
// hub can show why files are unavailable.
func NewService(config Config) (*Service, error) {
	service := &Service{config: config, slots: make(chan struct{}, maxConcurrent)}
	if config.Mode == ModeOff {
		return service, nil
	}
	root, err := os.OpenRoot(config.Root)
	if err != nil {
		return nil, fmt.Errorf("ouverture de la racine des fichiers %s : %w", config.Root, err)
	}
	service.root = root
	return service, nil
}

func (s *Service) Mode() Mode {
	if s == nil {
		return ModeOff
	}
	return s.config.Mode
}

// Close releases the jail.
func (s *Service) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

type Entry struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
	Mode  string    `json:"mode"`
}

func entryType(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "dir"
	case mode&fs.ModeSymlink != 0:
		return "symlink"
	case mode.IsRegular():
		return "file"
	}
	return "other"
}

// Version identifies a file's content for conflict detection.
func Version(info fs.FileInfo) string {
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}

// Execute runs one operation. payload is the operation's JSON object.
func (s *Service) Execute(ctx context.Context, operation string, payload json.RawMessage) (json.RawMessage, error) {
	if s == nil {
		return nil, &Error{Code: CodeDisabled, Message: "fichiers désactivés sur cet hôte"}
	}
	if operation == "info" {
		return json.Marshal(map[string]any{"mode": s.config.Mode, "root": s.config.Root, "chunk_size": ChunkSize, "max_file_size": MaxFileSize})
	}
	if s.config.Mode == ModeOff || s.root == nil {
		return nil, &Error{Code: CodeDisabled, Message: "fichiers désactivés sur cet hôte (TMUXATLAS_FILES=off)"}
	}
	write := operation == "write" || operation == "mkdir" || operation == "rename" || operation == "delete"
	if write && s.config.Mode != ModeReadWrite {
		return nil, &Error{Code: CodeReadOnly, Message: "cet hôte ne sert ses fichiers qu'en lecture"}
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, &Error{Code: CodeBusy, Message: "trop d'opérations en cours"}
	}
	var request struct {
		Path     string `json:"path"`
		To       string `json:"to"`
		Offset   int64  `json:"offset"`
		Length   int    `json:"length"`
		Data     string `json:"data"`
		UploadID string `json:"upload_id"`
		Final    bool   `json:"final"`
		Abort    bool   `json:"abort"`
		Replace  bool   `json:"replace"`
		Version  string `json:"version"`
		Actor    string `json:"actor"`
	}
	if len(payload) > 0 && json.Unmarshal(payload, &request) != nil {
		return nil, errInvalid("requête invalide")
	}
	var result any
	var err error
	switch operation {
	case "list":
		result, err = s.list(request.Path, int(request.Offset))
	case "stat":
		result, err = s.stat(request.Path)
	case "read":
		result, err = s.read(request.Path, request.Offset, request.Length)
	case "write":
		result, err = s.write(request.Path, request.UploadID, request.Offset, request.Data, request.Final, request.Abort, request.Replace, request.Version)
	case "mkdir":
		result, err = s.mkdir(request.Path)
	case "rename":
		result, err = s.rename(request.Path, request.To)
	case "delete":
		result, err = s.remove(request.Path)
	default:
		return nil, errInvalid("opération inconnue")
	}
	if write && (operation != "write" || request.Final) {
		entry := logrus.WithFields(logrus.Fields{"component": "files", "operation": operation, "path": request.Path, "actor": request.Actor})
		if request.To != "" {
			entry = entry.WithField("to", request.To)
		}
		if err != nil {
			entry.WithError(err).Warn("file change refused")
		} else {
			entry.Info("file changed")
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// resolve cleans a path, applies the deny list and refuses any symbolic link
// on the way, so a link cannot reach a protected file or leave the jail. The
// os.Root jail is a second barrier against "..", absolute links and races.
func (s *Service) resolve(value string) (string, error) {
	rel, err := cleanPath(value)
	if err != nil {
		return "", err
	}
	if denied(rel, s.config.Deny) {
		return "", errDenied
	}
	if rel == "." {
		return rel, nil
	}
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		info, err := s.root.Lstat(strings.Join(parts[:i], "/"))
		if err != nil {
			return "", mapError(err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", errSymlink
		}
		if !info.IsDir() {
			return "", errNotFound
		}
	}
	return rel, nil
}

func mapError(err error) error {
	var fileError *Error
	switch {
	case errors.As(err, &fileError):
		return err
	case errors.Is(err, fs.ErrNotExist):
		return errNotFound
	case errors.Is(err, fs.ErrExist):
		return &Error{Code: CodeConflict, Message: "ce nom existe déjà"}
	case errors.Is(err, fs.ErrPermission):
		return &Error{Code: CodeDenied, Message: "permission refusée par le système"}
	}
	if strings.Contains(err.Error(), "path escapes") {
		return &Error{Code: CodeDenied, Message: "ce chemin sort de la racine"}
	}
	return &Error{Code: CodeFailed, Message: "opération impossible"}
}

func (s *Service) list(value string, offset int) (any, error) {
	rel, err := s.resolve(value)
	if err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(rel)
	if err != nil {
		return nil, mapError(err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, errSymlink
	}
	if !info.IsDir() {
		return nil, errInvalid("ce chemin n'est pas un dossier")
	}
	dir, err := s.root.Open(rel)
	if err != nil {
		return nil, mapError(err)
	}
	defer dir.Close()
	raw, err := dir.ReadDir(MaxListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapError(err)
	}
	truncated := len(raw) > MaxListEntries
	if truncated {
		raw = raw[:MaxListEntries]
	}
	entries := make([]Entry, 0, len(raw))
	for _, item := range raw {
		child := item.Name()
		if rel != "." {
			child = rel + "/" + child
		}
		if denied(child, s.config.Deny) || strings.HasPrefix(item.Name(), uploadPrefix) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		entries = append(entries, Entry{Name: item.Name(), Type: entryType(info.Mode()), Size: info.Size(), MTime: info.ModTime(), Mode: info.Mode().Perm().String()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Name < entries[j].Name
	})
	total := len(entries)
	if offset < 0 || offset > total {
		offset = total
	}
	end := min(offset+listPageSize, total)
	return map[string]any{
		"path": "/" + strings.TrimPrefix(rel, "."), "entries": entries[offset:end],
		"total": total, "next": map[bool]int{true: end, false: -1}[end < total], "truncated": truncated,
	}, nil
}

type statResult struct {
	Entry
	Path     string `json:"path"`
	Version  string `json:"version"`
	Writable bool   `json:"writable"`
}

func (s *Service) regular(value string) (string, fs.FileInfo, error) {
	rel, err := s.resolve(value)
	if err != nil {
		return "", nil, err
	}
	info, err := s.root.Lstat(rel)
	if err != nil {
		return "", nil, mapError(err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", nil, errSymlink
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return "", nil, errSpecial
	}
	return rel, info, nil
}

func (s *Service) stat(value string) (any, error) {
	rel, info, err := s.regular(value)
	if err != nil {
		return nil, err
	}
	return statResult{
		Entry: Entry{Name: path.Base(rel), Type: entryType(info.Mode()), Size: info.Size(), MTime: info.ModTime(), Mode: info.Mode().Perm().String()},
		Path:  "/" + strings.TrimPrefix(rel, "."), Version: Version(info), Writable: s.config.Mode == ModeReadWrite && info.Mode().Perm()&0o200 != 0,
	}, nil
}

func (s *Service) read(value string, offset int64, length int) (any, error) {
	rel, info, err := s.regular(value)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, errInvalid("ce chemin est un dossier")
	}
	if info.Size() > MaxFileSize {
		return nil, errTooLarge
	}
	if length <= 0 || length > ChunkSize {
		length = ChunkSize
	}
	if offset < 0 || offset > info.Size() {
		return nil, errInvalid("position invalide")
	}
	file, err := s.root.OpenFile(rel, os.O_RDONLY|nofollow, 0)
	if err != nil {
		return nil, mapError(err)
	}
	defer file.Close()
	// Re-check the opened file: it must still be the same regular file.
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(opened, info) {
		return nil, &Error{Code: CodeConflict, Message: "le fichier a changé pendant la lecture"}
	}
	buffer := make([]byte, length)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapError(err)
	}
	return map[string]any{
		"data": base64.StdEncoding.EncodeToString(buffer[:n]), "size": opened.Size(),
		"version": Version(opened), "eof": offset+int64(n) >= opened.Size(),
	}, nil
}

const uploadPrefix = ".tmuxatlas-upload-"

// write stores one chunk of an upload in a private temporary file next to the
// target, then renames it over the target atomically on the final chunk.
func (s *Service) write(value, uploadID string, offset int64, data string, final, abort, replace bool, version string) (any, error) {
	if !uploadIDPattern.MatchString(uploadID) {
		return nil, errInvalid("identifiant d'envoi invalide")
	}
	rel, err := s.resolve(value)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, errInvalid("chemin invalide")
	}
	temporary := path.Join(path.Dir(rel), uploadPrefix+uploadID)
	if abort {
		_ = s.root.Remove(temporary)
		return map[string]any{"aborted": true}, nil
	}
	chunk, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(chunk) > ChunkSize {
		return nil, errInvalid("morceau invalide")
	}
	if offset < 0 || offset+int64(len(chunk)) > MaxFileSize {
		_ = s.root.Remove(temporary)
		return nil, errTooLarge
	}
	flags := os.O_WRONLY | nofollow
	if offset == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := s.root.OpenFile(temporary, flags, 0o600)
	if err != nil {
		return nil, mapError(err)
	}
	info, err := file.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Size() != offset) {
		err = &Error{Code: CodeConflict, Message: "morceau hors séquence"}
	}
	if err == nil {
		_, err = file.WriteAt(chunk, offset)
	}
	if err == nil && final {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = s.root.Remove(temporary)
		return nil, mapError(err)
	}
	if !final {
		return map[string]any{"received": offset + int64(len(chunk))}, nil
	}
	mode := fs.FileMode(0o644)
	if existing, err := s.root.Lstat(rel); err == nil {
		switch {
		case !existing.Mode().IsRegular():
			err = errSpecial
		case !replace:
			err = &Error{Code: CodeConflict, Message: "un fichier porte déjà ce nom"}
		case version != "" && version != Version(existing):
			err = &Error{Code: CodeConflict, Message: "le fichier a été modifié entre-temps"}
		}
		if err != nil {
			_ = s.root.Remove(temporary)
			return nil, err
		}
		mode = existing.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		_ = s.root.Remove(temporary)
		return nil, mapError(err)
	}
	if err := s.root.Chmod(temporary, mode); err != nil {
		_ = s.root.Remove(temporary)
		return nil, mapError(err)
	}
	if err := s.root.Rename(temporary, rel); err != nil {
		_ = s.root.Remove(temporary)
		return nil, mapError(err)
	}
	return s.stat(rel)
}

func (s *Service) mkdir(value string) (any, error) {
	rel, err := s.resolve(value)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, errInvalid("chemin invalide")
	}
	if err := s.root.Mkdir(rel, 0o755); err != nil {
		return nil, mapError(err)
	}
	return s.stat(rel)
}

func (s *Service) rename(from, to string) (any, error) {
	source, err := s.resolve(from)
	if err != nil {
		return nil, err
	}
	target, err := s.resolve(to)
	if err != nil {
		return nil, err
	}
	if source == "." || target == "." {
		return nil, errInvalid("chemin invalide")
	}
	if holdsDenied(source, s.config.Deny) {
		return nil, errDenied
	}
	info, err := s.root.Lstat(source)
	if err != nil {
		return nil, mapError(err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		return nil, errSpecial
	}
	if _, err := s.root.Lstat(target); err == nil {
		return nil, &Error{Code: CodeConflict, Message: "ce nom existe déjà"}
	}
	if err := s.root.Rename(source, target); err != nil {
		return nil, mapError(err)
	}
	return map[string]any{"path": "/" + target}, nil
}

// remove deletes a file, a link (not its target) or an empty folder.
func (s *Service) remove(value string) (any, error) {
	rel, err := s.resolve(value)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return nil, errInvalid("la racine ne peut pas être supprimée")
	}
	if err := s.root.Remove(rel); err != nil {
		if info, statErr := s.root.Lstat(rel); statErr == nil && info.IsDir() {
			return nil, &Error{Code: CodeConflict, Message: "le dossier n'est pas vide"}
		}
		return nil, mapError(err)
	}
	return map[string]any{"deleted": true}, nil
}
