#!/usr/bin/env bash
# docker-snapshot.sh — writes the state of every Docker container to
# $TF_OPS_DIR/containers.json (schema 1) for the TmuxAtlas "Conteneurs" tab.
#
# Runs in the account that owns Docker (transcriptflow), from a systemd
# timer. It is the only process that touches the Docker socket. It runs a
# fixed command: `docker ps -aq` then `docker inspect --format` with an
# explicit list of fields. It never reads Env, Mounts, Args, Cmd, Labels other
# than the two compose labels, nor Health.Log (healthcheck output can hold
# secrets). It never reads its input from TmuxAtlas.
#
# Install a reviewed copy in the owner's account (not a path that the agents
# account can write). See docs/ops.md.
set -euo pipefail
umask 027
export LC_ALL=C

OUT_DIR=${TF_OPS_DIR:-/srv/tf-ops}
DOCKER=${TF_OPS_DOCKER:-docker}
MAX_CONTAINERS=200

# One JSON object per container. Each value goes through the template's json
# function, so names and images are escaped by Docker itself. Docker runs the
# template on the inspect JSON and fails on a missing key: Health is absent for
# a container without a healthcheck (e.g. Traefik), so optional keys are read
# with index, which yields nothing instead of dropping the container.
readonly FORMAT='{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Config.Image}},"state":{{json .State.Status}},"health":{{if index .State "Health"}}{{json .State.Health.Status}}{{else}}""{{end}},"restart_count":{{json .RestartCount}},"started_at":{{json .State.StartedAt}},"finished_at":{{json .State.FinishedAt}},"exit_code":{{json .State.ExitCode}},"oom_killed":{{json .State.OOMKilled}},"compose_project":{{with index .Config "Labels"}}{{json (index . "com.docker.compose.project")}}{{else}}""{{end}},"compose_service":{{with index .Config "Labels"}}{{json (index . "com.docker.compose.service")}}{{else}}""{{end}}}'

json_string() { # escapes $1 as a JSON string (one line, at most 300 chars)
  local s=${1:0:300}
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=$(printf '%s' "$s" | tr '\000-\037' ' ')
  printf '"%s"' "$s"
}

now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker_ok=true
error=""
items=""

if ! ids=$(timeout 20 "$DOCKER" ps -aq --no-trunc 2>&1); then
  docker_ok=false
  error=$(printf '%s' "$ids" | head -n 1)
  ids=""
fi
if [ -n "$ids" ]; then
  # shellcheck disable=SC2086 # ids are hex strings from docker ps
  set -- $(printf '%s\n' "$ids" | head -n "$MAX_CONTAINERS")
  if lines=$(timeout 20 "$DOCKER" inspect --format "$FORMAT" "$@" 2>/dev/null); then
    items=$(printf '%s\n' "$lines" | grep '^{' | paste -sd, - || true)
  else
    # A container may vanish between ps and inspect: keep what was printed.
    items=$(printf '%s\n' "${lines:-}" | grep '^{' | paste -sd, - || true)
  fi
fi

mkdir -p "$OUT_DIR"
tmp=$(mktemp "$OUT_DIR/.containers.json.XXXXXX")
trap 'rm -f "$tmp"' EXIT
printf '{"schema":1,"collected_at":"%s","docker_ok":%s,"error":%s,"containers":[%s]}\n' \
  "$now" "$docker_ok" "$(json_string "$error")" "$items" >"$tmp"
chmod 0640 "$tmp"
mv -f "$tmp" "$OUT_DIR/containers.json"
trap - EXIT
