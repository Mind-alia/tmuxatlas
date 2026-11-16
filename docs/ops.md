# Ops: containers, backups and notifications

The **Conteneurs** tab shows the Docker containers of the production account
(state, health, restarts, uptime). Only admins can see it: `GET /api/ops` is
refused to `user` accounts by the server, not just hidden in the interface.

## Who does what

```
 production account (owns Docker)            account running the hub
 ─────────────────────────────────           ────────────────────────
 systemd timer, every 30 s                    tmuxatlas server
   docker-snapshot.sh ──writes──▶ /srv/tf-ops/containers.json ──reads──▶ pkg/ops
                                  (mode 2750 transcriptflow:tfops,
                                   files 0640)
```

- The hub **never** talks to Docker and never runs anything in the production
  account. Ideally the account that runs the hub is not in the `docker` group.
- `docker-snapshot.sh` is the only process that touches the Docker socket. It
  runs a fixed command and reads nothing from TmuxAtlas.
- The hub only opens the status files read-only. It refuses symlinks,
  non-regular files and files above 256 KiB.

### Why a shared directory and not an `--ops-only` peer

Two read-only transports were considered:

1. **An `agent --ops-only` peer** in the production account that sends the
   status to the hub over the peer WebSocket.
2. **A directory readable by a group**, written by the production account and
   read by the hub.

The fork already runs its hub on the same machine as production, so (2) is
enough, and it is the simplest and the safest:

- **No shell is possible by construction.** There is no connection from the
  hub to the production account at all, so there is no PTY, tmux or runtime
  message to filter. With a peer, the safety would rest on code that strips
  `pty-open` and the runtime operations from a binary that normally has them.
- **Nothing built by the hub's account runs in production.** A peer would
  require the production account to run a `tmuxatlas` binary. If that binary
  comes from code the agents account can change, whoever controls agents
  controls production. Here production only runs a 60-line shell script that a
  human reviews once and copies into its own account.
- **Read-only is enforced by the kernel**, not by the protocol: the group has
  `r-x` on the directory and `r--` on the files.
- No new port, no pairing, no peer name collision.

If the hub ever moves to another machine, the peer option becomes necessary;
it should then be a separate binary without the PTY and tmux code.

### Where the hub runs today

On the current server the hub (`tmuxatlas server`, port 7654) runs **as
transcriptflow**, the production account, which is in the `docker` and `sudo`
groups. The `agents` account only runs a `tmuxatlas agent` peer. So:

- the hub can read the status directory without any group (step 1 below is
  optional: `install -d -m 0750 /srv/tf-ops` as transcriptflow is enough, or
  use `~/.local/state/tf-ops`);
- the ops code still never calls Docker, so moving the hub later to an account
  without Docker (a dedicated `tfmonitor`, for instance) only needs step 1.

Running the hub in the production account means that a flaw in the hub gives
Docker, hence root. Moving it is recommended but out of scope here.

## Installation (by a human, on the production side)

Nothing here is installed by TmuxAtlas. Read `contrib/ops/docker-snapshot.sh`
before copying it.

```bash
# 1. As root: a group for readers, the status directory. Only needed when the
#    hub runs in another account; replace "tfmonitor" by that account.
sudo groupadd --system tfops
sudo usermod -aG tfops tfmonitor
sudo install -d -o transcriptflow -g tfops -m 2750 /srv/tf-ops

# 2. As transcriptflow: copy a reviewed version of the collector into a path
#    that only transcriptflow can write (never run it from another account's
#    checkout).
install -D -m 0755 contrib/ops/docker-snapshot.sh ~/.local/libexec/tf-ops/docker-snapshot.sh
install -D -m 0644 contrib/ops/systemd/tf-ops-docker-snapshot.service ~/.config/systemd/user/tf-ops-docker-snapshot.service
install -D -m 0644 contrib/ops/systemd/tf-ops-docker-snapshot.timer ~/.config/systemd/user/tf-ops-docker-snapshot.timer
systemctl --user daemon-reload
systemctl --user enable --now tf-ops-docker-snapshot.timer
loginctl enable-linger transcriptflow   # timers keep running without a session

# 3. Check
systemctl --user status tf-ops-docker-snapshot.service
ls -l /srv/tf-ops                      # containers.json, -rw-r----- transcriptflow tfops
sudo -u tfmonitor cat /srv/tf-ops/containers.json   # readable (if step 1)
sudo -u tfmonitor touch /srv/tf-ops/x               # must fail: Permission denied
sudo -u agents cat /srv/tf-ops/containers.json      # must fail: agents is not a reader

# 4. The hub: add to its environment, then restart it.
TMUXATLAS_OPS_DIR=/srv/tf-ops
```

The new group only applies to processes started after `usermod`: restart the
user manager of the hub's account (`sudo systemctl restart user@$(id -u tfmonitor)`)
or reboot.

If the service fails with `218/CAPABILITIES` or `status=226/NAMESPACE`, the
`--user` manager cannot apply the sandbox options (`ProtectSystem`,
`PrivateTmp`, `ReadWritePaths`): remove them from the unit.

## Configuration

| Variable | Where | Default | Role |
|---|---|---|---|
| `TMUXATLAS_OPS_DIR` | hub | empty (tab shows how to set it up) | status directory to read |
| `TF_OPS_DIR` | collector | `/srv/tf-ops` | where to write |
| `TF_OPS_DOCKER` | collector | `docker` | Docker CLI to call |

## `containers.json` (schema 1)

```json
{
  "schema": 1,
  "collected_at": "2026-11-16T12:00:00Z",
  "docker_ok": true,
  "error": "",
  "containers": [
    {
      "id": "4f1c…", "name": "/web", "image": "nginx:1.27",
      "state": "running", "health": "healthy", "restart_count": 0,
      "started_at": "2026-11-16T08:00:00.1Z", "finished_at": "0001-01-01T00:00:00Z",
      "exit_code": 0, "oom_killed": false,
      "compose_project": "transcriptflow", "compose_service": "web"
    }
  ]
}
```

The collector asks `docker inspect --format` for exactly these fields. It never
reads `Env`, `Mounts`, `Args`, `Cmd`, other labels nor `Health.Log`. A test
(`pkg/ops/contrib_test.go`) runs the script against a fake `docker` and fails
if the template reads any other field.

## Alerts

The hub reads the directory every 15 s and derives:

| Key | Severity | When |
|---|---|---|
| `ops:container:<name>:down` | critical | state `exited`, `dead` or `restarting` |
| `ops:container:<name>:unhealthy` | critical | healthcheck `unhealthy` |
| `ops:container:<name>:flapping` | warning | 3 restarts or more in 10 min |
| `ops:docker:down` | critical | the collector could not reach Docker |
| `ops:containers:stale` | critical | no new snapshot for 2 min |
| `ops:containers:missing` | warning | no snapshot at all |

Restarts are followed by container name: a new id (compose recreated the
container, its `restart_count` starts again at 0), a higher `restart_count`
or a new `started_at` each count as a restart.
