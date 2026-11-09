// Package files serves the file explorer: it lists folders, reads and writes
// text files, streams raw bytes for previews and runs simple file operations
// on the host that runs TmuxAtlas. The operating system enforces permissions:
// the explorer can reach what the TmuxAtlas process can reach.
package files

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	// MaxEditSize is the largest file the editor opens or saves.
	MaxEditSize = 5 << 20
	// MaxEntries bounds one listing so a huge folder cannot exhaust memory.
	MaxEntries = 10000
	// sniffSize is how much of a file decides whether it is text.
	sniffSize = 8 << 10
	// DefaultMaxUpload bounds one uploaded file unless TMUXATLAS_FILES_UPLOAD_MAX says otherwise.
	DefaultMaxUpload = 4 << 30
)

// MaxUpload reads TMUXATLAS_FILES_UPLOAD_MAX in megabytes.
func MaxUpload(value string) int64 {
	if mb, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && mb > 0 {
		return mb << 20
	}
	return DefaultMaxUpload
}

// Enabled reads TMUXATLAS_FILES. The explorer stays on unless explicitly disabled.
func Enabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "false", "0", "no":
		return false
	default:
		return true
	}
}

// Entry is one item of a folder listing.
type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Type    string `json:"type"` // dir, file, symlink, other
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mtime"` // Unix milliseconds
	Hidden  bool   `json:"hidden"`
	// Target is where a symlink points; LinkDir tells whether it leads to a folder.
	Target  string `json:"target,omitempty"`
	LinkDir bool   `json:"link_dir,omitempty"`
	Broken  bool   `json:"broken,omitempty"`
}

// Listing is the response of a folder listing.
type Listing struct {
	Path      string  `json:"path"`
	Parent    string  `json:"parent,omitempty"`
	Home      string  `json:"home"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated,omitempty"`
	Writable  bool    `json:"writable"`
}

// Content is a file opened for reading or editing.
type Content struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	ModTime  int64  `json:"mtime"`
	Version  string `json:"version"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"too_large"`
	Writable bool   `json:"writable"`
	Content  string `json:"content"`
}

// Saved describes a file after a write.
type Saved struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
	Version string `json:"version"`
}

type apiError struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Version string `json:"version,omitempty"`
}

// Handler serves the explorer endpoints under /api/files.
type Handler struct {
	home      string
	maxUpload int64
}

// New returns a handler whose default folder is the process home directory.
func New() *Handler {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/"
	}
	return &Handler{home: filepath.Clean(home), maxUpload: MaxUpload(os.Getenv("TMUXATLAS_FILES_UPLOAD_MAX"))}
}

// NewWithHome is used by tests to pin the default folder.
func NewWithHome(home string) *Handler {
	return &Handler{home: filepath.Clean(home), maxUpload: DefaultMaxUpload}
}

// UploadLimit is the largest body the upload route accepts.
func (h *Handler) UploadLimit() int64 { return h.maxUpload }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

// writeFSError maps operating system errors to clear HTTP answers.
func writeFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "fichier ou dossier introuvable")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission refusée")
	case errors.Is(err, fs.ErrExist):
		writeError(w, http.StatusConflict, "un élément porte déjà ce nom")
	case errors.Is(err, syscall.ENOTDIR):
		writeError(w, http.StatusBadRequest, "ce chemin n’est pas un dossier")
	case errors.Is(err, syscall.EISDIR):
		writeError(w, http.StatusBadRequest, "ce chemin est un dossier")
	case errors.Is(err, syscall.ENOTEMPTY):
		writeError(w, http.StatusConflict, "le dossier n’est pas vide")
	case errors.Is(err, syscall.EXDEV):
		writeError(w, http.StatusBadRequest, "déplacement impossible entre deux systèmes de fichiers")
	case errors.Is(err, syscall.EROFS):
		writeError(w, http.StatusForbidden, "système de fichiers en lecture seule")
	case errors.Is(err, syscall.ENOSPC):
		writeError(w, http.StatusInsufficientStorage, "plus d’espace disque")
	default:
		writeError(w, http.StatusInternalServerError, "opération impossible : "+err.Error())
	}
}

// cleanPath accepts absolute paths only and normalizes them. An empty path
// means the home folder.
func (h *Handler) cleanPath(raw string) (string, error) {
	if raw == "" {
		return h.home, nil
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("chemin invalide")
	}
	if strings.HasPrefix(raw, "~") && (len(raw) == 1 || raw[1] == '/') {
		raw = h.home + raw[1:]
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("le chemin doit être absolu")
	}
	return filepath.Clean(raw), nil
}

func version(info fs.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

func modTime(info fs.FileInfo) int64 {
	return info.ModTime().UnixMilli()
}

func writable(path string) bool {
	return syscall.Access(path, 2 /* W_OK */) == nil
}

// List handles GET /api/files/list?path=.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	dir, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "ce chemin n’est pas un dossier")
		return
	}
	handle, err := os.Open(dir)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer handle.Close()
	dirEntries, err := handle.ReadDir(MaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		writeFSError(w, err)
		return
	}
	listing := Listing{Path: dir, Home: h.home, Entries: make([]Entry, 0, len(dirEntries)), Writable: writable(dir)}
	if dir != "/" {
		listing.Parent = filepath.Dir(dir)
	}
	if len(dirEntries) > MaxEntries {
		dirEntries = dirEntries[:MaxEntries]
		listing.Truncated = true
	}
	for _, dirEntry := range dirEntries {
		full := filepath.Join(dir, dirEntry.Name())
		entry := Entry{Name: dirEntry.Name(), Path: full, Hidden: strings.HasPrefix(dirEntry.Name(), ".")}
		info, err := dirEntry.Info()
		if err != nil {
			// The entry vanished between ReadDir and Lstat.
			continue
		}
		entry.Size = info.Size()
		entry.Mode = info.Mode().String()
		entry.ModTime = modTime(info)
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			entry.Type = "symlink"
			entry.Target, _ = os.Readlink(full)
			if target, err := os.Stat(full); err == nil {
				entry.LinkDir = target.IsDir()
				if !target.IsDir() {
					entry.Size = target.Size()
				}
			} else {
				entry.Broken = true
			}
		case mode.IsDir():
			entry.Type = "dir"
		case mode.IsRegular():
			entry.Type = "file"
		default:
			entry.Type = "other"
		}
		listing.Entries = append(listing.Entries, entry)
	}
	sort.Slice(listing.Entries, func(i, j int) bool {
		a, b := listing.Entries[i], listing.Entries[j]
		aDir, bDir := a.Type == "dir" || a.LinkDir, b.Type == "dir" || b.LinkDir
		if aDir != bDir {
			return aDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	writeJSON(w, http.StatusOK, listing)
}

// looksBinary reports whether a file prefix is not text: a NUL byte or
// invalid UTF-8 means binary. A rune cut at the end of the sample is allowed.
func looksBinary(sample []byte, complete bool) bool {
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	if !complete {
		for i := 0; i < utf8.UTFMax && len(sample) > 0; i++ {
			if utf8.Valid(sample) {
				return false
			}
			sample = sample[:len(sample)-1]
		}
	}
	return !utf8.Valid(sample)
}

// Read handles GET /api/files/content?path=: it returns the text of a file,
// or only its metadata when it is binary or too large to edit.
func (h *Handler) Read(w http.ResponseWriter, r *http.Request) {
	path, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeFSError(w, err)
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "ce chemin est un dossier")
		return
	}
	content := Content{
		Path: path, Name: filepath.Base(path), Size: info.Size(), Mode: info.Mode().String(),
		ModTime: modTime(info), Version: version(info), Writable: writable(path),
	}
	if !info.Mode().IsRegular() {
		content.Binary = true
		writeJSON(w, http.StatusOK, content)
		return
	}
	sample := make([]byte, sniffSize)
	n, err := io.ReadFull(file, sample)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		writeFSError(w, err)
		return
	}
	sample = sample[:n]
	if looksBinary(sample, int64(n) >= info.Size()) {
		content.Binary = true
		writeJSON(w, http.StatusOK, content)
		return
	}
	if info.Size() > MaxEditSize {
		content.TooLarge = true
		writeJSON(w, http.StatusOK, content)
		return
	}
	rest, err := io.ReadAll(io.LimitReader(file, MaxEditSize+1-int64(n)))
	if err != nil {
		writeFSError(w, err)
		return
	}
	data := append(sample, rest...)
	if len(data) > MaxEditSize {
		content.TooLarge = true
		writeJSON(w, http.StatusOK, content)
		return
	}
	if !utf8.Valid(data) {
		content.Binary = true
		writeJSON(w, http.StatusOK, content)
		return
	}
	content.Content = string(data)
	writeJSON(w, http.StatusOK, content)
}

// Write handles PUT /api/files/content?path=&version=&force=. The body is the
// raw new text. version must match the file on disk unless force=1, so a save
// never silently overwrites a change made elsewhere. Without a version the
// file must not exist yet.
func (h *Handler) Write(w http.ResponseWriter, r *http.Request) {
	path, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil || r.URL.Query().Get("path") == "" {
		writeError(w, http.StatusBadRequest, "chemin invalide")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxEditSize+1))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "fichier trop volumineux")
			return
		}
		writeError(w, http.StatusBadRequest, "lecture de la requête impossible")
		return
	}
	if len(body) > MaxEditSize {
		writeError(w, http.StatusRequestEntityTooLarge, "fichier trop volumineux")
		return
	}
	expected := r.URL.Query().Get("version")
	force := r.URL.Query().Get("force") == "1"

	// Writes go to the file a symlink points to, so the link itself survives.
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	info, statErr := os.Stat(target)
	switch {
	case statErr == nil && info.IsDir():
		writeError(w, http.StatusBadRequest, "ce chemin est un dossier")
		return
	case statErr == nil && !info.Mode().IsRegular():
		writeError(w, http.StatusBadRequest, "ce fichier ne peut pas être modifié")
		return
	case statErr == nil && !force && version(info) != expected:
		writeJSON(w, http.StatusConflict, apiError{
			Error: "le fichier a été modifié ailleurs depuis son ouverture", Code: "conflict", Version: version(info),
		})
		return
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		writeFSError(w, statErr)
		return
	case statErr != nil && expected != "" && !force:
		writeJSON(w, http.StatusConflict, apiError{Error: "le fichier a été supprimé ailleurs", Code: "deleted"})
		return
	}

	if statErr != nil {
		err = createFile(target, body)
	} else {
		err = replaceFile(target, info, body)
	}
	if err != nil {
		writeFSError(w, err)
		return
	}
	saved, err := os.Stat(target)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Saved{Path: path, Size: saved.Size(), ModTime: modTime(saved), Version: version(saved)})
}

func createFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// replaceFile writes atomically through a temporary file and a rename, so a
// crash never leaves half a file. When the owner cannot be preserved or the
// file has several hard links, it writes in place instead.
func replaceFile(path string, info fs.FileInfo, data []byte) error {
	stat, _ := info.Sys().(*syscall.Stat_t)
	if stat == nil || stat.Nlink > 1 {
		return writeInPlace(path, data)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmuxatlas-*")
	if err != nil {
		// The folder may not be writable even though the file is.
		return writeInPlace(path, data)
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }
	if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
		if err := temp.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			temp.Close()
			cleanup()
			return writeInPlace(path, data)
		}
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func writeInPlace(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// previewTypes lists the media the browser may display inline. Every other
// file is served as an attachment so the browser never renders it as a page.
var previewTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".jfif": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	".ico": "image/x-icon", ".svg": "image/svg+xml", ".tif": "image/tiff", ".tiff": "image/tiff",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime", ".ogv": "video/ogg",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".flac": "audio/flac", ".m4a": "audio/mp4", ".opus": "audio/ogg",
}

// Raw handles GET /api/files/raw?path=&download=1. It streams the bytes of a
// file with Range support. A strict sandbox policy keeps an SVG or any other
// active content from running scripts on the TmuxAtlas origin.
func (h *Handler) Raw(w http.ResponseWriter, r *http.Request) {
	path, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeFSError(w, err)
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusBadRequest, "ce chemin n’est pas un fichier")
		return
	}
	name := filepath.Base(path)
	contentType, inline := previewTypes[strings.ToLower(filepath.Ext(name))]
	if r.URL.Query().Get("download") == "1" {
		inline = false
	}
	if !inline {
		contentType = "application/octet-stream"
	}
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	header.Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'; sandbox")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, no-cache")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "", info.ModTime(), file)
}

type pathRequest struct {
	Path string `json:"path"`
}

func (h *Handler) decodePath(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req pathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "chemin requis")
		return "", false
	}
	path, err := h.cleanPath(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	return path, true
}

func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return errors.New("nom invalide")
	}
	if len(name) > 255 {
		return errors.New("nom trop long")
	}
	return nil
}

func (h *Handler) entryFor(path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Name: filepath.Base(path), Path: path, Size: info.Size(), Mode: info.Mode().String(),
		ModTime: modTime(info), Hidden: strings.HasPrefix(filepath.Base(path), "."), Type: "file",
	}
	if info.IsDir() {
		entry.Type = "dir"
	}
	return entry, nil
}

// Mkdir handles POST /api/files/mkdir {"path"}.
func (h *Handler) Mkdir(w http.ResponseWriter, r *http.Request) {
	path, ok := h.decodePath(w, r)
	if !ok {
		return
	}
	if err := validName(filepath.Base(path)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		writeFSError(w, err)
		return
	}
	entry, err := h.entryFor(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// Create handles POST /api/files/create {"path"}: an empty new file.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	path, ok := h.decodePath(w, r)
	if !ok {
		return
	}
	if err := validName(filepath.Base(path)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := createFile(path, nil); err != nil {
		writeFSError(w, err)
		return
	}
	entry, err := h.entryFor(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// Rename handles POST /api/files/rename {"from","to"}. It never replaces an
// existing destination.
func (h *Handler) Rename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.From == "" || req.To == "" {
		writeError(w, http.StatusBadRequest, "chemins requis")
		return
	}
	from, err := h.cleanPath(req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := h.cleanPath(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from == "/" {
		writeError(w, http.StatusBadRequest, "la racine ne peut pas être renommée")
		return
	}
	if err := validName(filepath.Base(to)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from == to {
		writeError(w, http.StatusBadRequest, "le nom n’a pas changé")
		return
	}
	if strings.HasPrefix(to, from+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "un dossier ne peut pas être déplacé dans lui-même")
		return
	}
	if _, err := os.Lstat(to); err == nil {
		writeError(w, http.StatusConflict, "un élément porte déjà ce nom")
		return
	}
	if err := os.Rename(from, to); err != nil {
		writeFSError(w, err)
		return
	}
	entry, err := h.entryFor(to)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// Delete handles POST /api/files/delete {"path","recursive"}. A folder that
// still holds items is removed only when recursive is true.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "chemin requis")
		return
	}
	path, err := h.cleanPath(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if path == "/" || path == h.home {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s ne peut pas être supprimé ici", path))
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if info.IsDir() && req.Recursive {
		err = os.RemoveAll(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Upload handles POST /api/files/upload?path=&overwrite=1. The body is the raw
// file. Missing parent folders are created, so a dropped folder keeps its
// layout. The bytes land in a temporary file first: an interrupted upload
// never leaves a partial file behind.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	path, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil || r.URL.Query().Get("path") == "" {
		writeError(w, http.StatusBadRequest, "chemin invalide")
		return
	}
	if err := validName(filepath.Base(path)); err != nil || path == "/" {
		writeError(w, http.StatusBadRequest, "nom invalide")
		return
	}
	if r.ContentLength > h.maxUpload {
		writeError(w, http.StatusRequestEntityTooLarge, "fichier trop volumineux")
		return
	}
	overwrite := r.URL.Query().Get("overwrite") == "1"
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() || !overwrite {
			writeJSON(w, http.StatusConflict, apiError{Error: "un élément porte déjà ce nom", Code: "exists"})
			return
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeFSError(w, err)
		return
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".upload-*")
	if err != nil {
		writeFSError(w, err)
		return
	}
	tempName := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tempName)
		}
	}()
	written, err := io.Copy(temp, io.LimitReader(r.Body, h.maxUpload+1))
	if err == nil && written > h.maxUpload {
		temp.Close()
		writeError(w, http.StatusRequestEntityTooLarge, "fichier trop volumineux")
		return
	}
	if err != nil {
		temp.Close()
		writeError(w, http.StatusBadRequest, "envoi interrompu")
		return
	}
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		writeFSError(w, err)
		return
	}
	if err := temp.Close(); err != nil {
		writeFSError(w, err)
		return
	}
	if !overwrite {
		// Link instead of rename so a file created meanwhile is never replaced.
		if err := os.Link(tempName, path); err != nil {
			if errors.Is(err, fs.ErrExist) {
				writeJSON(w, http.StatusConflict, apiError{Error: "un élément porte déjà ce nom", Code: "exists"})
				return
			}
			if err := os.Rename(tempName, path); err != nil {
				writeFSError(w, err)
				return
			}
			keep = true
		}
	} else if err := os.Rename(tempName, path); err != nil {
		writeFSError(w, err)
		return
	} else {
		keep = true
	}
	entry, err := h.entryFor(path)
	if err != nil {
		writeFSError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// Archive handles GET /api/files/archive?path=: it streams a folder as a zip.
// Symlinks are stored as links and never followed, so a loop cannot run forever.
func (h *Handler) Archive(w http.ResponseWriter, r *http.Request) {
	root, err := h.cleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(root)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "ce chemin n’est pas un dossier")
		return
	}
	base := filepath.Base(root)
	if base == "/" {
		base = "racine"
	}
	header := w.Header()
	header.Set("Content-Type", "application/zip")
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": base + ".zip"}))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	archive := zip.NewWriter(w)
	defer archive.Close()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if r.Context().Err() != nil {
			return r.Context().Err()
		}
		if err != nil {
			// Unreadable entries are skipped rather than failing the whole archive.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		name := filepath.ToSlash(filepath.Join(base, rel))
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		fileHeader, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		fileHeader.Name = name
		switch {
		case entry.IsDir():
			fileHeader.Name += "/"
			_, err = archive.CreateHeader(fileHeader)
			return err
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			writer, err := archive.CreateHeader(fileHeader)
			if err != nil {
				return err
			}
			_, err = io.WriteString(writer, target)
			return err
		case !info.Mode().IsRegular():
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()
		fileHeader.Method = zip.Deflate
		writer, err := archive.CreateHeader(fileHeader)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, file)
		return err
	})
}
