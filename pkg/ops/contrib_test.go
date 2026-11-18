package ops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"
)

// These tests run the scripts of contrib/ops against a fake docker binary.
// They never touch a real Docker daemon.

func contribScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "contrib", "ops", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash absent")
	}
	return path
}

// fakeDocker writes a docker stub that records its arguments (one per line)
// in args.log and prints canned output. inspect also prints inspect.err on
// stderr and exits with the code in inspect.rc, when those files exist.
func fakeDocker(t *testing.T, dir, psOut, inspectOut string, psFails bool) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "ps.out"), []byte(psOut), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inspect.out"), []byte(inspectOut), 0o600); err != nil {
		t.Fatal(err)
	}
	fail := ""
	if psFails {
		fail = `echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. "Is it running?"' >&2; exit 1`
	}
	script := `#!/usr/bin/env bash
dir=$(dirname "$0")
printf '%s\n' "--call--" "$@" >>"$dir/args.log"
case "$1" in
  ps) ` + fail + `
      cat "$dir/ps.out" ;;
  inspect) cat "$dir/inspect.out"
      if [ -f "$dir/inspect.err" ]; then cat "$dir/inspect.err" >&2; fi
      exit "$(cat "$dir/inspect.rc" 2>/dev/null || echo 0)" ;;
  *) exit 3 ;;
esac
`
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Docker-like objects with only the fields the collector may read. The test
// runs the collector's template on their JSON form, as `docker inspect
// --format` does: it fails if the template asks for anything else (Env,
// Mounts, Args, Health.Log...) or for a key Docker leaves out, such as Health
// for a container without a healthcheck.
type fakeHealth struct{ Status string }
type fakeState struct {
	Status     string
	Health     *fakeHealth `json:",omitempty"`
	StartedAt  string
	FinishedAt string
	ExitCode   int
	OOMKilled  bool
}
type fakeConfig struct {
	Image  string
	Labels map[string]string `json:",omitempty"`
}
type fakeContainer struct {
	Id           string
	Name         string
	RestartCount int
	Config       fakeConfig
	State        fakeState
}

func TestDockerSnapshotScript(t *testing.T) {
	script := contribScript(t, "docker-snapshot.sh")
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "status")
	docker := fakeDocker(t, bin, "aaa111\nbbb222\n", "", false)
	run := func() {
		t.Helper()
		cmd := exec.Command("bash", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "TF_OPS_DIR=" + out, "TF_OPS_DOCKER=" + docker, "HOME=" + bin}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script : %v\n%s", err, output)
		}
	}
	run() // empty inspect output
	logged, err := os.ReadFile(filepath.Join(bin, "args.log"))
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimPrefix(string(logged), "--call--\n"), "--call--\n")
	if len(calls) != 2 {
		t.Fatalf("appels docker : %q", calls)
	}
	if calls[0] != "ps\n-aq\n--no-trunc\n" {
		t.Fatalf("appel ps : %q", calls[0])
	}
	args := strings.Split(strings.TrimSuffix(calls[1], "\n"), "\n")
	if len(args) != 5 || args[0] != "inspect" || args[1] != "--format" || args[3] != "aaa111" || args[4] != "bbb222" {
		t.Fatalf("appel inspect : %q", args)
	}
	format := args[2]
	for _, forbidden := range []string{"Env", "Mounts", "Args", "Cmd", ".Log", "Entrypoint", "HostConfig", "{{json .}}", "{{json .Config}}", "{{json .State}}"} {
		if strings.Contains(format, forbidden) {
			t.Fatalf("le format lit %q : %s", forbidden, format)
		}
	}
	tmpl, err := template.New("f").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err },
	}).Parse(format)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range []fakeContainer{
		{Id: "aaa111", Name: "/web", RestartCount: 2,
			Config: fakeConfig{Image: "nginx:1", Labels: map[string]string{"com.docker.compose.project": "tf", "com.docker.compose.service": "web"}},
			State:  fakeState{Status: "running", Health: &fakeHealth{Status: "healthy"}, StartedAt: "2026-11-16T10:00:00.123456789Z", FinishedAt: "0001-01-01T00:00:00Z"}},
		{Id: "bbb222", Name: `/odd"name`, Config: fakeConfig{Image: "busybox"},
			State: fakeState{Status: "exited", StartedAt: "2026-11-16T09:00:00Z", FinishedAt: "2026-11-16T09:30:00Z", ExitCode: 137, OOMKilled: true}},
	} {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, doc); err != nil {
			t.Fatalf("le format lit un champ non prévu : %v", err)
		}
		lines = append(lines, buf.String())
	}
	if err := os.WriteFile(filepath.Join(bin, "inspect.out"), []byte(strings.Join(lines, "\n")+"\nError: No such object: ccc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run()

	info, err := os.Stat(filepath.Join(out, ContainersFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("droits : %v", info.Mode().Perm())
	}
	m := New(out)
	m.Refresh()
	resp := m.Snapshot()
	if resp.Containers.Error != "" || !resp.Containers.DockerOK || len(resp.Containers.Items) != 2 {
		t.Fatalf("relevé : %+v", resp.Containers)
	}
	if age := time.Since(time.Unix(resp.Containers.CollectedAt, 0)); age > time.Minute || age < -time.Minute {
		t.Fatalf("collected_at : %d", resp.Containers.CollectedAt)
	}
	byName := map[string]Container{}
	for _, c := range resp.Containers.Items {
		byName[c.Name] = c
	}
	if c := byName["web"]; c.Status != "healthy" || c.ComposeService != "web" || c.RestartCount != 2 {
		t.Fatalf("web : %+v", c)
	}
	if c := byName[`odd"name`]; c.Status != "exited" || !c.OOMKilled || c.ExitCode != 137 {
		t.Fatalf("odd : %+v", c)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 {
		t.Fatalf("fichiers temporaires laissés : %v", entries)
	}
}

func TestDockerSnapshotScriptReportsDockerDown(t *testing.T) {
	script := contribScript(t, "docker-snapshot.sh")
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "status")
	docker := fakeDocker(t, bin, "", "", true)
	cmd := exec.Command("bash", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TF_OPS_DIR=" + out, "TF_OPS_DOCKER=" + docker, "HOME=" + bin}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script : %v\n%s", err, output)
	}
	var snap ContainersSnapshot
	if _, err := NewSource(out).ReadJSON(ContainersFile, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.DockerOK || !strings.Contains(snap.Error, `"Is it running?"`) || len(snap.Containers) != 0 {
		t.Fatalf("Docker arrêté : %+v", snap)
	}
}

// docker ps works but docker inspect fails, after printing part of its
// output or nothing at all (timeout): the snapshot says it is incomplete,
// and the hub keeps the outage it knew about.
func TestDockerSnapshotScriptMarksFailedInspectIncomplete(t *testing.T) {
	script := contribScript(t, "docker-snapshot.sh")
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "status")
	docker := fakeDocker(t, bin, "aaa111\nbbb222\n", "", false)
	now := time.Now().UTC()
	web := container("web", "exited", "", 0, now.Add(-time.Hour))
	web.ID, web.FinishedAt, web.ExitCode = "aaa111", now.Add(-10*time.Minute), 1
	api := container("api", "running", "", 0, now.Add(-time.Hour))
	api.ID = "bbb222"
	line := func(c ContainerState) string {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return string(data) + "\n"
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(out)
	run := func(stdout, stderr, rc string) (map[string]any, Response) {
		t.Helper()
		write("inspect.out", stdout)
		write("inspect.err", stderr)
		write("inspect.rc", rc)
		cmd := exec.Command("bash", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "TF_OPS_DIR=" + out, "TF_OPS_DOCKER=" + docker, "HOME=" + bin}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script : %v\n%s", err, output)
		}
		var raw map[string]any
		if _, err := NewSource(out).ReadJSON(ContainersFile, &raw); err != nil {
			t.Fatal(err)
		}
		m.Refresh()
		return raw, m.Snapshot()
	}
	count := func(raw map[string]any) int {
		list, _ := raw["containers"].([]any)
		return len(list)
	}

	raw, resp := run(line(web)+line(api), "", "0")
	if raw["docker_ok"] != true || raw["complete"] != true || count(raw) != 2 || !alertKeys(resp)["ops:container:web:down"] {
		t.Fatalf("relevé complet : %v %v", raw, alertKeys(resp))
	}
	raw, resp = run(line(api), "Error response from daemon: i/o timeout\n", "1")
	if raw["docker_ok"] != true || raw["complete"] != false || count(raw) != 1 || !strings.Contains(fmt.Sprint(raw["error"]), "i/o timeout") {
		t.Fatalf("inspect partiel puis en échec : %v", raw)
	}
	if keys := alertKeys(resp); !keys["ops:container:web:down"] || !keys["ops:containers:incomplete"] {
		t.Fatalf("la panne connue de web doit rester : %v", keys)
	}
	raw, resp = run("", "", "124")
	if raw["docker_ok"] != true || raw["complete"] != false || count(raw) != 0 || !strings.Contains(fmt.Sprint(raw["error"]), "20 s") {
		t.Fatalf("inspect sans réponse : %v", raw)
	}
	if keys := alertKeys(resp); !keys["ops:container:web:down"] || len(resp.Containers.Items) != 2 {
		t.Fatalf("après un inspect sans réponse : %v %+v", keys, resp.Containers.Items)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 {
		t.Fatalf("fichiers temporaires laissés : %v", entries)
	}
}
