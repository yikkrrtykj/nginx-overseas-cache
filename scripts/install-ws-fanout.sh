#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec bash "$root/scripts/install-admin.sh" "$@"
