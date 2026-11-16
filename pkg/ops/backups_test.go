package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backupAt(result string, started, finished time.Time) *BackupStatus {
	b := &BackupStatus{Schema: 1, Job: "db", RunID: "r1", Result: result, StartedAt: started, FinishedAt: tsOrEmpty{finished}}
	b.Local.OK = result != "failure"
	b.Local.RetentionDays = 3
	b.Remote.OK = result == "success"
	b.Remote.Verified = result == "success"
	b.Remote.RetentionDays = 14
	b.Schedule.ExpectedInterval = 86400
	b.Schedule.Grace = 3600
	b.Schedule.MaxDuration = 7200
	return b
}

func TestEvaluateBackups(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	okRun := backupAt("success", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	oldRun := backupAt("success", now.Add(-27*time.Hour), now.Add(-26*time.Hour))
	failed := backupAt("failure", now.Add(-time.Hour), now.Add(-50*time.Minute))
	failed.FailedStep, failed.ExitCode = "dump", 2
	partial := backupAt("partial", now.Add(-time.Hour), now.Add(-50*time.Minute))
	partial.FailedStep = "r2-upload"
	running := backupAt("running", now.Add(-10*time.Minute), time.Time{})
	running.Step = "r2-upload"
	stuck := backupAt("running", now.Add(-3*time.Hour), time.Time{})
	stuck.Step = "dump"
	unverified := backupAt("success", now.Add(-3*time.Hour), now.Add(-2*time.Hour))
	unverified.Remote.Verified = false

	for _, tc := range []struct {
		name          string
		current, last *BackupStatus
		state, reason string
	}{
		{"rien", nil, nil, "unknown", "aucune sauvegarde"},
		{"réussie", okRun, okRun, "ok", ""},
		{"échec", failed, okRun, "failed", "étape dump (code 2)"},
		{"R2 en échec", partial, okRun, "r2-failed", "r2-upload"},
		{"en retard", oldRun, oldRun, "late", "il y a 26 h"},
		{"en cours", running, okRun, "running", "étape r2-upload"},
		{"bloquée", stuck, oldRun, "stuck", "étape dump"},
		{"échec plus fort que retard", failed, oldRun, "failed", "dump"},
		{"non vérifiée", unverified, unverified, "ok", "non vérifiée"},
		{"seulement last-success", nil, okRun, "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := evaluateBackups(tc.current, tc.last, nil, now)
			if v.State != tc.state || (tc.reason != "" && !strings.Contains(v.Reason, tc.reason)) {
				t.Fatalf("état %q (%s), attendu %q (%s)", v.State, v.Reason, tc.state, tc.reason)
			}
			if v.LocalRetention != 3 || v.R2Retention != 14 {
				t.Fatalf("rétention : %d / %d", v.LocalRetention, v.R2Retention)
			}
			alerts := backupAlerts(v, "/srv/tf-ops")
			if (v.State == "ok" || v.State == "running") != (len(alerts) == 0) {
				t.Fatalf("alertes pour %s : %+v", v.State, alerts)
			}
			if len(alerts) > 0 && tc.current != nil && alerts[0].Key != "ops:backup:"+v.State {
				t.Fatalf("clé : %s", alerts[0].Key)
			}
		})
	}
	if v := evaluateBackups(okRun, okRun, nil, now); v.LastSuccessAt != okRun.FinishedAt.Unix() || v.NextExpectedBy != okRun.FinishedAt.Unix()+86400+3600 || !v.R2OK || !v.R2Verified {
		t.Fatalf("dates : %+v", v)
	}
}

// runBackup sources backup-status.sh in a fake backup script.
func runBackup(t *testing.T, dir, body string) {
	t.Helper()
	lib := contribScript(t, "backup-status.sh")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "dump.sql.zst"), []byte("données"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\nsource " + lib + "\ncd " + work + "\n" + body
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TF_OPS_DIR=" + dir, "HOME=" + work}
	_ = cmd.Run() // failures are part of the scenarios
}

func TestBackupStatusLibrary(t *testing.T) {
	now := time.Now()
	t.Run("réussite", func(t *testing.T) {
		dir := t.TempDir()
		runBackup(t, dir, `tf_status_begin db-nightly 86400 1800 3600
tf_status_step dump
tf_status_step local-write
tf_status_local dump.sql.zst 3
tf_status_step r2-upload
tf_status_remote 0 0 14
tf_status_step prune-local
`)
		b, err := readBackup(NewSource(dir), BackupStatusFile)
		if err != nil {
			t.Fatal(err)
		}
		if b.Result != "success" || b.Job != "db-nightly" || !b.Local.OK || b.Local.Size != int64(len("données")) || len(b.Local.SHA256) != 64 ||
			b.Local.File != "dump.sql.zst" || b.Local.Count != 3 || b.Local.RetentionDays != 3 ||
			!b.Remote.OK || !b.Remote.Verified || b.Remote.RetentionDays != 14 || b.Remote.Count != 14 ||
			b.Schedule.ExpectedInterval != 86400 || b.Schedule.Grace != 1800 || b.Schedule.MaxDuration != 3600 {
			t.Fatalf("statut : %+v", b)
		}
		if b.FinishedAt.IsZero() || b.FinishedAt.Sub(now) > time.Minute || b.FinishedAt.Sub(now) < -time.Minute {
			t.Fatalf("finished_at : %v", b.FinishedAt)
		}
		last, err := readBackup(NewSource(dir), LastSuccessFile)
		if err != nil || last.RunID != b.RunID {
			t.Fatalf("last-success : %+v %v", last, err)
		}
		info, _ := os.Stat(filepath.Join(dir, BackupStatusFile))
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("droits : %v", info.Mode().Perm())
		}
	})
	t.Run("échec du dump", func(t *testing.T) {
		dir := t.TempDir()
		runBackup(t, dir, "tf_status_begin db\ntf_status_step dump\nfalse\n")
		b, err := readBackup(NewSource(dir), BackupStatusFile)
		if err != nil {
			t.Fatal(err)
		}
		if b.Result != "failure" || b.FailedStep != "dump" || b.ExitCode != 1 {
			t.Fatalf("statut : %+v", b)
		}
		if _, err := os.Stat(filepath.Join(dir, LastSuccessFile)); !os.IsNotExist(err) {
			t.Fatal("last-success écrit après un échec")
		}
	})
	t.Run("échec R2", func(t *testing.T) {
		dir := t.TempDir()
		runBackup(t, dir, `tf_status_begin db
tf_status_step dump
tf_status_local dump.sql.zst 2
tf_status_step r2-upload
rc=0; false || rc=$?
tf_status_remote "$rc"
tf_status_message "rclone: 403 \"AccessDenied\""
`)
		b, err := readBackup(NewSource(dir), BackupStatusFile)
		if err != nil {
			t.Fatal(err)
		}
		if b.Result != "partial" || b.FailedStep != "r2-upload" || b.Remote.OK || b.Remote.RcloneExitCode != 1 || b.Message != `rclone: 403 "AccessDenied"` {
			t.Fatalf("statut : %+v", b)
		}
		v := evaluateBackups(b, nil, nil, time.Now())
		if v.State != "r2-failed" {
			t.Fatalf("état : %+v", v)
		}
	})
	t.Run("vérification R2 en échec", func(t *testing.T) {
		dir := t.TempDir()
		runBackup(t, dir, "tf_status_begin db\ntf_status_local dump.sql.zst\ntf_status_step r2-verify\ntf_status_remote 0 1\n")
		b, _ := readBackup(NewSource(dir), BackupStatusFile)
		if b == nil || b.Result != "partial" || b.FailedStep != "r2-verify" || !b.Remote.OK || b.Remote.Verified {
			t.Fatalf("statut : %+v", b)
		}
	})
	t.Run("en cours", func(t *testing.T) {
		dir := t.TempDir()
		lib := contribScript(t, "backup-status.sh")
		cmd := exec.Command("bash", "-c", "source "+lib+"\ntf_status_begin db\ntf_status_step dump\ntrap - EXIT\n")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "TF_OPS_DIR=" + dir}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		b, err := readBackup(NewSource(dir), BackupStatusFile)
		if err != nil || b.Result != "running" || b.Step != "dump" || !b.FinishedAt.IsZero() {
			t.Fatalf("statut : %+v %v", b, err)
		}
	})
}

func TestMonitorServesBackups(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m := New(dir)
	m.now = func() time.Time { return now }
	failed := backupAt("failure", now.Add(-time.Hour), now.Add(-50*time.Minute))
	failed.FailedStep = "compress"
	writeJSON(t, dir, BackupStatusFile, failed)
	writeJSON(t, dir, LastSuccessFile, backupAt("success", now.Add(-25*time.Hour), now.Add(-24*time.Hour)))
	m.Refresh()
	resp := m.Snapshot()
	if resp.Backups.State != "failed" || resp.Backups.LastSuccessAt != now.Add(-24*time.Hour).Unix() {
		t.Fatalf("sauvegardes : %+v", resp.Backups)
	}
	found := false
	for _, a := range resp.Alerts {
		found = found || a.Key == "ops:backup:failed"
	}
	if !found {
		t.Fatalf("alerte absente : %+v", resp.Alerts)
	}
	if err := os.WriteFile(filepath.Join(dir, BackupStatusFile), []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	m.Refresh()
	if resp := m.Snapshot(); resp.Backups.State != "failed" || resp.Backups.Error == "" {
		t.Fatalf("un fichier illisible doit garder le dernier état et signaler l'erreur : %+v", resp.Backups)
	}
}
