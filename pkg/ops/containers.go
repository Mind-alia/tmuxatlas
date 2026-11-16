package ops

import (
	"sort"
	"strings"
	"time"
)

// ContainersFile is the name of the snapshot written by
// contrib/ops/docker-snapshot.sh.
const ContainersFile = "containers.json"

// ContainersSchema is the only containers.json schema this hub understands.
const ContainersSchema = 1

// RestartWindow is how far back restarts are counted.
const RestartWindow = 10 * time.Minute

// ContainersSnapshot is containers.json (schema 1). It only carries fields
// listed explicitly by the collector: never Env, Mounts, Args nor Health.Log.
type ContainersSnapshot struct {
	Schema      int              `json:"schema"`
	CollectedAt time.Time        `json:"collected_at"`
	DockerOK    bool             `json:"docker_ok"`
	Error       string           `json:"error,omitempty"`
	Containers  []ContainerState `json:"containers"`
}

// ContainerState is one container as reported by `docker inspect`.
type ContainerState struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Image          string    `json:"image"`
	State          string    `json:"state"`  // created | running | paused | restarting | removing | exited | dead
	Health         string    `json:"health"` // "" | starting | healthy | unhealthy
	RestartCount   int       `json:"restart_count"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	ExitCode       int       `json:"exit_code"`
	OOMKilled      bool      `json:"oom_killed"`
	ComposeProject string    `json:"compose_project,omitempty"`
	ComposeService string    `json:"compose_service,omitempty"`
}

// Container is the view sent to the browser.
type Container struct {
	ContainerState
	Status     string `json:"status"` // running | healthy | unhealthy | starting | restarting | paused | exited | dead | created
	Tone       string `json:"tone"`   // ok | warning | critical | neutral
	UptimeSec  int64  `json:"uptime_seconds,omitempty"`
	Restarts   int    `json:"restarts_10m"`
	BadSince   int64  `json:"bad_since,omitempty"` // unix seconds since the container is not ok
	ShortID    string `json:"short_id"`
	ExitedDesc string `json:"exited_desc,omitempty"`
}

// ContainersView is the containers part of GET /api/ops.
type ContainersView struct {
	Available   bool        `json:"available"`
	Error       string      `json:"error,omitempty"`
	CollectedAt int64       `json:"collected_at,omitempty"` // unix seconds
	AgeSec      int64       `json:"age_seconds,omitempty"`
	Stale       bool        `json:"stale"`
	DockerOK    bool        `json:"docker_ok"`
	DockerError string      `json:"docker_error,omitempty"`
	Items       []Container `json:"items"`
}

// containerTracker remembers what earlier snapshots showed, so restarts can
// be counted even when a container is recreated (its restart_count then
// starts again from zero) and so a failure has a start time.
type containerTracker struct {
	last     map[string]ContainerState // by name
	restarts map[string][]time.Time
	badSince map[string]time.Time
	seenAt   time.Time
}

func newContainerTracker() *containerTracker {
	return &containerTracker{
		last:     map[string]ContainerState{},
		restarts: map[string][]time.Time{},
		badSince: map[string]time.Time{},
	}
}

// observe records one snapshot taken at now. Repeated observations of the
// same snapshot are harmless.
func (t *containerTracker) observe(snap ContainersSnapshot, now time.Time) {
	if !snap.CollectedAt.IsZero() && !snap.CollectedAt.After(t.seenAt) {
		t.prune(now)
		return
	}
	t.seenAt = snap.CollectedAt
	present := map[string]bool{}
	for _, c := range snap.Containers {
		present[c.Name] = true
		if prev, ok := t.last[c.Name]; ok {
			switch {
			case prev.ID != c.ID:
				t.restarts[c.Name] = append(t.restarts[c.Name], now) // recreated
			case c.RestartCount > prev.RestartCount:
				for i := 0; i < c.RestartCount-prev.RestartCount && i < 20; i++ {
					t.restarts[c.Name] = append(t.restarts[c.Name], now)
				}
			case !c.StartedAt.Equal(prev.StartedAt) && c.StartedAt.After(prev.StartedAt):
				t.restarts[c.Name] = append(t.restarts[c.Name], now) // manual restart
			}
		}
		t.last[c.Name] = c
		if containerOK(c) {
			delete(t.badSince, c.Name)
		} else if _, ok := t.badSince[c.Name]; !ok {
			since := now
			if c.State == "exited" || c.State == "dead" {
				if !c.FinishedAt.IsZero() && c.FinishedAt.Before(now) {
					since = c.FinishedAt
				}
			}
			t.badSince[c.Name] = since
		}
	}
	for name := range t.last {
		if !present[name] {
			delete(t.last, name)
			delete(t.badSince, name)
		}
	}
	t.prune(now)
}

func (t *containerTracker) prune(now time.Time) {
	cutoff := now.Add(-RestartWindow)
	for name, times := range t.restarts {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(t.restarts, name)
		} else {
			t.restarts[name] = kept
		}
	}
}

func containerOK(c ContainerState) bool {
	return c.State == "running" && c.Health != "unhealthy"
}

// containerStatus folds state and health into one label and tone.
func containerStatus(c ContainerState) (status, tone string) {
	switch c.State {
	case "running":
		switch c.Health {
		case "healthy":
			return "healthy", "ok"
		case "unhealthy":
			return "unhealthy", "critical"
		case "starting":
			return "starting", "warning"
		}
		return "running", "ok"
	case "restarting":
		return "restarting", "critical"
	case "paused":
		return "paused", "warning"
	case "exited", "dead":
		return c.State, "critical"
	case "created", "removing":
		return c.State, "neutral"
	}
	return "unknown", "neutral"
}

func (t *containerTracker) view(snap ContainersSnapshot, now time.Time) []Container {
	items := make([]Container, 0, len(snap.Containers))
	for _, c := range snap.Containers {
		v := Container{ContainerState: c, Restarts: len(t.restarts[c.Name])}
		v.Name = strings.TrimPrefix(c.Name, "/")
		v.Status, v.Tone = containerStatus(c)
		if len(c.ID) >= 12 {
			v.ShortID = c.ID[:12]
		} else {
			v.ShortID = c.ID
		}
		if c.State == "running" && !c.StartedAt.IsZero() && c.StartedAt.Before(now) {
			v.UptimeSec = int64(now.Sub(c.StartedAt).Seconds())
		}
		if since, ok := t.badSince[c.Name]; ok {
			v.BadSince = since.Unix()
		}
		if c.State == "exited" || c.State == "dead" {
			v.ExitedDesc = exitDescription(c)
		}
		if v.Tone == "ok" && v.Restarts >= 3 {
			v.Tone = "warning"
		}
		items = append(items, v)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if toneRank[items[i].Tone] != toneRank[items[j].Tone] {
			return toneRank[items[i].Tone] < toneRank[items[j].Tone]
		}
		if items[i].ComposeProject != items[j].ComposeProject {
			return items[i].ComposeProject < items[j].ComposeProject
		}
		return items[i].Name < items[j].Name
	})
	return items
}

var toneRank = map[string]int{"critical": 0, "warning": 1, "neutral": 2, "ok": 3}

func exitDescription(c ContainerState) string {
	switch {
	case c.OOMKilled:
		return "tué par manque de mémoire (OOM)"
	case c.ExitCode == 0:
		return "arrêté normalement (code 0)"
	case c.ExitCode == 137:
		return "tué (SIGKILL, code 137)"
	case c.ExitCode == 143:
		return "arrêté par SIGTERM (code 143)"
	}
	return "sorti en erreur (code " + itoa(c.ExitCode) + ")"
}
