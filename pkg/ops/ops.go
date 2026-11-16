package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/monitor"
	"github.com/sirupsen/logrus"
)

const (
	// PollInterval is how often the status directory is read.
	PollInterval = 15 * time.Second
	// StaleAfter marks a snapshot as too old: the collector stopped.
	StaleAfter = 2 * time.Minute
)

// Alert is a monitor.Alert with a stable key, so a notifier can follow it
// from one evaluation to the next.
type Alert struct {
	Key string `json:"key"`
	monitor.Alert
}

// Response is the payload of GET /api/ops (admin only).
type Response struct {
	Configured bool           `json:"configured"`
	Dir        string         `json:"dir,omitempty"`
	CheckedAt  int64          `json:"checked_at,omitempty"`
	Containers ContainersView `json:"containers"`
	Backups    BackupView     `json:"backups"`
	Alerts     []Alert        `json:"alerts"`
}

// Monitor polls the status directory and keeps the derived state.
type Monitor struct {
	src    *Source
	logger *logrus.Entry
	now    func() time.Time

	mu         sync.RWMutex
	tracker    *containerTracker
	containers ContainersSnapshot
	contErr    error
	backup     *BackupStatus
	lastOK     *BackupStatus
	backupErr  error
	checkedAt  time.Time
}

// New returns a monitor reading dir. Call Run to start polling.
func New(dir string) *Monitor {
	return &Monitor{
		src:     NewSource(dir),
		logger:  logrus.WithField("component", "ops"),
		now:     time.Now,
		tracker: newContainerTracker(),
		contErr: ErrMissing,
	}
}

// Run polls until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	m.Refresh()
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Refresh()
		}
	}
}

// Refresh reads every status file once.
func (m *Monitor) Refresh() {
	now := m.now()
	var snap ContainersSnapshot
	_, err := m.src.ReadJSON(ContainersFile, &snap)
	if err == nil && snap.Schema != ContainersSchema {
		err = fmt.Errorf("%s : schéma %d non pris en charge (attendu %d)", ContainersFile, snap.Schema, ContainersSchema)
	}
	backup, backupErr := readBackup(m.src, BackupStatusFile)
	lastOK, lastErr := readBackup(m.src, LastSuccessFile)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkedAt = now
	// A missing file clears what we knew; any other error keeps the last good
	// read and is reported.
	keep := func(dst **BackupStatus, v *BackupStatus, err error) error {
		switch {
		case err == nil:
			*dst = v
		case errors.Is(err, ErrMissing):
			*dst = nil
		default:
			return err
		}
		return nil
	}
	bErr := keep(&m.backup, backup, backupErr)
	if lErr := keep(&m.lastOK, lastOK, lastErr); bErr == nil {
		bErr = lErr
	}
	if bErr != nil && (m.backupErr == nil || bErr.Error() != m.backupErr.Error()) {
		m.logger.WithError(bErr).Warn("lecture du statut des sauvegardes")
	}
	m.backupErr = bErr
	if err != nil {
		if m.contErr == nil || err.Error() != m.contErr.Error() {
			m.logger.WithError(err).Warn("lecture du statut des conteneurs")
		}
		m.contErr = err
		return
	}
	m.contErr = nil
	m.containers = snap
	m.tracker.observe(snap, now)
}

// Snapshot builds the API response.
func (m *Monitor) Snapshot() Response {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.now()
	resp := Response{Configured: true, Dir: m.src.Dir(), CheckedAt: m.checkedAt.Unix()}
	resp.Containers = m.containersView(now)
	resp.Backups = evaluateBackups(m.backup, m.lastOK, m.backupErr, now)
	resp.Alerts = append(containerAlerts(resp.Containers, m.src.Dir()), backupAlerts(resp.Backups, m.src.Dir())...)
	if resp.Alerts == nil {
		resp.Alerts = []Alert{}
	}
	sort.SliceStable(resp.Alerts, func(i, j int) bool {
		return severityRank[resp.Alerts[i].Severity] < severityRank[resp.Alerts[j].Severity]
	})
	return resp
}

var severityRank = map[string]int{"critical": 0, "warning": 1, "info": 2}

func (m *Monitor) containersView(now time.Time) ContainersView {
	v := ContainersView{Items: []Container{}}
	if m.contErr != nil && m.containers.Schema == 0 {
		v.Error = m.contErr.Error()
		return v
	}
	if m.contErr != nil {
		v.Error = m.contErr.Error() // keep showing the last good snapshot
	}
	snap := m.containers
	v.Available = true
	v.DockerOK = snap.DockerOK
	v.DockerError = snap.Error
	if !snap.CollectedAt.IsZero() {
		v.CollectedAt = snap.CollectedAt.Unix()
		v.AgeSec = int64(now.Sub(snap.CollectedAt).Seconds())
		v.Stale = now.Sub(snap.CollectedAt) > StaleAfter
	} else {
		v.Stale = true
	}
	v.Items = m.tracker.view(snap, now)
	return v
}

// --- alerts ------------------------------------------------------------------

const collectorUnit = "tf-ops-docker-snapshot"

func containerAlerts(v ContainersView, dir string) []Alert {
	var out []Alert
	collectorCommands := []monitor.Command{
		{Label: "État du collecteur (compte transcriptflow)", Command: "systemctl --user status " + collectorUnit + ".timer " + collectorUnit + ".service"},
		{Label: "Derniers journaux du collecteur", Command: "journalctl --user -u " + collectorUnit + ".service -n 50 --no-pager"},
	}
	if !v.Available {
		out = append(out, Alert{Key: "ops:containers:missing", Alert: monitor.Alert{
			Severity: "warning", Title: "Aucune donnée sur les conteneurs",
			Detail:   v.Error,
			Facts:    []monitor.Fact{{Label: "Dossier lu", Value: dir + "/" + ContainersFile}},
			Commands: collectorCommands,
			Advice:   "Le collecteur docker-snapshot.sh n'a encore rien écrit, ou le hub ne peut pas lire le dossier. Voir docs/ops.md.",
		}})
		return out
	}
	if v.Stale {
		out = append(out, Alert{Key: "ops:containers:stale", Alert: monitor.Alert{
			Severity: "critical", Title: "Le collecteur Docker ne répond plus",
			Detail:   fmt.Sprintf("Dernier relevé il y a %s.", human(v.AgeSec)),
			Facts:    []monitor.Fact{{Label: "Fichier", Value: dir + "/" + ContainersFile}},
			Commands: collectorCommands,
			Advice:   "Les états ci-dessous sont peut-être faux. Vérifie le timer côté transcriptflow.",
		}})
	}
	if !v.DockerOK {
		out = append(out, Alert{Key: "ops:docker:down", Alert: monitor.Alert{
			Severity: "critical", Title: "Docker injoignable",
			Detail:   v.DockerError,
			Commands: []monitor.Command{{Label: "État du démon Docker", Command: "systemctl status docker"}},
			Advice:   "Le collecteur n'a pas pu interroger Docker : démon arrêté ou socket inaccessible.",
		}})
	}
	for _, c := range v.Items {
		logs := monitor.Command{Label: "Derniers journaux", Command: "docker logs --tail 100 " + c.Name}
		facts := []monitor.Fact{{Label: "Image", Value: c.Image}, {Label: "État", Value: c.State}}
		if c.Health != "" {
			facts = append(facts, monitor.Fact{Label: "Santé", Value: c.Health})
		}
		if c.ComposeProject != "" {
			facts = append(facts, monitor.Fact{Label: "Compose", Value: c.ComposeProject + "/" + c.ComposeService})
		}
		switch {
		case c.State != "running" && c.Tone == "critical":
			detail := c.ExitedDesc
			if c.State == "restarting" {
				detail = "Docker le relance en boucle."
			}
			out = append(out, Alert{Key: "ops:container:" + c.Name + ":down", Alert: monitor.Alert{
				Severity: "critical", Title: "Conteneur " + c.Name + " arrêté",
				Detail: detail, Facts: facts,
				Commands: []monitor.Command{logs, {Label: "Détail", Command: "docker inspect " + c.Name + " --format '{{json .State}}'"}},
				Advice:   "Lis les derniers journaux avant de le relancer.",
			}})
		case c.Health == "unhealthy":
			out = append(out, Alert{Key: "ops:container:" + c.Name + ":unhealthy", Alert: monitor.Alert{
				Severity: "critical", Title: "Conteneur " + c.Name + " en mauvaise santé",
				Facts:    facts,
				Commands: []monitor.Command{logs, {Label: "Résultat des healthchecks", Command: "docker inspect " + c.Name + " --format '{{json .State.Health}}'"}},
				Advice:   "Le conteneur tourne mais son healthcheck échoue.",
			}})
		}
		if c.Restarts >= 3 {
			out = append(out, Alert{Key: "ops:container:" + c.Name + ":flapping", Alert: monitor.Alert{
				Severity: "warning", Title: fmt.Sprintf("Conteneur %s redémarré %d fois en 10 min", c.Name, c.Restarts),
				Facts:    append(facts, monitor.Fact{Label: "Compteur Docker", Value: strconv.Itoa(c.RestartCount)}),
				Commands: []monitor.Command{logs},
				Advice:   "Un redémarrage en boucle cache souvent une erreur de configuration ou un manque de mémoire.",
			}})
		}
	}
	return out
}

func human(sec int64) string {
	switch {
	case sec < 120:
		return strconv.FormatInt(sec, 10) + " s"
	case sec < 7200:
		return strconv.FormatInt(sec/60, 10) + " min"
	case sec < 172800:
		return strconv.FormatInt(sec/3600, 10) + " h"
	}
	return strconv.FormatInt(sec/86400, 10) + " j"
}

func itoa(v int) string { return strconv.Itoa(v) }
