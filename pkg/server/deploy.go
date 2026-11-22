package server

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/deploy"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/paths"
)

// Application deployment is admin-only: its routes are mounted on
// dockerRouters, which requires an admin, and none is in accountUserRoutes.
func init() {
	for _, route := range deploy.Routes {
		method, path, _ := strings.Cut(route, " ")
		accountOtherRoutes[method+" /api"+path] = "admin"
	}
}

// deployRunner replaces the real git and docker clients in tests.
var deployRunner deploy.Runner

// deployEnabled reads TMUXATLAS_DEPLOY: off unless explicitly asked for.
func deployEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TMUXATLAS_DEPLOY"))) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// newDeployHandler builds the application deployment (TMUXATLAS_DEPLOY) with
// its records in <data>/deploy. It reads the stacks through the Containers
// view, so the switches and gates of the Docker actions apply here too.
func newDeployHandler(opts *Options, docker *dockerui.Service) *deploy.Handler {
	actor := "passkey"
	if !opts.AuthEnabled {
		actor = "sans authentification"
	}
	options := deploy.Options{Stacks: docker, Enabled: deployEnabled(), Runner: deployRunner}
	if options.Runner == nil {
		cli, socket := docker.CLI()
		runner := deploy.NewExecRunner(cli, socket, dockerui.DefaultSocket)
		options.Runner, options.Problem = runner, runner.Missing()
	}
	if dataDir, err := paths.DataDir(); err != nil {
		logrus.WithError(err).Warn("application deployment unavailable")
		options.Problem = "dossier de données du hub indisponible"
	} else {
		options.Dir = filepath.Join(dataDir, "deploy")
	}
	return deploy.NewHandler(deploy.New(options), actor)
}
