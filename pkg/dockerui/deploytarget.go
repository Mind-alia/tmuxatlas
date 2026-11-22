package dockerui

import (
	"context"
	"strconv"
	"strings"
)

// What pkg/deploy needs to know about one Compose project. It is read from
// the same listing and behind the same switches as the Applications tab.

// labelEnvFiles is set by Compose when the stack was started with --env-file.
const labelEnvFiles = "com.docker.compose.project.environment_file"

// DeployService is one container of a stack.
type DeployService struct {
	ContainerID string
	Container   string
	Service     string
	// Image is the image reference the container was created from.
	Image string
	// ImageID is the "sha256:…" ID of the image the container uses.
	ImageID string
	State   string
	// Health is healthy, unhealthy, starting, or "" without healthcheck.
	Health string
	// ExitCode is set for an exited container when Docker tells it.
	ExitCode *int
}

// DeployTarget describes a Compose project from the labels of its containers.
// Nothing in it is validated: the paths are whatever the labels say.
type DeployTarget struct {
	Project     string
	WorkingDir  string
	ConfigFiles []string
	EnvFiles    []string
	// Services lists the containers in start order.
	Services []DeployService
	// Hub names the container of the stack the hub itself runs in, if any.
	Hub string
}

func splitLabel(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// DeployTarget lists the containers of a Compose project. It applies the
// switches of every Docker action (TMUXATLAS_DOCKER, the shared-account gate).
func (s *Service) DeployTarget(ctx context.Context, project string) (DeployTarget, error) {
	if !stackPattern.MatchString(project) {
		return DeployTarget{}, errBadRequest("nom d’application invalide")
	}
	if err := s.usable(); err != nil {
		return DeployTarget{}, err
	}
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	list, err := s.client.listContainers(listCtx)
	if err != nil {
		return DeployTarget{}, err
	}
	members := stackMembers(list, project)
	if len(members) == 0 {
		return DeployTarget{}, errStackNotFound
	}
	target := DeployTarget{Project: project}
	if hub, found := s.hubIn(members); found {
		target.Hub = containerName(hub.Names, hub.ID)
	}
	for _, c := range stackOrder(members) {
		if target.WorkingDir == "" {
			target.WorkingDir = c.Labels[labelWorkingDir]
		}
		if len(target.ConfigFiles) == 0 {
			target.ConfigFiles = splitLabel(c.Labels[labelConfigFiles])
		}
		if len(target.EnvFiles) == 0 {
			target.EnvFiles = splitLabel(c.Labels[labelEnvFiles])
		}
		service := DeployService{
			ContainerID: c.ID,
			Container:   containerName(c.Names, c.ID),
			Service:     c.Labels[labelService],
			Image:       c.Image,
			ImageID:     c.ImageID,
			State:       c.State,
			Health:      health(c, apiInspect{}),
		}
		if match := exitedPattern.FindStringSubmatch(c.Status); match != nil && (c.State == "exited" || c.State == "dead") {
			if code, err := strconv.Atoi(match[1]); err == nil {
				service.ExitCode = &code
			}
		}
		target.Services = append(target.Services, service)
	}
	return target, nil
}

// ErrorStatus is the HTTP status and the message the Containers view gives err.
func ErrorStatus(err error) (int, string) {
	return errorStatus(err)
}

// CLI returns the docker client found at startup ("" when there is none) and
// the Engine API socket the view talks to.
func (s *Service) CLI() (docker, socket string) {
	return s.cfg.DockerCLI, s.cfg.Socket
}
