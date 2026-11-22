package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/sirupsen/logrus"

	"github.com/LosFurina/tmuxatlas/pkg/dbbackup"
	"github.com/LosFurina/tmuxatlas/pkg/dockerui"
	"github.com/LosFurina/tmuxatlas/pkg/ops"
	"github.com/LosFurina/tmuxatlas/pkg/paths"
)

// The database backups are admin-only: their routes are mounted on
// dockerRouters, which requires an admin, and none is in accountUserRoutes.
func init() {
	for _, route := range dbbackup.Routes {
		method, path, _ := strings.Cut(route, " ")
		accountOtherRoutes[method+" /api"+path] = "admin"
	}
}

// dbBackupService ties the database backups to the server: the alert engine
// is built before the Containers view the backups need, so it reads the
// manager through here once it exists.
type dbBackupService struct {
	manager atomic.Pointer[dbbackup.Manager]
}

// dbRestoreEnabled reads TMUXATLAS_DB_RESTORE: a restore overwrites a
// database, so it is off unless asked for.
func dbRestoreEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TMUXATLAS_DB_RESTORE"))) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

// start builds the manager in <data>/db-backups and its scheduler. Every
// access to Docker goes through the Containers view, so its switches
// (TMUXATLAS_DOCKER, the shared-account gate) close the backups too.
func (s *dbBackupService) start(ctx context.Context, opts *Options, docker *dockerui.Service) *dbbackup.Handler {
	actor := "passkey"
	if !opts.AuthEnabled {
		actor = "sans authentification"
	}
	dataDir, err := paths.DataDir()
	if err != nil {
		logrus.WithError(err).Warn("database backups unavailable")
		return dbbackup.NewHandler(nil, "", actor)
	}
	manager, err := dbbackup.New(dbbackup.Options{
		Dir: filepath.Join(dataDir, "db-backups"), Docker: dockerBackupAccess{docker: docker},
		RestoreEnabled: dbRestoreEnabled(),
	})
	if err != nil {
		logrus.WithError(err).Warn("database backups unavailable")
		return dbbackup.NewHandler(nil, "", actor)
	}
	s.manager.Store(manager)
	go manager.Run(ctx)
	return dbbackup.NewHandler(manager, "", actor)
}

// signals gives the alert engine the scheduled backups that failed. A
// signal lasts until a backup of that database succeeds: the engine then
// announces the recovery.
func (s *dbBackupService) signals() func() []ops.Signal {
	return func() []ops.Signal {
		manager := s.manager.Load()
		if manager == nil {
			return nil
		}
		failures := manager.Failures()
		names := make([]string, 0, len(failures))
		for name := range failures {
			names = append(names, name)
		}
		sort.Strings(names)
		out := make([]ops.Signal, 0, len(names))
		for _, name := range names {
			out = append(out, ops.Signal{
				Key: "dbbackup:" + name + ":failed", Severity: "critical",
				Title:  "Sauvegarde de la base " + name + " en échec",
				Detail: failures[name].Detail,
			})
		}
		return out
	}
}

// dockerBackupAccess is the Docker the backups see: the Containers view.
// Its errors are turned into that view's messages, which name no variable.
type dockerBackupAccess struct{ docker *dockerui.Service }

func (d dockerBackupAccess) Problem() string {
	if d.docker == nil {
		return "vue Docker indisponible"
	}
	return d.docker.Problem()
}

func (d dockerBackupAccess) Running(ctx context.Context) ([]dbbackup.Container, error) {
	if d.docker == nil {
		return nil, errors.New("vue Docker indisponible")
	}
	list, err := d.docker.RunningContainers(ctx)
	if err != nil {
		return nil, errors.New(dockerui.ErrorMessage(err))
	}
	out := make([]dbbackup.Container, 0, len(list))
	for _, c := range list {
		out = append(out, dbbackup.Container{ID: c.ID, Name: c.Name, Image: c.Image})
	}
	return out, nil
}

func (d dockerBackupAccess) Env(ctx context.Context, id string) ([]string, error) {
	if d.docker == nil {
		return nil, errors.New("vue Docker indisponible")
	}
	env, err := d.docker.ContainerEnv(ctx, id)
	if err != nil {
		return nil, errors.New(dockerui.ErrorMessage(err))
	}
	return env, nil
}

func (d dockerBackupAccess) Exec(ctx context.Context, id string, spec dbbackup.ExecSpec) (int, error) {
	if d.docker == nil {
		return -1, errors.New("vue Docker indisponible")
	}
	code, err := d.docker.Exec(ctx, id, dockerui.ExecSpec{
		Cmd: spec.Cmd, Env: spec.Env, Stdin: spec.Stdin, Stdout: spec.Stdout, Stderr: spec.Stderr,
	})
	if err != nil {
		return code, errors.New(dockerui.ErrorMessage(err))
	}
	return code, nil
}
