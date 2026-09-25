#!/bin/sh
# Local dev runner for the heyGo backend.
#
#   ./scripts/dev.sh                    start all four services in the background
#   ./scripts/dev.sh trip-service       run one service in the foreground
#   ./scripts/dev.sh -w trip-service    run one service with hot reload (air)
#   ./scripts/dev.sh -h                 show this help
#
# Stop everything with: ./scripts/dev-stop.sh
#
# Environment comes from .env.local (gitignored). Kafka must be running:
#   brew services start kafka
#
# Background logs: .dev/<service>.log   (tail -f .dev/trip-service.log)

set -eu

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$REPO_ROOT"

ENV_FILE="${ENV_FILE:-$REPO_ROOT/.env.local}"
DEV_DIR="$REPO_ROOT/.dev"
SERVICES="trip-service driver-service payment-service api-gateway"

usage() {
	sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
	-h|--help)
		usage
		exit 0
		;;
esac

if [ ! -f "$ENV_FILE" ]; then
	echo "error: $ENV_FILE not found" >&2
	echo "       copy .env.local.example or create it with DATABASE_URL and KAFKA_BROKERS" >&2
	exit 1
fi

# air is installed by `go install github.com/air-verse/air@latest`
PATH="$HOME/go/bin:$PATH"
export PATH

# Export every variable in the env file to the service processes.
set -a
. "$ENV_FILE"
set +a

watch=""
if [ "${1:-}" = "-w" ] || [ "${1:-}" = "--watch" ]; then
	watch=1
	shift
fi

# Run a single service in the foreground so its logs stream to this terminal.
run_one() {
	svc="$1"
	if [ -n "$watch" ]; then
		if ! command -v air >/dev/null 2>&1; then
			echo "error: air not found. Install it with:" >&2
			echo "       go install github.com/air-verse/air@latest" >&2
			exit 1
		fi
		echo "watching services/$svc and shared/ (Ctrl+C to stop)"
		exec air -root "$REPO_ROOT" \
			-build.cmd "go build -o ./tmp/air/$svc ./services/$svc" \
			-build.bin "./tmp/air/$svc" \
			-build.include_dir "./services/$svc" \
			-build.include_dir "./shared"
	fi
	echo "running $svc (Ctrl+C to stop)"
	exec go run "./services/$svc"
}

if [ "$#" -gt 0 ]; then
	run_one "$1"
fi

# No arguments: start everything in the background.
mkdir -p "$DEV_DIR/bin"

for svc in $SERVICES; do
	pid_file="$DEV_DIR/$svc.pid"
	if [ -f "$pid_file" ] && kill -0 "$(cat "$pid_file")" 2>/dev/null; then
		echo "$svc already running (pid $(cat "$pid_file"))"
		continue
	fi
	echo "building $svc..."
	go build -o "$DEV_DIR/bin/$svc" "./services/$svc"
	nohup "$DEV_DIR/bin/$svc" > "$DEV_DIR/$svc.log" 2>&1 &
	echo $! > "$pid_file"
	echo "started $svc (pid $!) -> .dev/$svc.log"
done

echo
echo "api-gateway  http://localhost:8080/health"
echo "payment      http://localhost:9200/health"
echo "trip (gRPC)  localhost:9000    driver (gRPC)  localhost:9100"
echo
echo "stop with: ./scripts/dev-stop.sh"
