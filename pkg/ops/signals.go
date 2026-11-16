package ops

import (
	"fmt"
	"strings"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/monitor"
)

// Delays before a problem becomes a notification (docs/ops.md).
const (
	ContainerPending = 60 * time.Second
	// SiteFailures consecutive failed probes make a site "down". The host
	// monitor probes every minute.
	SiteFailures = 3
)

// pendingFor returns how long an ops alert must last before it notifies, and
// false for alerts that never notify (setup states).
func pendingFor(key string) (time.Duration, bool) {
	switch {
	case key == "ops:containers:missing" || key == "ops:backup:missing" || key == "ops:backup:unknown":
		return 0, false // not set up yet: shown on the page only
	case strings.HasSuffix(key, ":down") || strings.HasSuffix(key, ":unhealthy"):
		return ContainerPending, true // container down/unhealthy, Docker down
	}
	return 0, true // flapping, stale collector, backup failed/r2-failed/late/stuck
}

// OpsSignals turns the alerts of an ops snapshot into signals.
func OpsSignals(resp Response) []Signal {
	var out []Signal
	for _, a := range resp.Alerts {
		pending, notify := pendingFor(a.Key)
		if !notify {
			continue
		}
		out = append(out, Signal{Key: a.Key, Severity: a.Severity, Title: a.Title, Detail: a.Detail, Pending: pending})
	}
	return out
}

// SiteSignals reports the probed sites whose last SiteFailures probes all
// failed. The URLs come from TMUXATLAS_MONITOR_URLS and the local Caddy
// configuration, never from the browser.
func SiteSignals(endpoints []monitor.Endpoint) []Signal {
	var out []Signal
	for _, e := range endpoints {
		h := e.History
		if len(h) < SiteFailures {
			continue
		}
		down := true
		for _, p := range h[len(h)-SiteFailures:] {
			down = down && !p.Up
		}
		if !down {
			continue
		}
		detail := fmt.Sprintf("%d vérifications de suite en échec", SiteFailures)
		if e.Error != "" {
			detail += " : " + e.Error
		} else if e.Status != 0 {
			detail += fmt.Sprintf(" : HTTP %d", e.Status)
		}
		out = append(out, Signal{Key: "site:" + e.URL + ":down", Severity: "critical",
			Title: "Site " + hostOf(e.URL) + " ne répond plus", Detail: detail})
	}
	return out
}

func hostOf(url string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	return strings.TrimSuffix(s, "/")
}
