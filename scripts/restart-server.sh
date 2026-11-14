#!/bin/sh
# Restart the installed Linux user service without interrupting tmux sessions.
set -eu

service=tmuxatlas.service
binary="${TMUXATLAS_BINARY:-tmuxatlas}"
export LC_ALL=C

systemctl --user is-active --quiet "$service" || {
	echo "$service is not running." >&2
	exit 1
}
if [ "$(systemctl --user show "$service" --property=KillMode --value)" != process ]; then
	echo "Restart refused: $service must use KillMode=process to preserve tmux." >&2
	exit 1
fi
command -v "$binary" >/dev/null
"$binary" healthcheck

snapshot_dir="$(mktemp -d "${TMPDIR:-/tmp}/tmuxatlas-restart.XXXXXX")"
trap 'rm -rf "$snapshot_dir"' EXIT
trap 'exit 1' HUP INT TERM

snapshot() {
	tmux list-sessions -F '#{session_id} #{session_name}' > "$snapshot_dir/sessions.raw"
	tmux list-panes -a -F '#{session_id} #{pane_id} #{pane_pid}' > "$snapshot_dir/panes.raw"
	sort "$snapshot_dir/sessions.raw" > "$snapshot_dir/$1.sessions"
	sort "$snapshot_dir/panes.raw" > "$snapshot_dir/$1.panes"
}

tmux_pid="$(tmux display-message -p '#{pid}')"
snapshot before
echo "Restarting $service; keeping tmux server $tmux_pid and its sessions."
systemctl --user restart "$service"

attempt=0
until "$binary" healthcheck >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 30 ]; then
		echo "Atlas did not become ready; inspect journalctl --user -u $service." >&2
		exit 1
	fi
	sleep 1
done

if [ "$(tmux display-message -p '#{pid}')" != "$tmux_pid" ]; then
	echo "The tmux server changed during the restart." >&2
	exit 1
fi
snapshot after
comm -23 "$snapshot_dir/before.sessions" "$snapshot_dir/after.sessions" > "$snapshot_dir/missing.sessions"
comm -23 "$snapshot_dir/before.panes" "$snapshot_dir/after.panes" > "$snapshot_dir/missing.panes"
if [ -s "$snapshot_dir/missing.sessions" ] || [ -s "$snapshot_dir/missing.panes" ]; then
	echo "Some sessions or pane processes changed during the restart:" >&2
	cat "$snapshot_dir/missing.sessions" "$snapshot_dir/missing.panes" >&2
	exit 1
fi
echo "Atlas is ready. All $(wc -l < "$snapshot_dir/before.sessions" | tr -d ' ') tmux sessions and their pane processes survived."
