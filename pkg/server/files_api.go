package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/LosFurina/tmuxatlas/pkg/auth"
	"github.com/LosFurina/tmuxatlas/pkg/files"
	"github.com/LosFurina/tmuxatlas/pkg/peer"
	"github.com/sirupsen/logrus"
)

const (
	// maxFileView bounds a file opened in the browser's editor.
	maxFileView = 1 << 20
	// maxFileUpload stays under the global request body limit.
	maxFileUpload = (1 << 20) - (16 << 10)
	// maxFileOpsPerAccount bounds the file operations one account runs at once.
	maxFileOpsPerAccount = 4
)

// filesAPI serves /api/files/*. Each request names a host with host_id; the
// account policy has already checked that the account may use it.
type filesAPI struct {
	opts   *Options
	router *peer.FileRouter
	policy *accountPolicy
	mu     sync.Mutex
	active map[string]int
}

func newFilesAPI(opts *Options, policy *accountPolicy) *filesAPI {
	return &filesAPI{opts: opts, router: peer.NewFileRouter(opts.PeerMgr, opts.Files, 0), policy: policy, active: make(map[string]int)}
}

var fileErrorStatus = map[string]int{
	files.CodeDisabled: http.StatusForbidden, files.CodeReadOnly: http.StatusForbidden,
	files.CodeDenied: http.StatusForbidden, files.CodeNotFound: http.StatusNotFound,
	files.CodeInvalid: http.StatusBadRequest, files.CodeConflict: http.StatusConflict,
	files.CodeTooLarge: http.StatusRequestEntityTooLarge, files.CodeBusy: http.StatusTooManyRequests,
	files.CodeOffline: http.StatusServiceUnavailable, "binary": http.StatusUnsupportedMediaType,
}

func writeFileError(w http.ResponseWriter, err error) {
	var fileError *files.Error
	if !errors.As(err, &fileError) {
		fileError = &files.Error{Code: files.CodeFailed, Message: "opération impossible"}
	}
	status, ok := fileErrorStatus[fileError.Code]
	if !ok {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(fileError)
}

func writeFileJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func (api *filesAPI) actor(r *http.Request) (string, string) {
	if identity, ok := auth.AccountFromContext(r.Context()); ok {
		return identity.User.ID, identity.User.Username
	}
	return "local", "local"
}

// begin reserves one of the account's concurrent file operations.
func (api *filesAPI) begin(w http.ResponseWriter, r *http.Request) (func(), bool) {
	id, _ := api.actor(r)
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.active[id] >= maxFileOpsPerAccount {
		writeFileError(w, &files.Error{Code: files.CodeBusy, Message: "trop d'opérations en cours"})
		return nil, false
	}
	api.active[id]++
	return func() {
		api.mu.Lock()
		defer api.mu.Unlock()
		if api.active[id]--; api.active[id] <= 0 {
			delete(api.active, id)
		}
	}, true
}

func (api *filesAPI) call(ctx context.Context, host, operation string, payload any, result any) error {
	raw, err := api.router.Execute(ctx, host, operation, payload)
	if err != nil {
		return err
	}
	if result != nil && json.Unmarshal(raw, result) != nil {
		return &files.Error{Code: files.CodeFailed, Message: "réponse invalide de l'hôte"}
	}
	return nil
}

// hosts lists the hosts whose files the account may browse: every host for
// an administrator, the granted ones for a guest. Each is served by its own
// account's process.
func (api *filesAPI) hosts(w http.ResponseWriter, r *http.Request) {
	type hostView struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
		Local  bool   `json:"local"`
	}
	identity, accounts := auth.AccountFromContext(r.Context())
	views := make([]hostView, 0)
	if api.opts.PeerMgr != nil {
		for _, host := range api.opts.PeerMgr.StateHostSnapshots() {
			if accounts && !api.policy.guestMayUseHost(identity.User, host.ID) {
				continue
			}
			views = append(views, hostView{ID: host.ID, Name: host.DisplayName, Online: host.Online, Local: host.Local})
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Local != views[j].Local {
			return views[i].Local
		}
		return views[i].Name < views[j].Name
	})
	writeFileJSON(w, map[string]any{"hosts": views})
}

func (api *filesAPI) info(w http.ResponseWriter, r *http.Request) {
	var result json.RawMessage
	if err := api.call(r.Context(), r.URL.Query().Get("host_id"), "info", map[string]any{}, &result); err != nil {
		writeFileError(w, err)
		return
	}
	writeFileJSON(w, result)
}

func (api *filesAPI) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	done, ok := api.begin(w, r)
	if !ok {
		return
	}
	defer done()
	var entries []json.RawMessage
	var listed struct {
		Path      string            `json:"path"`
		Entries   []json.RawMessage `json:"entries"`
		Next      int               `json:"next"`
		Truncated bool              `json:"truncated"`
	}
	for offset := 0; offset >= 0 && len(entries) < files.MaxListEntries; offset = listed.Next {
		if err := api.call(r.Context(), query.Get("host_id"), "list", map[string]any{"path": query.Get("path"), "offset": offset}, &listed); err != nil {
			writeFileError(w, err)
			return
		}
		entries = append(entries, listed.Entries...)
	}
	writeFileJSON(w, map[string]any{"path": listed.Path, "entries": entries, "truncated": listed.Truncated})
}

type fileStat struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	MTime    string `json:"mtime"`
	Version  string `json:"version"`
	Writable bool   `json:"writable"`
}

// readFile streams a regular file chunk by chunk to emit. It stops if the
// file changes between chunks.
func (api *filesAPI) readFile(ctx context.Context, host, filePath string, limit int64, start func(fileStat) error, emit func([]byte) error) error {
	var stat fileStat
	if err := api.call(ctx, host, "stat", map[string]any{"path": filePath}, &stat); err != nil {
		return err
	}
	if stat.Type != "file" {
		return &files.Error{Code: files.CodeInvalid, Message: "ce chemin n'est pas un fichier"}
	}
	if stat.Size > limit {
		return &files.Error{Code: files.CodeTooLarge, Message: "fichier trop volumineux pour cette opération"}
	}
	if err := start(stat); err != nil {
		return err
	}
	for offset := int64(0); ; {
		var chunk struct {
			Data    string `json:"data"`
			Version string `json:"version"`
			EOF     bool   `json:"eof"`
		}
		if err := api.call(ctx, host, "read", map[string]any{"path": filePath, "offset": offset, "length": files.ChunkSize}, &chunk); err != nil {
			return err
		}
		if chunk.Version != stat.Version {
			return &files.Error{Code: files.CodeConflict, Message: "le fichier a changé pendant la lecture"}
		}
		data, err := base64.StdEncoding.DecodeString(chunk.Data)
		if err != nil {
			return &files.Error{Code: files.CodeFailed, Message: "réponse invalide de l'hôte"}
		}
		if err := emit(data); err != nil {
			return err
		}
		offset += int64(len(data))
		if chunk.EOF || len(data) == 0 || offset > limit {
			return nil
		}
	}
}

func (api *filesAPI) content(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	done, ok := api.begin(w, r)
	if !ok {
		return
	}
	defer done()
	var stat fileStat
	var content []byte
	err := api.readFile(r.Context(), query.Get("host_id"), query.Get("path"), maxFileView,
		func(s fileStat) error { stat = s; return nil },
		func(data []byte) error { content = append(content, data...); return nil })
	if err != nil {
		writeFileError(w, err)
		return
	}
	if !utf8.Valid(content) {
		writeFileError(w, &files.Error{Code: "binary", Message: "fichier binaire : téléchargez-le"})
		return
	}
	writeFileJSON(w, map[string]any{
		"path": stat.Path, "content": string(content), "size": stat.Size, "mtime": stat.MTime,
		"version": stat.Version, "writable": stat.Writable,
	})
}

// raw downloads a file. It is always an attachment, served as opaque bytes,
// so a file cannot run as a page of the Atlas origin.
func (api *filesAPI) raw(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	done, ok := api.begin(w, r)
	if !ok {
		return
	}
	defer done()
	started := false
	err := api.readFile(r.Context(), query.Get("host_id"), query.Get("path"), files.MaxFileSize,
		func(stat fileStat) error {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(stat.Path)))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
			w.Header().Set("Cache-Control", "no-store")
			return nil
		},
		func(data []byte) error {
			started = true
			_, err := w.Write(data)
			return err
		})
	if err != nil && !started {
		w.Header().Del("Content-Disposition")
		writeFileError(w, err)
		return
	}
	if err != nil {
		// Headers are gone: cut the connection so the download is incomplete
		// rather than silently truncated.
		panic(http.ErrAbortHandler)
	}
}

func newUploadID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}

// writeFile sends data to the host in chunks and commits it atomically.
func (api *filesAPI) writeFile(ctx context.Context, r *http.Request, host, filePath string, data []byte, replace bool, version string) (json.RawMessage, error) {
	_, actor := api.actor(r)
	id := newUploadID()
	for offset := 0; ; {
		end := min(offset+files.ChunkSize, len(data))
		final := end == len(data)
		var result json.RawMessage
		err := api.call(ctx, host, "write", map[string]any{
			"path": filePath, "upload_id": id, "offset": offset,
			"data":  base64.StdEncoding.EncodeToString(data[offset:end]),
			"final": final, "replace": replace, "version": version, "actor": actor,
		}, &result)
		if err != nil {
			if offset > 0 {
				_ = api.call(context.WithoutCancel(ctx), host, "write", map[string]any{"path": filePath, "upload_id": id, "abort": true}, nil)
			}
			return nil, err
		}
		if final {
			return result, nil
		}
		offset = end
	}
}

func (api *filesAPI) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxFileUpload+1))
	if err != nil {
		writeFileError(w, &files.Error{Code: files.CodeInvalid, Message: "envoi interrompu"})
		return nil, false
	}
	if len(data) > maxFileUpload {
		writeFileError(w, &files.Error{Code: files.CodeTooLarge, Message: "fichier trop volumineux (1 Mio par envoi)"})
		return nil, false
	}
	return data, true
}

func (api *filesAPI) save(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	done, ok := api.begin(w, r)
	if !ok {
		return
	}
	defer done()
	data, ok := api.readBody(w, r)
	if !ok {
		return
	}
	if !utf8.Valid(data) {
		writeFileError(w, &files.Error{Code: files.CodeInvalid, Message: "le texte doit être en UTF-8"})
		return
	}
	result, err := api.writeFile(r.Context(), r, query.Get("host_id"), query.Get("path"), data, true, query.Get("version"))
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeFileJSON(w, result)
}

func (api *filesAPI) upload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	done, ok := api.begin(w, r)
	if !ok {
		return
	}
	defer done()
	data, ok := api.readBody(w, r)
	if !ok {
		return
	}
	result, err := api.writeFile(r.Context(), r, query.Get("host_id"), query.Get("path"), data, query.Get("overwrite") == "1", "")
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeFileJSON(w, result)
}

// change runs mkdir, rename or delete with a small JSON body.
func (api *filesAPI) change(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Path string `json:"path"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			writeFileError(w, &files.Error{Code: files.CodeInvalid, Message: "requête invalide"})
			return
		}
		_, actor := api.actor(r)
		payload := map[string]any{"path": request.Path, "actor": actor}
		if operation == "rename" {
			payload = map[string]any{"path": request.From, "to": request.To, "actor": actor}
		}
		var result json.RawMessage
		if err := api.call(r.Context(), r.URL.Query().Get("host_id"), operation, payload, &result); err != nil {
			writeFileError(w, err)
			return
		}
		logrus.WithFields(logrus.Fields{"component": "files", "operation": operation, "host": r.URL.Query().Get("host_id"), "actor": actor}).Info("file change requested")
		writeFileJSON(w, result)
	}
}
