package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/paths"
	"github.com/LosFurina/tmuxatlas/pkg/secrets"
)

// The Secrets view is admin-only: its routes are mounted on dockerRouters,
// which requires an admin, and none of them is in accountUserRoutes.
func init() {
	for _, route := range secrets.Routes {
		method, path, _ := strings.Cut(route, " ")
		accountOtherRoutes[method+" /api"+path] = "admin"
	}
}

// secretsScryptWorkFactor is lowered by tests only; 0 keeps the age default.
var secretsScryptWorkFactor = 0

// secretsRestartTimeout bounds the restart that follows an apply. It is not
// tied to the request: a browser that goes away does not interrupt it.
const secretsRestartTimeout = 6 * time.Minute

// newSecretsHandler builds the Secrets vault (TMUXATLAS_SECRETS) in
// <data>/secrets. Without authentication any local process could write
// environment files and restart containers through it, so it then needs
// TMUXATLAS_SECRETS=on, like the Containers view.
func newSecretsHandler(opts *Options, docker *dockerui.Service) *secrets.Handler {
	actor := "passkey"
	if !opts.AuthEnabled {
		actor = "sans authentification"
	}
	if problem := secretsProblem(opts.AuthEnabled); problem != "" {
		return secrets.NewHandler(nil, problem, actor)
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		logrus.WithError(err).Warn("secrets vault unavailable")
		return secrets.NewHandler(nil, "", actor)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		logrus.WithError(err).Warn("secrets vault unavailable")
		return secrets.NewHandler(nil, "", actor)
	}
	// No target may be written among the hub's own files.
	forbidden := []string{dataDir}
	if configDir, err := paths.ConfigDir(); err == nil {
		forbidden = append(forbidden, configDir)
	}
	vault, err := secrets.New(secrets.Options{
		Dir: filepath.Join(dataDir, "secrets"), Home: home, Forbidden: forbidden,
		Restarter: dockerRestarter{docker: docker}, ScryptWorkFactor: secretsScryptWorkFactor,
	})
	if err != nil {
		logrus.WithError(err).Warn("secrets vault unavailable")
		return secrets.NewHandler(nil, "", actor)
	}
	return secrets.NewHandler(vault, "", actor)
}

// secretsProblem reads TMUXATLAS_SECRETS and says why the vault is closed,
// or nothing when it is open.
func secretsProblem(authEnabled bool) string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TMUXATLAS_SECRETS"))) {
	case "off", "false", "0", "no":
		return "coffre de secrets désactivé sur ce serveur (TMUXATLAS_SECRETS=off)"
	case "on", "true", "1", "yes":
		return ""
	}
	if !authEnabled {
		return "coffre de secrets désactivé : ce serveur tourne sans authentification (TMUXATLAS_SECRETS=on pour l’activer quand même)"
	}
	return ""
}

// dockerRestarter restarts a container for the vault through the Containers
// view, so its switches and gates apply here too.
type dockerRestarter struct{ docker *dockerui.Service }

func (d dockerRestarter) RestartContainer(actor, name string) error {
	if d.docker == nil {
		return errors.New("vue Docker indisponible")
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsRestartTimeout)
	defer cancel()
	if _, err := d.docker.RestartByName(ctx, actor, name); err != nil {
		// The same message as the Containers view: it names no secret.
		return errors.New(dockerui.ErrorMessage(err))
	}
	return nil
}
