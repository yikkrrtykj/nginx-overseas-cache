#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  sudo bash scripts/change-upstreams.sh NEW_ORIGIN_HOST [NEW_CDN_HOST]

Examples:
  sudo bash scripts/change-upstreams.sh api.example.com
  sudo bash scripts/change-upstreams.sh api.example.com cdn.example.com

Supply hostnames only, without https://, paths, ports or trailing slashes.
When NEW_CDN_HOST is omitted, the current CDN hostname is kept.
EOF
}

die() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
    die "run this script as root (use sudo)"
fi

if [[ $# -lt 1 || $# -gt 2 ]]; then
    usage
    exit 2
fi

upstream_config=/etc/nginx/conf.d/proxy-cache.conf
site_config=/etc/nginx/sites-available/overseas-cache
backup_root=/etc/nginx/overseas-cache/backups

[[ -f "$upstream_config" ]] || die "missing $upstream_config"
[[ -f "$site_config" ]] || die "missing $site_config"

valid_hostname() {
    local host=$1
    [[ ${#host} -le 253 ]] || return 1
    [[ $host =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]]
}

read_upstream_host() {
    local group=$1
    awk -v wanted="$group" '
        $1 == "upstream" && $2 == wanted { inside = 1; next }
        inside && $1 == "server" {
            host = $2
            sub(/:443;$/, "", host)
            print host
            exit
        }
        inside && /^}/ { exit }
    ' "$upstream_config"
}

current_origin=$(read_upstream_host webcoder_origin)
current_cdn=$(read_upstream_host webcoder_cdn)

[[ -n "$current_origin" ]] || die "could not detect the current origin hostname"
[[ -n "$current_cdn" ]] || die "could not detect the current CDN hostname"

new_origin=${1,,}
new_cdn=${2:-$current_cdn}
new_cdn=${new_cdn,,}

valid_hostname "$new_origin" || die "invalid origin hostname: $new_origin"
valid_hostname "$new_cdn" || die "invalid CDN hostname: $new_cdn"

if [[ "$current_origin" == "$current_cdn" && "$new_origin" != "$new_cdn" ]]; then
    die "the current origin and CDN use one hostname; split them manually once before using this script"
fi

if [[ "$current_origin" == "$new_origin" && "$current_cdn" == "$new_cdn" ]]; then
    printf 'Nothing to change. Origin=%s CDN=%s\n' "$current_origin" "$current_cdn"
    exit 0
fi

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="$backup_root/$timestamp"
mkdir -p "$backup_dir"
cp -a "$upstream_config" "$backup_dir/proxy-cache.conf"
cp -a "$site_config" "$backup_dir/overseas-cache"

restore_backup() {
    cp -a "$backup_dir/proxy-cache.conf" "$upstream_config"
    cp -a "$backup_dir/overseas-cache" "$site_config"
}

rollback_needed=1

rollback_on_failure() {
    local status=$1
    trap - ERR INT TERM
    if [[ "$rollback_needed" == 1 ]]; then
        printf 'Update interrupted; restoring %s\n' "$backup_dir" >&2
        restore_backup
        nginx -t || true
    fi
    exit "$status"
}

trap 'rollback_on_failure $?' ERR
trap 'rollback_on_failure 130' INT
trap 'rollback_on_failure 143' TERM

escape_sed_pattern() {
    printf '%s' "$1" | sed 's/[][\\.^$*+?{}|()\/]/\\&/g'
}

old_origin_pattern=$(escape_sed_pattern "$current_origin")
old_cdn_pattern=$(escape_sed_pattern "$current_cdn")

# Replace through reserved placeholders. A direct two-step replacement is not
# safe when a new hostname happens to equal one of the old hostnames.
origin_placeholder=origin-placeholder.invalid
cdn_placeholder=cdn-placeholder.invalid

if grep -Fq "$origin_placeholder" "$upstream_config" "$site_config" ||
   grep -Fq "$cdn_placeholder" "$upstream_config" "$site_config"; then
    die "reserved replacement placeholder already exists in an Nginx config"
fi

if [[ "$current_origin" == "$current_cdn" ]]; then
    sed -i "s/$old_origin_pattern/$origin_placeholder/g" "$upstream_config" "$site_config"
else
    sed -i "s/$old_cdn_pattern/$cdn_placeholder/g" "$upstream_config" "$site_config"
    sed -i "s/$old_origin_pattern/$origin_placeholder/g" "$upstream_config" "$site_config"
    sed -i "s/$cdn_placeholder/$new_cdn/g" "$upstream_config" "$site_config"
fi

sed -i "s/$origin_placeholder/$new_origin/g" "$upstream_config" "$site_config"

if ! nginx -t; then
    printf 'Nginx validation failed; restoring %s\n' "$backup_dir" >&2
    restore_backup
    rollback_needed=0
    nginx -t || true
    exit 1
fi

if ! systemctl reload nginx; then
    printf 'Nginx reload failed; restoring %s\n' "$backup_dir" >&2
    restore_backup
    rollback_needed=0
    nginx -t || true
    systemctl reload nginx || true
    exit 1
fi

rollback_needed=0
trap - ERR INT TERM

printf 'Updated successfully.\n'
printf '  Origin: %s -> %s\n' "$current_origin" "$new_origin"
printf '  CDN:    %s -> %s\n' "$current_cdn" "$new_cdn"
printf '  Backup: %s\n' "$backup_dir"
printf 'The cache key now uses the new hostnames, so cached objects from the old domains will not be served.\n'
