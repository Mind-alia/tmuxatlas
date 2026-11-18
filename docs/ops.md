# Ops: containers, backups and notifications

The **Conteneurs** tab has two sections, both admin-only (refused to `user`
accounts by the server, not just hidden in the interface):

- **Conteneurs**: a live view modelled on the Containers view of Docker
  Desktop, read from the Docker Engine API (`pkg/dockerui`, see
  [Live view](#live-view-pkgdockerui) below): state, health, ports, CPU and
  memory, logs, inspect, start/stop/restart/delete and a shell in a container.
- **Sauvegardes et alertes**: the alerts, the backups panel and the phone
  notifications, fed by the collector described next (`GET /api/ops`).

The phone alerts never depend on the live view: they keep reading the
collector's snapshot, so they work even with `TMUXATLAS_DOCKER=off`.

## Live view (`pkg/dockerui`)

The hub talks to the Engine API over its unix socket with `net/http`; it runs
no `docker` command, except the shell described below. Every call has a
timeout and every response is read with a size bound.

| Route (under `/api`) | Docker call | Notes |
|---|---|---|
| `GET /docker/containers` | `/containers/json?all=1`, `/info`, inspect, `/stats` | inspect cached 15 s (or until the state changes), at most 3 s for all of them, retried 15 s after a failure; stats refreshed at most every 3 s and shared by every poll |
| `GET /docker/containers/{id}/logs?tail=N&since=T` | `/logs` | stdout and stderr with timestamps, demultiplexed without TTY; the newest 2000 lines and 2 MiB, out of at most 8 MiB read |
| `GET /docker/containers/{id}/inspect` | `/containers/{id}/json` | every environment value replaced by `••••`, plus values of secret-looking keys and flags, URL passwords, and HTTP credentials in commands and healthchecks, shell strings (`CMD-SHELL`, `sh -c`) included: every `-H`/`--header` value, `Authorization` and `Proxy-Authorization`, Bearer and Basic tokens |
| `POST /docker/containers/{id}/start`, `stop`, `restart` | same | stop waits for the container's grace period |
| `DELETE /docker/containers/{id}` | `DELETE /containers/{id}` | refused (409) while running; never `force` nor `v` |
| `POST /docker/containers/{id}/terminal` | — | opens a shell in a tmux session, see below |
| `POST /docker/containers` | `/containers/create`, then `/start` | the "Run" dialog of the Images tab: image, name, ports (published on `127.0.0.1` unless `host_ip` says otherwise), `NAME=value` variables, named volumes (never a host path), restart policy; unknown fields are refused |
| `GET /docker/images` | `/images/json`, `/containers/json?all=1` | tags, size, age and the containers created from each image |
| `POST /docker/images/pull` | `/images/create?fromImage=…&tag=…` | public images only (no registry credentials); waits for the end of the download, at most 10 min; an `error` line of the stream is a failure |
| `POST /docker/images/prune` | `/images/prune` with `dangling=true` | untagged images only |
| `DELETE /docker/images/{id}` | `DELETE /images/{tag or id}` | never `force`: refused (409) while a container uses the image; an image with several tags loses them one by one |
| `GET /docker/volumes` | `/volumes`, `/containers/json?all=1`, `/system/df?type=volume` | sizes are read in the background and cached one minute (`null` until then) |
| `POST /docker/volumes` | `/volumes/create` | local driver, name only |
| `DELETE /docker/volumes/{name}` | `DELETE /volumes/{name}` | never `force`: refused (409) while a container mounts the volume; only a name Docker listed is sent |

- `{id}` must be 12 to 64 lowercase hex characters and is resolved against a
  fresh listing; only the full ID returned by Docker is used afterwards.
- The routes check the session like the others and, in accounts mode, that
  the account is an admin; the changes also check `Origin` and the CSRF
  token. They do not go through the account policy, whose lock is held for
  the whole of every request: a stop lasts the container's grace period and
  would freeze every account meanwhile. Each change, and each inspect, is
  logged with the account, the container and the result.
- The routes that take a body (`POST /docker/containers`,
  `/docker/images/pull`, `/docker/volumes`) read it whole before anything
  else, like the other JSON routes: `application/json`, one JSON value, at
  most 64 KiB, sent within 10 s. The server has no read timeout: without
  this deadline, a body sent byte by byte would hold the request for ever.
  A refused body closes the connection rather than being drained.
- Without authentication (`--no-auth`), the view stays off unless
  `TMUXATLAS_DOCKER=on`: every local account could otherwise drive Docker.
  When on, its changes still check `Origin`, like the file explorer.
- **Terminal.** The hub creates (or reuses) the tmux session
  `docker-<container name>` with the same `new` action as the *Nouvelle
  session* dialog, then replaces its shell with
  `docker --host unix://<socket> exec -it <full id> sh -c '<probe>'`, where
  the probe starts `bash` when the image has it, `sh` otherwise. tmux runs
  these arguments with `execvp`: no shell parses them on the host, and they
  contain nothing from the request. Leaving the shell closes the session; a
  failed start keeps its message on screen (`remain-on-exit failed`), and the
  next click starts it again. A live session of the same name not opened for
  this container is never typed into: the next one is `docker-<name>-2`; the
  dead shell of a container that was recreated gives its name back.
- The shell session is reserved to admins: its `@atlas-docker` option marks
  it. In accounts mode a `user` account does not see it (session lists,
  `/ws/events`, tool events, activity) and cannot open, preview, type into,
  rename, kill, claim or release it, even with its exact name. The hub
  applies the same rule to a session that an agent reports as a container
  shell. Before docker starts in the session, the terminals that `user`
  accounts hold on it are closed, so none sees the shell, not even one
  opened in the instant before the marker was set. Admins, and passkey mode
  (a single owner), keep the session as before.
- This is a rule of Atlas, not of the system: every account still shares the
  hub's Unix account, which is in the `docker` group, so a `user` account
  can still run `docker` or `tmux attach` from a shell in one of its own
  sessions. That is root on the machine, with or without this view.
- For that reason the view is refused (503, with the reason on the page)
  while an account other than an admin is active in accounts mode: reserving
  it to admins would promise a separation the system does not have. Disabling
  or deleting those accounts brings it back at once. `TMUXATLAS_DOCKER=on`
  keeps it on anyway, for an operator who accepts that every account is
  trusted with Docker. Refusing the view does not protect the socket: only
  separate Unix accounts do (sessions of `user` accounts on a peer whose
  account is not in the `docker` group), which Atlas does not set up.
- Polling stops while the page is hidden.

| Variable | Default | Role |
|---|---|---|
| `TMUXATLAS_DOCKER` | on with authentication, off without; off in accounts mode while a `user` account is active | `off` turns the live view off; `on` forces it on without authentication, or with active `user` accounts |
| `TMUXATLAS_DOCKER_SOCKET` | `/var/run/docker.sock` | Engine API socket, absolute path (`unix://` accepted) |

⚠️ Access to the Docker socket is root on the machine, and the live view
gives it to every TmuxAtlas admin. On this server the hub account is already
in the `docker` group, so its admins could already run `docker` from a
terminal; the view does not widen that, but anyone given an admin account
gets it too. Only the environment, the obvious secret names, URL passwords
and HTTP credentials are masked in *Inspecter*: a secret passed another way
(a plain command-line argument, or a flag whose name does not look secret,
for instance) still shows, and the logs are shown as the container writes
them.

## Who does what (collector and alerts)

```
 production account (owns Docker)            account running the hub
 ─────────────────────────────────           ────────────────────────
 systemd timer, every 30 s                    tmuxatlas server
   docker-snapshot.sh ──writes──▶ /srv/tf-ops/containers.json ──reads──▶ pkg/ops
                                  (mode 2750 transcriptflow:tfops,
                                   files 0640)
```

- The alerting part of the hub never talks to Docker and never runs anything
  in the production account (the live view above does talk to Docker, when
  the hub's account can open the socket).
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
- the alerts never call Docker, so moving the hub later to an account
  without Docker (a dedicated `tfmonitor`, for instance) only needs step 1;
  the live view then says that Docker is unreachable, or can be turned off
  with `TMUXATLAS_DOCKER=off`.

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
  "complete": true,
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

`complete` is `false` when `docker inspect` failed or timed out, or when there
are more than 200 containers and the rest was not read (`error` then
says why), and when `docker ps` failed (`docker_ok` false). The containers
such a snapshot lacks keep the last state the hub saw, until a complete
snapshot: a failed read never makes an outage look recovered. A container
removed between `ps` and `inspect` is enough to make one snapshot
incomplete; it is then dropped by the next one. A snapshot without the
field (collector older than it) counts as complete when `docker_ok` is true.

## Alerts

The hub reads the directory every 15 s and derives:

| Key | Severity | When |
|---|---|---|
| `ops:container:<name>:down` | critical | state `exited`, `dead` or `restarting` |
| `ops:container:<name>:unhealthy` | critical | healthcheck `unhealthy` |
| `ops:container:<name>:flapping` | warning | 3 restarts or more in 10 min |
| `ops:docker:down` | critical | the collector could not reach Docker |
| `ops:containers:incomplete` | warning | `docker inspect` could not describe every container |
| `ops:containers:stale` | critical | no new snapshot for 2 min |
| `ops:containers:missing` | warning | no snapshot at all |

Restarts are followed by container name: a new id (compose recreated the
container, its `restart_count` starts again at 0), a higher `restart_count`
or a new `started_at` each count as a restart.

## Backups

The **Sauvegardes** panel (section *Sauvegardes et alertes* of the same page) shows the date of the last full
backup, whether the copy on Cloudflare R2 succeeded and was verified, when the
next one is expected, and the retention: **3 days locally, 14 days on R2**.

The backup script itself, in the production account, publishes its progress
by sourcing `contrib/ops/backup-status.sh`. TmuxAtlas never runs, reads or
changes the backup itself.

```bash
#!/usr/bin/env bash
set -euo pipefail
export TF_OPS_DIR=/srv/tf-ops TF_LOCAL_RETENTION_DAYS=3 TF_R2_RETENTION_DAYS=14
source ~/.local/libexec/tf-ops/backup-status.sh
# job, expected interval, grace, maximum duration (seconds)
tf_status_begin db-nightly 86400 3600 7200

file=/var/backups/tf/db-$(date -u +%F).sql.zst
tf_status_step dump;        pg_dump ... | zstd -q -o "$file"
tf_status_step local-write; tf_status_local "$file" "$(ls /var/backups/tf | wc -l)"
tf_status_step r2-upload;   rc=0; rclone copy "$file" r2:tf-backups/ || rc=$?
tf_status_step r2-verify;   vrc=0; rclone check --one-way "$(dirname "$file")" r2:tf-backups/ --include "$(basename "$file")" || vrc=$?
tf_status_remote "$rc" "$vrc" "$(rclone lsf r2:tf-backups/ | wc -l)"
tf_status_step prune-local; find /var/backups/tf -name 'db-*.sql.zst' -mtime +"$TF_LOCAL_RETENTION_DAYS" -delete
if [ "$rc" = 0 ]; then
  tf_status_step prune-r2;  rclone delete r2:tf-backups/ --min-age "${TF_R2_RETENTION_DAYS}d"
fi
# The EXIT trap writes the result. A failed R2 copy (rc != 0) without exiting
# gives "partial" with failed_step r2-upload; exiting early would name the
# step running at that moment instead.
```

Install the library next to the collector:
`install -D -m 0644 contrib/ops/backup-status.sh ~/.local/libexec/tf-ops/backup-status.sh`.
Check that `sha256sum` and `rclone check` are available.

### `backup-status.json` (schema 1)

`last-success.json` has the same content, written only after a full success.
The last success is the newer of the two files: when writing
`last-success.json` fails, a successful `backup-status.json` still counts,
for the date shown and for lateness.

```json
{
  "schema": 1, "job": "db-nightly", "run_id": "20261116T030000Z-4242",
  "result": "success",
  "started_at": "2026-11-16T03:00:00Z", "finished_at": "2026-11-16T03:04:12Z",
  "exit_code": 0, "step": "prune-r2", "failed_step": "", "message": "",
  "local":  {"ok": true, "file": "db-2026-11-16.sql.zst", "size": 73400320,
             "sha256": "…", "retention_days": 3, "count": 3},
  "remote": {"provider": "r2", "attempted": true, "ok": true, "verified": true,
             "rclone_exit_code": 0, "retention_days": 14, "count": 14},
  "schedule": {"expected_interval_seconds": 86400, "grace_seconds": 3600,
               "max_duration_seconds": 7200}
}
```

`result` is `running`, `success` (local and R2 copies done, and verified when
a check ran), `partial` (local copy done, then R2 or a later step failed) or
`failure` (no valid local copy). `failed_step` is one of `dump`, `compress`,
`local-write`, `r2-upload`, `r2-verify`, `prune-local`, `prune-r2`.

### States and alerts

| State | Key | When |
|---|---|---|
| `stuck` | `ops:backup:stuck` | `running` for longer than `max_duration_seconds` |
| `failed` | `ops:backup:failed` | last run `failure` |
| `r2-failed` | `ops:backup:r2-failed` | last run `partial` |
| `late` | `ops:backup:late` | no full success for `expected_interval + grace` |
| `running` | — | a run in progress |
| `ok` | — | everything above is fine |
| `unknown` | `ops:backup:missing` | no status file yet |

The checks are applied in this order: a stuck run hides a failure, which
hides lateness.

## Phone notifications

Every 30 s the hub evaluates the ops alerts above and the sites probed by the
host monitor, and notifies the phone when something breaks or comes back.

### What notifies

| Problem | Notified after |
|---|---|
| container stopped, restarting in a loop, or unhealthy | 60 s |
| Docker unreachable | 60 s |
| container restarted 3 times in 10 min | at once |
| collector silent | 2 min without a new snapshot |
| snapshot incomplete (`docker inspect` failing) | 5 min |
| site down | 3 failed probes in a row (one probe per minute) |
| backup failed, R2 copy failed, backup stuck or late | at once |

"Not set up yet" states (`ops:containers:missing`, `ops:backup:missing`) only
show on the page.

Sites are those of `TMUXATLAS_MONITOR_URLS` and of the local Caddy
configuration. They can only be set in the environment, never from the
browser: a URL typed in the interface would let any admin session make the
hub request internal addresses (SSRF).

### Anti-spam

- One alert goes `pending → firing → resolving → ok`; one notification when it
  fires, one when it is resolved, nothing in between.
- A recovery needs 2 clear checks in a row (about 1 min). A problem that comes
  back while resolving does not notify again.
- A reminder every 6 h while it lasts.
- At least 5 min between two notifications of the same alert.
- More than 3 notifications due at once are grouped into one.
- At most 10 notifications per 10 min; the rest wait and are grouped.
- A notification counts as sent once a channel accepted it. If every channel
  fails, it is tried again after 1, 2, 4, 8 then 15 min (6 attempts). After
  that a problem still there is announced again 6 h later, and an undelivered
  recovery is dropped. With several channels, one success is enough: the
  failing one is only logged. Without any channel nothing counts as sent:
  the page still shows the alerts, but none is marked notified, enters the
  history or uses the budget.
- A problem notification not delivered yet waits while the problem looks
  gone, with its attempts and delay. It goes out if the problem comes back
  before the recovery is confirmed, and is dropped if the recovery is
  confirmed first: a problem never announced has no recovery to announce.
- States are kept in `alerts.json` (data directory), so a restart of the
  hub does not notify again.

### Channels

- **Web Push** (default): the PWA must be installed and notifications enabled
  in *Réglages*. Only admins can subscribe (`/api/push/subscribe` is admin
  only), so ops notifications only reach admins. TTL 1 h, high urgency; the
  recovery replaces the outage on the phone (same tag). On iOS, Web Push
  needs iOS 16.4 or later and the app added to the home screen. A push
  service that does not answer is given up after 10 s, and the whole channel
  after 15 s, so it cannot hold the next evaluations.
- **ntfy** (optional): set `TMUXATLAS_NTFY_URL=https://ntfy.sh/<topic>` and,
  for a protected topic, `TMUXATLAS_NTFY_TOKEN`, in the hub's
  `~/.config/tmuxatlas/.env` (not versioned, see `.env.example`). A token is
  refused over plain http. On ntfy.sh the topic name is the only secret: use a
  long random one.

`POST /api/ops/notifications/test` (button on the page, admin only, once every
30 s) sends a test through every channel; without any channel it answers 503
and sends nothing. `TMUXATLAS_OPS_NOTIFY=off` turns the notifications off.

### Limits

The hub runs on the same machine as production: if the machine or the hub
itself goes down, nothing is notified. An external check (healthchecks.io,
UptimeRobot, Cloudflare health checks) on the public site covers that case.
