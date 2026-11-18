package ops

import (
	"errors"
	"fmt"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/monitor"
)

// Files written by contrib/ops/backup-status.sh.
const (
	BackupStatusFile = "backup-status.json"
	LastSuccessFile  = "last-success.json"
	BackupSchema     = 1
)

// BackupStatus is backup-status.json / last-success.json (schema 1).
type BackupStatus struct {
	Schema     int       `json:"schema"`
	Job        string    `json:"job"`
	RunID      string    `json:"run_id"`
	Result     string    `json:"result"` // running | success | failure | partial
	StartedAt  time.Time `json:"started_at"`
	FinishedAt tsOrEmpty `json:"finished_at"`
	ExitCode   int       `json:"exit_code"`
	Step       string    `json:"step,omitempty"`
	FailedStep string    `json:"failed_step,omitempty"` // dump | compress | local-write | r2-upload | r2-verify | prune-local | prune-r2
	Message    string    `json:"message,omitempty"`
	Local      struct {
		OK            bool   `json:"ok"`
		File          string `json:"file"`
		Size          int64  `json:"size"`
		SHA256        string `json:"sha256"`
		RetentionDays int    `json:"retention_days"`
		Count         int    `json:"count"`
	} `json:"local"`
	Remote struct {
		Provider       string `json:"provider"`
		Attempted      bool   `json:"attempted"`
		OK             bool   `json:"ok"`
		Verified       bool   `json:"verified"`
		RcloneExitCode int    `json:"rclone_exit_code"`
		RetentionDays  int    `json:"retention_days"`
		Count          int    `json:"count"`
	} `json:"remote"`
	Schedule struct {
		ExpectedInterval int64 `json:"expected_interval_seconds"`
		Grace            int64 `json:"grace_seconds"`
		MaxDuration      int64 `json:"max_duration_seconds"`
	} `json:"schedule"`
}

// tsOrEmpty accepts "" (a run still in progress) as the zero time.
type tsOrEmpty struct{ time.Time }

func (t *tsOrEmpty) UnmarshalJSON(b []byte) error {
	if string(b) == `""` || string(b) == "null" {
		t.Time = time.Time{}
		return nil
	}
	return t.Time.UnmarshalJSON(b)
}

func (t tsOrEmpty) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte(`""`), nil
	}
	return t.Time.MarshalJSON()
}

// Default retention, used when the status file does not say.
const (
	DefaultLocalRetentionDays = 3
	DefaultR2RetentionDays    = 14
)

// BackupView is the backups part of GET /api/ops.
type BackupView struct {
	// State: ok | running | failed | r2-failed | late | stuck | unknown
	State            string        `json:"state"`
	Tone             string        `json:"tone"`
	Reason           string        `json:"reason,omitempty"`
	Error            string        `json:"error,omitempty"`
	Current          *BackupStatus `json:"current,omitempty"`
	LastSuccess      *BackupStatus `json:"last_success,omitempty"`
	LastSuccessAt    int64         `json:"last_success_at,omitempty"` // unix seconds
	NextExpectedBy   int64         `json:"next_expected_by,omitempty"`
	LocalRetention   int           `json:"local_retention_days"`
	R2Retention      int           `json:"r2_retention_days"`
	R2OK             bool          `json:"r2_ok"`
	R2Verified       bool          `json:"r2_verified"`
	RunningForSecond int64         `json:"running_for_seconds,omitempty"`
}

func readBackup(src *Source, name string) (*BackupStatus, error) {
	var b BackupStatus
	if _, err := src.ReadJSON(name, &b); err != nil {
		return nil, err
	}
	if b.Schema != BackupSchema {
		return nil, fmt.Errorf("%s : schéma %d non pris en charge (attendu %d)", name, b.Schema, BackupSchema)
	}
	return &b, nil
}

// evaluateBackups derives the panel state. The order of the checks is the
// order of importance: a stuck run hides everything else, then a failure,
// then an R2 failure, then lateness.
func evaluateBackups(current, last *BackupStatus, readErr error, now time.Time) BackupView {
	v := BackupView{State: "unknown", Tone: "neutral", Current: current, LastSuccess: last,
		LocalRetention: DefaultLocalRetentionDays, R2Retention: DefaultR2RetentionDays}
	if readErr != nil && !errors.Is(readErr, ErrMissing) {
		v.Error = readErr.Error()
	}
	ref := current
	if ref == nil {
		ref = last
	}
	if ref == nil {
		v.Reason = "aucune sauvegarde n'a encore publié son statut"
		return v
	}
	if ref.Local.RetentionDays > 0 {
		v.LocalRetention = ref.Local.RetentionDays
	}
	if ref.Remote.RetentionDays > 0 {
		v.R2Retention = ref.Remote.RetentionDays
	}
	// The newest success wins: last-success.json is written after
	// backup-status.json, so a failed second write leaves it older than a
	// successful current status.
	if last != nil && !last.FinishedAt.IsZero() {
		v.LastSuccessAt = last.FinishedAt.Unix()
	}
	if current != nil && current.Result == "success" && !current.FinishedAt.IsZero() && current.FinishedAt.Unix() > v.LastSuccessAt {
		v.LastSuccessAt = current.FinishedAt.Unix()
	}
	interval, grace, maxDuration := ref.Schedule.ExpectedInterval, ref.Schedule.Grace, ref.Schedule.MaxDuration
	if interval <= 0 {
		interval = 86400
	}
	if grace <= 0 {
		grace = 3600
	}
	if maxDuration <= 0 {
		maxDuration = 7200
	}
	if v.LastSuccessAt > 0 {
		v.NextExpectedBy = v.LastSuccessAt + interval + grace
	}
	if current != nil {
		switch current.Result {
		case "success":
			v.R2OK, v.R2Verified = current.Remote.OK, current.Remote.Verified
		case "running":
			if last != nil {
				v.R2OK, v.R2Verified = last.Remote.OK, last.Remote.Verified
			}
		default:
			v.R2OK, v.R2Verified = current.Remote.OK, current.Remote.Verified
		}
	} else if last != nil {
		v.R2OK, v.R2Verified = last.Remote.OK, last.Remote.Verified
	}

	set := func(state, tone, reason string) { v.State, v.Tone, v.Reason = state, tone, reason }
	switch {
	case current != nil && current.Result == "running" && now.Sub(current.StartedAt) > time.Duration(maxDuration)*time.Second:
		v.RunningForSecond = int64(now.Sub(current.StartedAt).Seconds())
		set("stuck", "critical", fmt.Sprintf("en cours depuis %s (étape %s), au-delà des %s prévus", human(v.RunningForSecond), orUnknown(current.Step), human(maxDuration)))
	case current != nil && current.Result == "failure":
		set("failed", "critical", fmt.Sprintf("échec à l'étape %s (code %d)%s", orUnknown(current.FailedStep), current.ExitCode, withMessage(current.Message)))
	case current != nil && current.Result == "partial":
		set("r2-failed", "critical", fmt.Sprintf("copie locale faite, mais échec à l'étape %s%s", orUnknown(current.FailedStep), withMessage(current.Message)))
	case v.NextExpectedBy > 0 && now.Unix() > v.NextExpectedBy:
		set("late", "warning", fmt.Sprintf("dernière sauvegarde réussie il y a %s, une par %s attendue", human(now.Unix()-v.LastSuccessAt), human(interval)))
	case v.LastSuccessAt == 0 && current != nil && current.Result != "running":
		set("unknown", "neutral", "aucune sauvegarde réussie connue")
	case current != nil && current.Result == "running":
		v.RunningForSecond = int64(now.Sub(current.StartedAt).Seconds())
		set("running", "neutral", fmt.Sprintf("en cours depuis %s (étape %s)", human(v.RunningForSecond), orUnknown(current.Step)))
	default:
		set("ok", "ok", "")
		if !v.R2Verified {
			v.Reason = "copie R2 faite mais non vérifiée"
		}
	}
	return v
}

func orUnknown(s string) string {
	if s == "" {
		return "inconnue"
	}
	return s
}

func withMessage(msg string) string {
	if msg == "" {
		return ""
	}
	return " : " + msg
}

func backupAlerts(v BackupView, dir string) []Alert {
	if v.State == "ok" || v.State == "running" {
		return nil
	}
	commands := []monitor.Command{
		{Label: "Statut publié par la sauvegarde", Command: "cat " + dir + "/" + BackupStatusFile},
		{Label: "Journaux du service de sauvegarde (compte transcriptflow)", Command: "journalctl --user -u <service de sauvegarde> -n 100 --no-pager"},
	}
	facts := []monitor.Fact{}
	if v.LastSuccessAt > 0 {
		facts = append(facts, monitor.Fact{Label: "Dernière réussite", Value: time.Unix(v.LastSuccessAt, 0).Format("02/01/2006 15:04")})
	}
	if v.Current != nil {
		facts = append(facts, monitor.Fact{Label: "Tâche", Value: v.Current.Job}, monitor.Fact{Label: "Exécution", Value: v.Current.RunID})
	}
	alert := func(severity, title, advice string) []Alert {
		return []Alert{{Key: "ops:backup:" + v.State, Alert: monitor.Alert{
			Severity: severity, Title: title, Detail: v.Reason, Facts: facts, Commands: commands, Advice: advice,
		}}}
	}
	switch v.State {
	case "failed":
		return alert("critical", "Sauvegarde en échec", "Aucune nouvelle copie n'a été faite. Lis les journaux de la sauvegarde avant de la relancer.")
	case "r2-failed":
		return alert("critical", "Copie de la sauvegarde sur R2 en échec", "La copie locale existe mais ne sera gardée que "+fmt.Sprint(v.LocalRetention)+" jours. Vérifie rclone et les accès R2.")
	case "late":
		return alert("warning", "Sauvegarde en retard", "Le timer de sauvegarde ne s'est peut-être pas déclenché.")
	case "stuck":
		return alert("critical", "Sauvegarde bloquée", "Le processus tourne trop longtemps : disque plein, verrou de base de données ou réseau vers R2.")
	}
	if v.Current == nil && v.LastSuccess == nil {
		return []Alert{{Key: "ops:backup:missing", Alert: monitor.Alert{
			Severity: "warning", Title: "Aucune donnée de sauvegarde", Detail: v.Error,
			Facts:  []monitor.Fact{{Label: "Dossier lu", Value: dir + "/" + BackupStatusFile}},
			Advice: "Le script de sauvegarde doit sourcer contrib/ops/backup-status.sh. Voir docs/ops.md.",
		}}}
	}
	return alert("warning", "État de la sauvegarde inconnu", "Aucune exécution réussie n'a été publiée.")
}
