#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 ]] || die 'run as root'
action=${1:-install}
case $action in install|rollback|uninstall) ;; *) die 'usage: install-admin.sh [install|rollback|uninstall]';; esac
source_dir=$(cd -- "$(dirname -- "$0")/.." && pwd)
app=/opt/overseas-cache-v2
etc=/etc/nginx/overseas-cache
state=/var/lib/overseas-cache-admin
history=/var/lib/overseas-cache-installer
upstream=/etc/nginx/conf.d/proxy-cache.conf
site=/etc/nginx/sites-available/overseas-cache
enabled=/etc/nginx/sites-enabled/overseas-cache
lock=$etc/.config.lock
legacy_lock=/run/overseas-cache-admin.lock
units=(ws-fanout.service overseas-cache-admin.service overseas-cache-config.service overseas-cache-config.socket)
paths=("$upstream" "$site" "$enabled" "$etc/settings.conf" "$etc/admin-password.json"
    "$etc/admin-v2.env" "$state/admin-password.json" "$app"
    /etc/logrotate.d/overseas-cache-v2)
for unit in "${units[@]}"; do paths+=("/etc/systemd/system/$unit"); done
for cmd in nginx systemctl flock install runuser awk dpkg; do command -v "$cmd" >/dev/null || die "missing $cmd"; done

if [[ $action == install ]]; then
    version=$(nginx -v 2>&1)
    version=${version##*/}
    version=${version%% *}
    dpkg --compare-versions "$version" ge 1.19.3 || die 'Nginx 1.19.3 or newer is required'
fi

mkdir -p "$history" "$etc" "$etc/backups"
chmod 700 "$history" "$etc" "$etc/backups"
exec 8>"$history/install.lock"
flock -w 30 8 || die 'another installer is running'
touch "$lock" "$legacy_lock"
chmod 600 "$lock" "$legacy_lock"
exec 7<>"$legacy_lock"
flock -w 30 7 || die 'another V1 configuration operation is running'
exec 9<>"$lock"
flock -w 85 9 || die 'another configuration transaction is running'

install_atomic() {
    local input=$1 destination=$2 mode=$3 temporary
    temporary=$(mktemp -- "$(dirname "$destination")/.overseas-install.XXXXXX")
    install -m "$mode" "$input" "$temporary"
    mv -fT -- "$temporary" "$destination"
}
nginx_reload() {
    nginx -t -q >/dev/null 2>&1 && systemctl reload nginx && systemctl is-active --quiet nginx
}
remember() {
    local directory=$1 path unit
    mkdir "$directory"
    for path in "${paths[@]}"; do
        if [[ -e $path || -L $path ]]; then cp -a --parents -- "$path" "$directory"; fi
    done
    for unit in "${units[@]}"; do
        if systemctl is-enabled --quiet "$unit" 2>/dev/null; then touch "$directory/$unit.enabled"; fi
        if systemctl is-active --quiet "$unit" 2>/dev/null; then touch "$directory/$unit.active"; fi
    done
}
stop_v2() {
    systemctl stop overseas-cache-admin.service overseas-cache-config.socket overseas-cache-config.service ws-fanout.service 2>/dev/null || true
}
restore() {
    local directory=$1 path unit status=0
    stop_v2
    for unit in "${units[@]}"; do systemctl disable "$unit" >/dev/null 2>&1 || true; done
    for path in "${paths[@]}"; do
        rm -rf -- "$path" || status=1
        if [[ -e $directory$path || -L $directory$path ]]; then
            mkdir -p "$(dirname "$path")" && cp -a -- "$directory$path" "$path" || status=1
        fi
    done
    systemctl daemon-reload || status=1
    for unit in overseas-cache-config.socket overseas-cache-config.service ws-fanout.service overseas-cache-admin.service; do
        if [[ -f $directory/$unit.enabled ]]; then systemctl enable "$unit" >/dev/null || status=1; fi
        if [[ -f $directory/$unit.active ]]; then systemctl start "$unit" || status=1; fi
    done
    nginx_reload || status=1
    return "$status"
}

if [[ $action == rollback || $action == uninstall ]]; then
    [[ -d $history/original ]] || die 'no pre-V2 installation snapshot'
    snapshot=$(mktemp -d -- "$history/pre-$action.XXXXXX")
    rmdir "$snapshot"
    remember "$snapshot"
    if [[ -f $state/admin-password.json && -f $history/original/etc/systemd/system/overseas-cache-admin.service ]]; then
        modern_password=$snapshot$state/admin-password.json
    else
        modern_password=
    fi
    if ! restore "$history/original"; then
        restore "$snapshot" || die 'could not restore either installation; inspect installation snapshots'
        die "could not $action; prior installation restored"
    fi
    if [[ -n $modern_password ]]; then
        install_atomic "$modern_password" "$etc/admin-password.json" 0600
        systemctl try-restart overseas-cache-admin.service
    fi
    printf '%s complete; previous V2 snapshot: %s\n' "$action" "$snapshot"
    exit 0
fi

for bin in overseas-admin ws-fanout config-helper; do
    [[ -x $source_dir/bin/$bin ]] || die 'run from a built V2 release archive (see docs/DEPLOYMENT.md)'
    "$source_dir/bin/$bin" --version >/dev/null || die "binary $bin cannot run on this host"
done
if [[ -e $etc/backups/pending ]]; then
    [[ -x $app/bin/config-helper ]] || die 'recover the interrupted configuration transaction before upgrading'
    flock -u 9
    "$app/bin/config-helper" --recover
    flock -w 85 9 || die 'configuration lock is busy'
fi

workspace=$(mktemp -d -- "$history/stage.XXXXXX")
snapshot=$(mktemp -d -- "$history/before.XXXXXX")
rmdir "$snapshot"
remember "$snapshot"
rollback_install() {
    local code=$?
    trap - EXIT
    rm -rf -- "$workspace"
    if (( code != 0 )); then
        if restore "$snapshot"; then printf 'Install failed. Previous installation restored.\n' >&2
        else printf 'Install failed. Automatic recovery incomplete. Snapshot %s\n' "$snapshot" >&2
        fi
    fi
    exit "$code"
}
trap rollback_install EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for account in overseas-admin overseas-fanout; do
    if ! id "$account" >/dev/null 2>&1; then
        useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$account"
    fi
done
install -d -m 0700 -o overseas-admin -g overseas-admin "$state"
install -d -m 0755 "$app/bin" /var/log/overseas-cache
# On a fresh host the cache parent can be absent. Create it with explicit
# traversal permission for the packaged Nginx worker account.
if [[ ! -d /var/cache/nginx ]]; then
    install -d -m 0750 -o root -g www-data /var/cache/nginx
fi
runuser -u www-data -- test -x /var/cache/nginx || die 'Nginx workers cannot traverse /var/cache/nginx'
install -d -m 0750 -o www-data -g www-data /var/cache/nginx/web_cache
if [[ ! -e /var/log/overseas-cache/traffic.log ]]; then
    install -m 0640 -o www-data -g overseas-admin /dev/null /var/log/overseas-cache/traffic.log
fi
install -m 0600 /dev/null "$workspace/settings"
if [[ -f $etc/settings.conf ]]; then
    cp "$etc/settings.conf" "$workspace/settings"
elif [[ -f $site ]]; then
    awk '/^[ \t]*set[ \t]+\$render_(channel|account|token)[ \t]+/ { print; count++ }
         END { if (count != 3) exit 1 }' "$site" > "$workspace/settings" ||
        die 'old renderer settings could not be migrated'
else
    cp "$source_dir/nginx/settings.conf.example" "$workspace/settings"
fi

old_host() {
    awk -v wanted="$2" '
      $1=="upstream" && $2==wanted { inside=1; next }
      inside && $1=="server" { sub(/:443;$/, "", $2); print $2; exit }
      inside && $1=="}" { exit }
    ' "$1"
}
valid_host() { [[ $1 =~ ^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ && ${#1} -le 253 ]]; }
template=$source_dir/nginx/conf.d/proxy-cache.conf
cp "$template" "$workspace/upstream"
if [[ -f $upstream ]]; then
    old_origin=$(old_host "$upstream" webcoder_origin)
    old_cdn=$(old_host "$upstream" webcoder_cdn)
    valid_host "$old_origin" && valid_host "$old_cdn" || die 'cannot preserve the current upstream hosts'
    # Replace only the managed server and map entries, independently of equal hosts.
    awk -v origin="$old_origin" -v cdn="$old_cdn" '
      /^upstream webcoder_origin / || /^map \$host \$overseas_origin_host / { h=origin }
      /^upstream webcoder_cdn / || /^map \$host \$overseas_cdn_host / { h=cdn }
      h!="" && $1=="server" { print "    server "h":443;"; next }
      h!="" && $1=="default" { print "    default "h";"; next }
      /^}/ { h="" }
      { print }
    ' "$template" > "$workspace/upstream"
fi

stop_v2
for bin in overseas-admin ws-fanout config-helper; do
    install_atomic "$source_dir/bin/$bin" "$app/bin/$bin" 0755
done
if [[ ! -f $state/admin-password.json ]]; then
    if [[ -f $etc/admin-password.json ]]; then
        install -m 0600 -o overseas-admin -g overseas-admin "$etc/admin-password.json" "$state/admin-password.json"
    else
        legacy_user=
        legacy_password=
        if [[ -f $etc/admin.env ]]; then
            legacy_user=$(sed -n 's/^OVERSEAS_ADMIN_USER=//p' "$etc/admin.env")
            legacy_password=$(sed -n 's/^OVERSEAS_ADMIN_PASSWORD=//p' "$etc/admin.env")
        fi
        if [[ -z $legacy_password ]]; then
            [[ -t 0 ]] || die 'new installation requires a password on stdin from a TTY, or a preinitialized V1 password file'
            read -r -s -p 'Admin password (12–256 characters): ' legacy_password
            printf '\n'
            read -r -s -p 'Confirm Admin password: ' confirmation
            printf '\n'
            [[ $legacy_password == "$confirmation" ]] || die 'passwords differ'
            unset confirmation
        fi
        printf '%s' "$legacy_password" | runuser -u overseas-admin -- "$app/bin/overseas-admin" \
            --init-password-file "$state/admin-password.json" --username "${legacy_user:-admin}"
        unset legacy_password
    fi
fi
if [[ ! -f $etc/admin-v2.env ]]; then
    networks=
    if [[ -f $etc/admin.env ]]; then
        networks=$(sed -n 's/^OVERSEAS_ADMIN_ALLOWED_NETWORKS=//p' "$etc/admin.env")
    fi
    {
        printf 'OVERSEAS_ADMIN_HOST=127.0.0.1\n'
        if [[ $networks =~ ^[0-9a-fA-F:./,[:space:]]+$ ]]; then printf 'OVERSEAS_ADMIN_ALLOWED_NETWORKS=%s\n' "$networks"; fi
    } > "$workspace/env"
    install_atomic "$workspace/env" "$etc/admin-v2.env" 0600
fi
install_atomic "$workspace/settings" "$etc/settings.conf" 0600
install_atomic "$workspace/upstream" "$upstream" 0644
install_atomic "$source_dir/nginx/sites-available/overseas-cache" "$site" 0644
ln -sfn "$site" "$enabled"
for unit in "${units[@]}"; do
    install_atomic "$source_dir/systemd/v2/$unit" "/etc/systemd/system/$unit" 0644
done

cat > "$workspace/logrotate" <<'EOF'
/var/log/overseas-cache/traffic.log {
    daily
    rotate 7
    size 20M
    missingok
    notifempty
    compress
    delaycompress
    create 0640 www-data overseas-admin
    postrotate
        /usr/bin/systemctl kill -s USR1 --kill-who=main nginx.service >/dev/null 2>&1 || true
    endscript
}
EOF
install_atomic "$workspace/logrotate" /etc/logrotate.d/overseas-cache-v2 0644
systemctl daemon-reload
systemctl enable --now ws-fanout.service overseas-cache-config.socket overseas-cache-admin.service >/dev/null
nginx_reload || die 'Nginx validation or reload failed'
for service in ws-fanout.service overseas-cache-config.socket overseas-cache-admin.service; do
    systemctl is-active --quiet "$service" || die "unit failed to start: $service"
done
if command -v curl >/dev/null; then
    curl --noproxy '*' --fail --silent --show-error --retry 5 --retry-connrefused \
        --max-time 2 http://127.0.0.1:3001/healthz >/dev/null
fi
if [[ ! -e $history/original ]]; then mv -T -- "$snapshot" "$history/original"; fi
printf 'V2 installed. Management defaults to http://127.0.0.1:23000.\n'
