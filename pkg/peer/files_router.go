package peer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/files"
)

const fileErrorPrefix = "files:"

// EncodeFileError carries a files.Error code across the runtime protocol in
// the message of an execution-failed error.
func EncodeFileError(err error) string {
	var fileError *files.Error
	if !errors.As(err, &fileError) {
		fileError = &files.Error{Code: files.CodeFailed, Message: "opération impossible"}
	}
	message := fileErrorPrefix + fileError.Code + ":" + fileError.Message
	if len(message) > 400 {
		message = message[:400]
	}
	return message
}

func decodeFileError(err error) *files.Error {
	var fileError *files.Error
	if errors.As(err, &fileError) {
		return fileError
	}
	var runtimeError RuntimeError
	if errors.As(err, &runtimeError) {
		if rest, ok := strings.CutPrefix(runtimeError.Message, fileErrorPrefix); ok {
			if code, message, ok := strings.Cut(rest, ":"); ok {
				return &files.Error{Code: code, Message: message}
			}
		}
		switch runtimeError.Code {
		case ErrorPeerOffline, ErrorPeerRevoked, ErrorStaleGeneration:
			return &files.Error{Code: files.CodeOffline, Message: "l'Agent de cet hôte est hors ligne"}
		case ErrorCapabilityUnsupported, ErrorUnsupported:
			return &files.Error{Code: files.CodeDisabled, Message: "l'Agent de cet hôte ne sert pas de fichiers (mettez-le à jour)"}
		case ErrorQueueFull, ErrorResourceExhausted:
			return &files.Error{Code: files.CodeBusy, Message: "trop d'opérations en cours"}
		case ErrorTimeout:
			return &files.Error{Code: files.CodeBusy, Message: "l'Agent n'a pas répondu à temps"}
		}
	}
	return &files.Error{Code: files.CodeFailed, Message: "opération impossible"}
}

// FileRouter sends a file operation to the process that runs under the
// host's own Unix account: the hub's local service for the hub's host, and
// the host's Agent otherwise. It never falls back to reading a host's files
// from another account: an offline Agent is an error.
type FileRouter struct {
	manager *Manager
	local   *files.Service
	timeout time.Duration
}

func NewFileRouter(manager *Manager, local *files.Service, timeout time.Duration) *FileRouter {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &FileRouter{manager: manager, local: local, timeout: timeout}
}

// Execute runs operation ("list", "read", …) on hostID's files. Errors are
// always *files.Error.
func (router *FileRouter) Execute(ctx context.Context, hostID, operation string, payload any) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil || router.manager == nil || hostID == "" {
		return nil, &files.Error{Code: files.CodeInvalid, Message: "requête invalide"}
	}
	if router.manager.IsLocal(hostID) {
		result, err := router.local.Execute(ctx, operation, encoded)
		if err != nil {
			return nil, decodeFileError(err)
		}
		return result, nil
	}
	if !router.manager.HasHost(hostID) {
		return nil, &files.Error{Code: files.CodeNotFound, Message: "hôte inconnu"}
	}
	connection := router.manager.GetPeerConnection(hostID)
	if connection == nil {
		return nil, &files.Error{Code: files.CodeOffline, Message: "l'Agent de cet hôte est hors ligne"}
	}
	if !connection.Supports(CapabilityFiles) {
		return nil, &files.Error{Code: files.CodeDisabled, Message: "l'Agent de cet hôte ne sert pas de fichiers (mettez-le à jour)"}
	}
	requestCtx, cancel := context.WithTimeout(ctx, router.timeout)
	defer cancel()
	request := RuntimeRequest{
		RequestID: newRuntimeRequestID(), Generation: connection.Generation,
		Deadline: time.Now().Add(router.timeout), Operation: FileOperationPrefix + operation,
		Target: SessionTarget{HostID: hostID}, Payload: encoded,
	}
	result, err := connection.Request(requestCtx, request)
	if err != nil {
		return nil, decodeFileError(err)
	}
	return result, nil
}
