package server

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/monitor"
	"github.com/LosFurina/tmuxatlas/pkg/ops"
	"github.com/LosFurina/tmuxatlas/pkg/paths"
	"github.com/sirupsen/logrus"
)

// alertInterval is the cadence of the notification engine.
const alertInterval = 30 * time.Second

// newAlertEngine starts the phone notifications for containers, backups and
// probed sites. It returns nil when there is nothing to watch or when
// TMUXATLAS_OPS_NOTIFY=off.
func newAlertEngine(ctx context.Context, opts *Options, opsMonitor *ops.Monitor, hostMonitor *monitor.Monitor) *ops.Engine {
	if opsMonitor == nil && hostMonitor == nil {
		return nil
	}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("TMUXATLAS_OPS_NOTIFY"))); v == "off" || v == "false" || v == "0" {
		return nil
	}
	logger := logrus.WithField("component", "ops-alerting")
	var notifiers []ops.Notifier
	if opts.PushSender != nil {
		notifiers = append(notifiers, ops.WebPushNotifier{Push: opts.PushSender.Broadcast})
	}
	if topic := strings.TrimSpace(os.Getenv("TMUXATLAS_NTFY_URL")); topic != "" {
		click := strings.TrimRight(opts.PublicURL, "/") + "/ops"
		if n, err := ops.NewNtfyNotifier(topic, os.Getenv("TMUXATLAS_NTFY_TOKEN"), click); err != nil {
			logger.WithError(err).Warn("ntfy désactivé")
		} else {
			notifiers = append(notifiers, n)
		}
	}
	path := ""
	if dir, err := paths.DataDir(); err == nil && os.MkdirAll(dir, 0o700) == nil {
		path = ops.DefaultAlertsPath(dir)
	}
	engine := ops.NewEngine(path, ops.DefaultPolicy, notifiers...)
	go func() {
		ticker := time.NewTicker(alertInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				engine.Evaluate(ctx, collectSignals(opsMonitor, hostMonitor))
			}
		}
	}()
	logger.WithField("channels", engine.Notifiers()).Info("notifications ops actives")
	return engine
}

func collectSignals(opsMonitor *ops.Monitor, hostMonitor *monitor.Monitor) []ops.Signal {
	var signals []ops.Signal
	if opsMonitor != nil {
		signals = append(signals, ops.OpsSignals(opsMonitor.Snapshot())...)
	}
	if hostMonitor != nil {
		signals = append(signals, ops.SiteSignals(hostMonitor.Endpoints())...)
	}
	return signals
}
