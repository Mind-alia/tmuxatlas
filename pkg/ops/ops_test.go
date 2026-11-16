package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeJSON(t *testing.T, dir, name string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o640); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	src := NewSource(dir)
	var v map[string]any

	if _, err := src.ReadJSON(ContainersFile, &v); err != ErrMissing {
		t.Fatalf("fichier absent : %v", err)
	}
	target := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(target, []byte(`{"secret":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadJSON("link.json", &v); err == nil {
		t.Fatal("lien symbolique suivi")
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.json"), 0o600); err == nil {
		if _, err := src.ReadJSON("fifo.json", &v); err == nil || !strings.Contains(err.Error(), "ordinaire") {
			t.Fatalf("FIFO acceptée : %v", err)
		}
	}
	big := strings.Repeat(" ", MaxFileSize+1) + "{}"
	if err := os.WriteFile(filepath.Join(dir, "big.json"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := src.ReadJSON("big.json", &v); err == nil {
		t.Fatal("fichier trop gros accepté")
	}
	for _, name := range []string{"../x.json", "a/b.json", ".."} {
		if _, err := src.ReadJSON(name, &v); err == nil {
			t.Fatalf("nom %q accepté", name)
		}
	}
}

func TestSourceNeverWrites(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, ContainersFile, ContainersSnapshot{Schema: 1})
	if err := os.Chmod(filepath.Join(dir, ContainersFile), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o550); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
	m := New(dir)
	m.Refresh()
	if snap := m.Snapshot(); !snap.Containers.Available {
		t.Fatalf("dossier en lecture seule illisible : %+v", snap.Containers)
	}
}

func container(name, state, health string, restarts int, started time.Time) ContainerState {
	return ContainerState{ID: "id-" + name + "-0123456789", Name: "/" + name, Image: "img/" + name,
		State: state, Health: health, RestartCount: restarts, StartedAt: started}
}

func TestContainersViewAndAlerts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m := New(dir)
	m.now = func() time.Time { return now }

	exited := container("worker", "exited", "", 0, now.Add(-time.Hour))
	exited.FinishedAt = now.Add(-5 * time.Minute)
	exited.ExitCode = 1
	writeJSON(t, dir, ContainersFile, ContainersSnapshot{Schema: 1, CollectedAt: now, DockerOK: true, Containers: []ContainerState{
		container("web", "running", "healthy", 0, now.Add(-2*time.Hour)),
		container("db", "running", "unhealthy", 0, now.Add(-time.Hour)),
		exited,
	}})
	m.Refresh()
	resp := m.Snapshot()
	if !resp.Containers.Available || resp.Containers.Stale || len(resp.Containers.Items) != 3 {
		t.Fatalf("vue : %+v", resp.Containers)
	}
	byName := map[string]Container{}
	for _, c := range resp.Containers.Items {
		byName[c.Name] = c
	}
	if c := byName["web"]; c.Status != "healthy" || c.Tone != "ok" || c.UptimeSec != 7200 || c.ShortID != "id-web-01234" {
		t.Fatalf("web : %+v", c)
	}
	if c := byName["worker"]; c.Tone != "critical" || c.BadSince != exited.FinishedAt.Unix() || !strings.Contains(c.ExitedDesc, "code 1") {
		t.Fatalf("worker : %+v", c)
	}
	if resp.Containers.Items[2].Name != "web" {
		t.Fatalf("les conteneurs sains doivent venir en dernier : %+v", resp.Containers.Items)
	}
	keys := map[string]bool{}
	for _, a := range resp.Alerts {
		keys[a.Key] = true
	}
	for _, want := range []string{"ops:container:worker:down", "ops:container:db:unhealthy"} {
		if !keys[want] {
			t.Errorf("alerte %s absente : %v", want, keys)
		}
	}
	if keys["ops:container:web:down"] {
		t.Error("alerte sur un conteneur sain")
	}
}

func TestRestartsAreCountedAcrossRecreation(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m := New(dir)
	m.now = func() time.Time { return now }
	step := func(c ContainerState) {
		now = now.Add(30 * time.Second)
		writeJSON(t, dir, ContainersFile, ContainersSnapshot{Schema: 1, CollectedAt: now, DockerOK: true, Containers: []ContainerState{c}})
		m.Refresh()
	}
	c := container("api", "running", "", 0, now)
	step(c)
	c.RestartCount = 2 // two restarts by the restart policy
	c.StartedAt = now
	step(c)
	c.ID = "recreated-0123456789" // compose recreated it: the counter restarts at 0
	c.RestartCount = 0
	c.StartedAt = now
	step(c)
	resp := m.Snapshot()
	if got := resp.Containers.Items[0].Restarts; got != 3 {
		t.Fatalf("redémarrages : %d", got)
	}
	found := false
	for _, a := range resp.Alerts {
		found = found || a.Key == "ops:container:api:flapping"
	}
	if !found {
		t.Fatal("alerte de redémarrages en boucle absente")
	}
	now = now.Add(11 * time.Minute)
	m.Refresh() // same file: nothing new, but old restarts expire
	if got := m.Snapshot().Containers.Items[0].Restarts; got != 0 {
		t.Fatalf("redémarrages après 10 min : %d", got)
	}
}

func TestStaleAndMissingSnapshots(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	m := New(dir)
	m.now = func() time.Time { return now }
	m.Refresh()
	resp := m.Snapshot()
	if resp.Containers.Available || len(resp.Alerts) != 2 || resp.Alerts[0].Key != "ops:containers:missing" || resp.Alerts[1].Key != "ops:backup:missing" {
		t.Fatalf("absence : %+v", resp)
	}
	writeJSON(t, dir, ContainersFile, ContainersSnapshot{Schema: 1, CollectedAt: now.Add(-5 * time.Minute), DockerOK: false, Error: "Cannot connect"})
	m.Refresh()
	resp = m.Snapshot()
	keys := map[string]bool{}
	for _, a := range resp.Alerts {
		keys[a.Key] = true
	}
	if !resp.Containers.Stale || !keys["ops:containers:stale"] || !keys["ops:docker:down"] {
		t.Fatalf("relevé périmé : %+v", resp)
	}
	writeJSON(t, dir, ContainersFile, map[string]any{"schema": 2})
	m.Refresh()
	resp = m.Snapshot()
	if !resp.Containers.Available || !strings.Contains(resp.Containers.Error, "schéma 2") {
		t.Fatalf("un schéma inconnu doit garder le dernier relevé et signaler l'erreur : %+v", resp.Containers)
	}
}
