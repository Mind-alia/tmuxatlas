package dockerui

import (
	"context"
	"regexp"

	"github.com/sirupsen/logrus"
)

var exactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// RestartByName restarts the one container called exactly name, through
// Action: same switches (TMUXATLAS_DOCKER, the shared-account gate), same
// resolution in a fresh listing, same timeouts. It serves callers inside the
// hub that know a container by its name, such as the secrets vault after it
// has written an environment file; actor is who asked, for the log.
func (s *Service) RestartByName(ctx context.Context, actor, name string) (ActionResponse, error) {
	if !exactNamePattern.MatchString(name) {
		return ActionResponse{}, errBadRequest("nom de conteneur invalide")
	}
	if err := s.usable(); err != nil {
		return ActionResponse{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := s.client.listContainers(listCtx)
	if err != nil {
		return ActionResponse{}, err
	}
	id := ""
	for _, c := range list {
		if containerName(c.Names, c.ID) != name {
			continue
		}
		if id != "" {
			return ActionResponse{}, errAmbiguousID
		}
		id = c.ID
	}
	if id == "" {
		return ActionResponse{}, errNotFound
	}
	result, err := s.Action(ctx, "restart", id)
	entry := s.logger.WithFields(logrus.Fields{
		"actor": actor, "action": "restart", "container": name, "container_id": shortID(id), "origin": "secrets",
	})
	if err != nil {
		_, message := errorStatus(err)
		entry.WithField("error", message).Warn("docker container action refused or failed")
		return result, err
	}
	entry.WithField("changed", result.Changed).Info("docker container action")
	return result, nil
}

// ErrorMessage is the message the Containers view shows for err.
func ErrorMessage(err error) string {
	_, message := errorStatus(err)
	return message
}
