#!/bin/sh
# ARTEX Daemon startup script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
# ./start.sh Run in the foreground (Ctrl-C to stop)
# nohup ./start.sh >artex.log 2>&1 & Background Presence,
# ./start.sh -addr:9000 Extra parameters are passed through to artex as is
#
# It does one thing: run artex, and restart if the process exits based on the exit code.
#
# 0 User normal stop → Break Loop,
# 75 Program request restart → Restart immediately (page clicked)"One-click update"Or"Rollback")
# Other Crash → Retry after backoff (1→2→4...Maximum of 60 seconds)
#
# Intentionally not doing download, SHA256 verification, or reinstallation here: those logic needs to be written twice on sh and bat.
# And they are precisely the most error-prone parts——Once running an unrunnable binary, this script will faithfully
# Repeatedly restarting it, users can only manually save on the machine. So validation/switching all stay in Go (selfupdate package),
# Completed by artex itself at startup, script kept simple.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] Executable file not found $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# Forward stop signal to artex core.
#
# Docker This is necessary: docker stop only sends SIGTERM to PID 1 (i.e., this script),
# Not sent to child processes. If not forwarded, artex will not receive signals and cannot do graceful shutdown, killed after 10 seconds by SIGKILL
# Hard kill, stop running tasks halfway.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while:; do
	"$BIN" "$@" &
	child=$!

	# Signal will interrupt wait and return it. >128. At this point, the child process is still performing graceful shutdown,
	# You must wait again to get its true exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] Stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] Normal Exit,"
			exit 0
			;;
		"$RESTART_CODE")
			# Update/rollback ready: rerun after artex will complete dressing on startup (see selfupdate)..Bootstrap).
			echo "[artex] Request restart (apply new version)...."
			delay=1
			;;
		*)
			echo "[artex] Abnormal Exit, (code=$code), ${delay}s Restart later" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
