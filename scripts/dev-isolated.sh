#!/bin/sh
# Persistent, loopback-only local backend with dependency and readiness checks.
set -eu
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 "$repo_root/scripts/dev-isolated.py" "$@"
