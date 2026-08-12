#!/usr/bin/env bash
set -Eeuo pipefail

die() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
    die "run this installer as root (use sudo)"
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd -- "$script_dir/.." && pwd)
install_dir=/opt/nginx-overseas-cache-admin
config_dir=/etc/nginx/overseas-cache
settings_file=$config_dir/settings.conf
admin_env=$config_dir/admin.env
password_file=$config_dir/admin-password.json
upstream_target=/etc/nginx/conf.d/proxy-cache.conf
site_target=/etc/nginx/sites-available/overseas-cache
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
install_backup=$config_dir/backups/install-$timestamp
stage_dir=$(mktemp -d)
staged_upstream=$stage_dir/proxy-cache.conf
staged_site=$stage_dir/overseas-cache

cleanup_stage() {
    rm -f "$staged_upstream" "$staged_site"
    rmdir "$stage_dir" 2>/dev/null || true
}
trap cleanup_stage EXIT

for command in nginx systemctl python3 openssl awk sed grep; do
    command -v "$command" >/dev/null || die "required command is missing: $command"
done

for source in \
    "$repo_dir/admin/server.py" \
    "$repo_dir/admin/static/index.html" \
    "$repo_dir/admin/static/login.html" \
    "$repo_dir/admin/static/app.css" \
    "$repo_dir/admin/static/app.js" \
    "$repo_dir/admin/static/login.js" \
    "$repo_dir/nginx/conf.d/proxy-cache.conf" \
    "$repo_dir/nginx/sites-available/overseas-cache" \
    "$repo_dir/systemd/overseas-cache-admin.service"; do
    [[ -f "$source" ]] || die "repository file is missing: $source"
done

cp "$repo_dir/nginx/conf.d/proxy-cache.conf" "$staged_upstream"
cp "$repo_dir/nginx/sites-available/overseas-cache" "$staged_site"

# Preserve the domains currently used by the server instead of resetting them
# to the repository's example defaults during an admin-panel installation.
read_upstream_host() {
    local file=$1
    local group=$2
    awk -v wanted="$group" '
        $1 == "upstream" && $2 == wanted { inside = 1; next }
        inside && $1 == "server" {
            host = $2
            sub(/:443;$/, "", host)
            print host
            exit
        }
        inside && /^}/ { exit }
    ' "$file"
}

valid_hostname() {
    local host=$1
    [[ ${#host} -le 253 ]] || return 1
    [[ $host =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]]
}

escape_sed_pattern() {
    printf '%s' "$1" | sed 's/[][\\.^$*+?{}|()\/]/\\&/g'
}

if [[ -f "$upstream_target" ]]; then
    current_origin=$(read_upstream_host "$upstream_target" webcoder_origin)
    current_cdn=$(read_upstream_host "$upstream_target" webcoder_cdn)
    staged_origin=$(read_upstream_host "$staged_upstream" webcoder_origin)
    staged_cdn=$(read_upstream_host "$staged_upstream" webcoder_cdn)
    [[ -n "$current_origin" && -n "$current_cdn" ]] || die "could not read the current upstream domains"
    [[ -n "$staged_origin" && -n "$staged_cdn" ]] || die "could not read the repository upstream domains"
    valid_hostname "$current_origin" || die "the current origin is not a supported hostname: $current_origin"
    valid_hostname "$current_cdn" || die "the current CDN is not a supported hostname: $current_cdn"

    staged_origin_pattern=$(escape_sed_pattern "$staged_origin")
    staged_cdn_pattern=$(escape_sed_pattern "$staged_cdn")
    if [[ "$staged_origin" == "$staged_cdn" ]]; then
        [[ "$current_origin" == "$current_cdn" ]] || die "repository defaults use one hostname but the server uses separate origin and CDN hosts"
        sed -i "s/$staged_origin_pattern/origin-placeholder.invalid/g" "$staged_upstream"
        sed -i "s/origin-placeholder\.invalid/$current_origin/g" "$staged_upstream"
    else
        sed -i "s/$staged_cdn_pattern/cdn-placeholder.invalid/g" "$staged_upstream"
        sed -i "s/$staged_origin_pattern/origin-placeholder.invalid/g" "$staged_upstream"
        sed -i "s/cdn-placeholder\.invalid/$current_cdn/g" "$staged_upstream"
        sed -i "s/origin-placeholder\.invalid/$current_origin/g" "$staged_upstream"
    fi
fi

mkdir -p "$config_dir" "$config_dir/backups" "$install_backup"
chmod 700 "$config_dir" "$config_dir/backups" "$install_backup"

if [[ ! -f "$settings_file" ]]; then
    [[ -f "$site_target" ]] || die "cannot migrate settings because $site_target is missing"

    extract_setting() {
        local variable=$1
        sed -nE "s/^[[:space:]]*set[[:space:]]+\\\$$variable[[:space:]]+\"([^\"]*)\"[[:space:]]*;.*/\\1/p" "$site_target" | head -n 1
    }

    existing_channel=$(extract_setting render_channel)
    existing_account=$(extract_setting render_account)
    existing_token=$(extract_setting render_token)

    if [[ -z "$existing_channel" || -z "$existing_account" || -z "$existing_token" ]]; then
        die "could not migrate channel/account/token; create $settings_file from nginx/settings.conf.example first"
    fi

    umask 077
    {
        printf '# Migrated by install-admin.sh. This file is sensitive.\n\n'
        printf 'set $render_channel "%s";\n' "$existing_channel"
        printf 'set $render_account "%s";\n' "$existing_account"
        printf 'set $render_token   "%s";\n' "$existing_token"
    } > "$settings_file"
    printf 'Migrated the existing renderer parameters into %s.\n' "$settings_file"
fi

chown root:root "$settings_file"
chmod 600 "$settings_file"

upstream_existed=0
site_existed=0
[[ -f "$upstream_target" ]] && upstream_existed=1
[[ -f "$site_target" ]] && site_existed=1

for target in "$upstream_target" "$site_target"; do
    if [[ -f "$target" ]]; then
        cp -a "$target" "$install_backup/$(basename "$target")"
    fi
done

restore_nginx_config() {
    if [[ "$upstream_existed" == 1 ]]; then
        cp -a "$install_backup/$(basename "$upstream_target")" "$upstream_target"
    else
        rm -f "$upstream_target"
    fi
    if [[ "$site_existed" == 1 ]]; then
        cp -a "$install_backup/$(basename "$site_target")" "$site_target"
    else
        rm -f "$site_target"
    fi
}

install -o root -g root -m 0644 "$staged_upstream" "$upstream_target"
install -o root -g root -m 0644 "$staged_site" "$site_target"
ln -sfn "$site_target" /etc/nginx/sites-enabled/overseas-cache

if ! nginx -t; then
    printf 'The new Nginx configuration is invalid; restoring the previous files.\n' >&2
    restore_nginx_config
    nginx -t || true
    exit 1
fi

if ! systemctl reload nginx; then
    printf 'Nginx could not reload; restoring the previous files.\n' >&2
    restore_nginx_config
    nginx -t || true
    systemctl reload nginx || true
    exit 1
fi

install -d -o root -g root -m 0755 "$install_dir" "$install_dir/static"
install -o root -g root -m 0755 "$repo_dir/admin/server.py" "$install_dir/server.py"
install -o root -g root -m 0644 "$repo_dir/admin/static/index.html" "$install_dir/static/index.html"
install -o root -g root -m 0644 "$repo_dir/admin/static/login.html" "$install_dir/static/login.html"
install -o root -g root -m 0644 "$repo_dir/admin/static/app.css" "$install_dir/static/app.css"
install -o root -g root -m 0644 "$repo_dir/admin/static/app.js" "$install_dir/static/app.js"
install -o root -g root -m 0644 "$repo_dir/admin/static/login.js" "$install_dir/static/login.js"

new_credentials=0
if [[ ! -f "$password_file" ]]; then
    admin_user=admin
    admin_password=
    if [[ -f "$admin_env" ]]; then
        existing_user=$(sed -n 's/^OVERSEAS_ADMIN_USER=//p' "$admin_env" | head -n 1)
        existing_password=$(sed -n 's/^OVERSEAS_ADMIN_PASSWORD=//p' "$admin_env" | head -n 1)
        [[ -z "$existing_user" ]] || admin_user=$existing_user
        [[ -z "$existing_password" ]] || admin_password=$existing_password
    fi
    if [[ -z "$admin_password" ]]; then
        admin_password=$(openssl rand -hex 20)
        new_credentials=1
    fi
    printf '%s' "$admin_password" | python3 "$repo_dir/admin/server.py" \
        --init-password-file "$password_file" --username "$admin_user"
fi

allowed_networks=$(sed -n 's/^OVERSEAS_ADMIN_ALLOWED_NETWORKS=//p' "$admin_env" 2>/dev/null | head -n 1 || true)
allowed_networks=${allowed_networks:-192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,127.0.0.0/8}
umask 077
printf 'OVERSEAS_ADMIN_ALLOWED_NETWORKS=%s\n' "$allowed_networks" > "$admin_env"

for sensitive_file in "$admin_env" "$password_file"; do
    chown root:root "$sensitive_file"
    chmod 600 "$sensitive_file"
done

if [[ "$new_credentials" == 0 ]]; then
    unset admin_password
fi

install -o root -g root -m 0644 \
    "$repo_dir/systemd/overseas-cache-admin.service" \
    /etc/systemd/system/overseas-cache-admin.service

systemctl daemon-reload
systemctl enable --now overseas-cache-admin.service
systemctl restart overseas-cache-admin.service
systemctl --no-pager --full status overseas-cache-admin.service >/dev/null

server_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
server_ip=${server_ip:-SERVER_IP}

printf '\nInstallation complete.\n\n'
printf '1. Open this address directly from a trusted LAN computer:\n'
printf '   http://%s:23000\n\n' "$server_ip"
if [[ "$new_credentials" == 1 ]]; then
    printf '2. Browser sign-in credentials (save these now):\n'
    printf '   Username: admin\n'
    printf '   Password: %s\n' "$admin_password"
else
    printf '2. Existing username and password were preserved.\n'
fi
printf '\nUse the web console to change the password. The stored password is hashed and cannot be displayed.\n'
printf 'The panel accepts configured private networks only. Do not forward TCP 23000 to the Internet.\n'
