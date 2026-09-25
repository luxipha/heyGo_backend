#!/bin/sh
# Stop services started by scripts/dev.sh.
#
#   ./scripts/dev-stop.sh                    stop all four services
#   ./scripts/dev-stop.sh trip-service       stop one service
#
# Kafka is a separate Homebrew service:
#   brew services stop kafka

set -eu

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DEV_DIR="$REPO_ROOT/.dev"
SERVICES="trip-service driver-service payment-service api-gateway"

target="${1:-all}"

for svc in $SERVICES; do
	if [ "$target" != "all" ] && [ "$target" != "$svc" ]; then
		continue
	fi

	pid_file="$DEV_DIR/$svc.pid"
	if [ ! -f "$pid_file" ]; then
		echo "$svc: not tracked by dev.sh"
		continue
	fi

	pid=$(cat "$pid_file")
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "$svc: not running"
		rm -f "$pid_file"
		continue
	fi

	kill "$pid" 2>/dev/null || true

	# Wait up to 5s for a clean shutdown, then force it.
	n=0
	while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 10 ]; do
		sleep 0.5
		n=$((n + 1))
	done

	if kill -0 "$pid" 2>/dev/null; then
		kill -9 "$pid" 2>/dev/null || true
		echo "$svc: force-killed (pid $pid)"
	else
		echo "$svc: stopped (pid $pid)"
	fi

	rm -f "$pid_file"
done
