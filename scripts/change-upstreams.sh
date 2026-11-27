#!/usr/bin/env bash
set -Eeuo pipefail
[[ $EUID == 0 && $# -ge 1 && $# -le 2 ]] ||
    { printf 'Usage: sudo bash scripts/change-upstreams.sh ORIGIN [CDN]\n' >&2; exit 2; }
exec /opt/overseas-cache-v2/bin/config-helper --set-origin="$1" --set-cdn="${2-}"
