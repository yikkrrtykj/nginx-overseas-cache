#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
[[ $EUID == 0 ]] || die 'run as root'
action=${1:-install}
case $action in install|rollback|uninstall|recover|recover-before-start) ;; *) die 'usage: install-admin.sh [install|rollback|uninstall|recover]';; esac
source_dir=$(cd -- "$(dirname -- "$0")/.." && pwd)
app=/opt/overseas-cache-v2
etc=/etc/nginx/overseas-cache
state=/var/lib/overseas-cache-admin
history=/var/lib/overseas-cache-installer
journal=$history/pending-install
in_progress=/run/overseas-cache-installer.active
recovery_dir=/usr/local/lib/overseas-cache
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
for cmd in nginx systemctl flock install runuser awk dpkg tar sha256sum; do command -v "$cmd" >/dev/null || die "missing $cmd"; done

if [[ $action == install ]]; then
    version=$(nginx -v 2>&1)
    version=${version##*/}
    version=${version%% *}
    dpkg --compare-versions "$version" ge 1.19.3 || die 'Nginx 1.19.3 or newer is required'
fi

process_start() {
    local pid=$1 stat
    local fields=()
    IFS= read -r stat < "/proc/$pid/stat" || return 1
    read -r -a fields <<< "${stat##*) }"
    [[ "${fields[0]:-}" != Z && "${fields[0]:-}" != X && "${fields[19]:-}" =~ ^[0-9]+$ ]] || return 1
    printf '%s\n' "${fields[19]}"
}
live_activation() {
    local pid started extra
    [[ -f $in_progress && ! -L $in_progress ]] || return 1
    IFS=' ' read -r pid started extra < "$in_progress" || return 1
    [[ $pid =~ ^[1-9][0-9]*$ && $started =~ ^[0-9]+$ && -z $extra ]] || return 1
    [[ $(process_start "$pid" 2>/dev/null) == "$started" ]] || return 1
    [[ /proc/$pid/fd/8 -ef $history/install.lock ]] || return 1
    # The lock belongs to the installer's open file description.
    grep -qE '^lock:[[:space:]].*FLOCK[[:space:]]+ADVISORY[[:space:]]+WRITE' "/proc/$pid/fdinfo/8" 2>/dev/null
}
allow_activation() {
    local owner=$BASHPID started temporary
    started=$(process_start "$owner") || return 1
    temporary=$(mktemp -- /run/.overseas-cache-activation.XXXXXX) || return 1
    printf '%s %s\n' "$owner" "$started" > "$temporary" || return 1
    mv -fT -- "$temporary" "$in_progress"
}
mkdir -p "$history" "$etc" "$etc/backups"
chmod 700 "$history" "$etc" "$etc/backups"
exec 8>>"$history/install.lock"
if [[ $action == recover-before-start ]]; then
    if ! flock -n 8; then
        # Starting a validated trial is the only installer-owned reentry.
        if live_activation; then exit 0; fi
        die 'service start blocked by an active installation or a retained lock'
    fi
else
    flock -w 30 8 || die 'another installer is running'
fi
rm -f -- "$in_progress"
touch "$lock" "$legacy_lock"
chmod 600 "$lock" "$legacy_lock"
exec 7<>"$legacy_lock"
exec 9<>"$lock"
if [[ $action == recover-before-start ]]; then
    flock -n 7 && flock -n 9 || die 'service start blocked by an active configuration transaction'
else
    flock -w 30 7 || die 'another V1 configuration operation is running'
    flock -w 85 9 || die 'another configuration transaction is running'
fi

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
    local unit load_state
    for unit in overseas-cache-admin.service overseas-cache-config.socket overseas-cache-config.service ws-fanout.service; do
        load_state=$(systemctl show --value -p LoadState "$unit") || return 1
        if [[ $load_state != not-found ]]; then systemctl stop "$unit" || return 1; fi
    done
}
verify_snapshot() {
    local directory=$1 expected=$2 actual
    [[ -d $directory && ! -L $directory ]] || return 1
    if [[ -n $expected ]]; then
        [[ $expected =~ ^[0-9a-f]{64}$ ]] || return 1
        actual=$(snapshot_digest "$directory") || return 1
        [[ $actual == "$expected" ]] || return 1
    elif [[ -f $directory/.sha256 ]]; then
        IFS= read -r expected < "$directory/.sha256" || return 1
        [[ -n $expected ]] || return 1
        verify_snapshot "$directory" "$expected" || return 1
    else
        # Previously shipped snapshots have no checksum. Validate the
        # essential dependencies implied by the saved service files.
        if [[ -e $directory$site || -L $directory$enabled ]]; then
            [[ -s $directory$site && -s $directory$upstream && -s $directory$etc/settings.conf ]] || return 1
        fi
        if grep -qsF "$app/bin/" "$directory/etc/systemd/system/overseas-cache-admin.service"; then
            [[ -x $directory$app/bin/overseas-admin && -x $directory$app/bin/ws-fanout &&
                -x $directory$app/bin/config-helper && -s $directory$etc/admin-v2.env &&
                -s $directory$state/admin-password.json ]] || return 1
        fi
    fi
}
snapshot_digest() (
    set -o pipefail
    cd -- "$1" || exit 1
    # Exclude the seal itself. The sorted archive covers names, file bytes,
    # links, modes, ownership and saved systemd state markers.
    find . -mindepth 1 -maxdepth 1 ! -name .sha256 -print0 |
        LC_ALL=C sort -z |
        LC_ALL=C tar --sort=name -cf - --null -T - |
        sha256sum | cut -d ' ' -f 1
)
restore() {
    local directory=$1 mode=$2 path unit status=0
    if [[ $mode == online ]]; then stop_v2 || return 1; fi
    for unit in "${units[@]}"; do systemctl disable --no-reload "$unit" >/dev/null 2>&1 || true; done
    for path in "${paths[@]}"; do
        rm -rf -- "$path" || return 1
        if [[ -e $directory$path || -L $directory$path ]]; then
            mkdir -p "$(dirname "$path")" && cp -a -- "$directory$path" "$path" || return 1
        fi
    done
    for unit in "${units[@]}"; do
        if [[ -f $directory/$unit.enabled ]]; then systemctl enable --no-reload "$unit" >/dev/null || status=1; fi
    done
    systemctl daemon-reload || return 1
    (( status == 0 )) || return 1
    nginx -t -q >/dev/null 2>&1 || { printf 'Restored Nginx configuration did not validate.\n' >&2; return 1; }
    if [[ $mode == online ]]; then allow_activation || return 1; fi
    for unit in overseas-cache-config.socket overseas-cache-config.service ws-fanout.service overseas-cache-admin.service; do
        if [[ -f $directory/$unit.active ]]; then
            systemctl reset-failed "$unit" 2>/dev/null || true
            if [[ $mode == boot ]]; then
                # The restored jobs can depend on this recovery unit.
                systemctl --no-block start "$unit" || status=1
            else
                systemctl start "$unit" || status=1
            fi
        fi
    done
    if [[ $mode == boot ]]; then
        return "$status"
    elif systemctl is-active --quiet nginx; then
        nginx_reload || status=1
    else
        systemctl start nginx || status=1
    fi
    return "$status"
}

record_pending() {
    local directory=$1 temporary digest
    digest=$(snapshot_digest "$directory") || return 1
    printf '%s\n' "$digest" > "$directory/.sha256"
    temporary=$(mktemp -- "$history/.pending.XXXXXX")
    printf '%s %s\n' "$(basename "$directory")" "$digest" > "$temporary"
    # Put the snapshot on disk before making it eligible for recovery.
    sync -f "$history"
    mv -fT -- "$temporary" "$journal"
    sync -f "$history"
}
finish_pending() {
    # Persist state across /etc, /opt and /var before removing the marker.
    sync -f "$etc" /etc/nginx/conf.d /etc/nginx/sites-available /etc/nginx/sites-enabled \
        /etc/systemd/system /etc/logrotate.d /opt /var/lib "$history" || return 1
    rm -f -- "$journal" || return 1
    sync -f "$history"
}
recover_install() {
    local mode=$1 snapshot_name digest extra
    IFS=' ' read -r snapshot_name digest extra < "$journal" || die 'cannot read pending installation snapshot'
    [[ -z $extra ]] || die 'invalid pending installation record'
    [[ $snapshot_name =~ ^(before|pre-rollback|pre-uninstall)\.[A-Za-z0-9]{6}$ ]] ||
        die 'invalid pending installation snapshot name'
    verify_snapshot "$history/$snapshot_name" "$digest" ||
        die 'pending installation snapshot is missing or damaged'
    restore "$history/$snapshot_name" "$mode" || die 'pending installation restore failed'
    finish_pending
    printf 'Recovered interrupted installation from %s.\n' "$snapshot_name"
}
recover_config() {
    local option=$1
    [[ -e $etc/backups/pending ]] || return 0
    [[ -x $app/bin/config-helper ]] || die 'recover the interrupted configuration transaction before continuing'
    flock -u 9
    "$app/bin/config-helper" "$option"
    flock -w 85 9 || die 'configuration lock is busy'
}

if [[ $action == recover-before-start ]]; then
    # A synchronous systemd stop/reload from an ordered startup gate can
    # wait on that very gate. Use explicit online recovery if a service
    # already has a process and needs to be stopped before restoration.
    for unit in nginx.service overseas-cache-admin.service ws-fanout.service overseas-cache-config.service; do
        pid=$(systemctl show --value -p MainPID "$unit") || die "cannot inspect $unit"
        if [[ -n $pid && $pid != 0 ]]; then
            die 'pending recovery blocks service start while managed processes are running; run install-admin.sh recover'
        fi
    done
    if [[ -e $journal ]]; then recover_install boot; fi
    recover_config --recover-before-nginx
    exit 0
fi

workspace=
snapshot=
rollback_install() {
    local code=$?
    trap - EXIT
    if [[ -n $workspace ]]; then rm -rf -- "$workspace" || true; fi
    if (( code != 0 )) && [[ -n $snapshot && -e $journal ]]; then
        rm -f -- "$in_progress" || true
        if verify_snapshot "$snapshot" '' && restore "$snapshot" online && finish_pending; then
            printf 'Install failed. Previous installation restored.\n' >&2
        else
            printf 'Install failed. Automatic recovery incomplete. Snapshot %s\n' "$snapshot" >&2
        fi
    fi
    rm -f -- "$in_progress" || true
    exit "$code"
}
trap rollback_install EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

install_recovery_gate() {
    local unit drop_in
    bash -n "$source_dir/scripts/install-admin.sh" || die 'invalid recovery script'
    install -d -m 0755 "$recovery_dir"
    install_atomic "$source_dir/scripts/install-admin.sh" "$recovery_dir/install-admin.sh" 0755
    systemd-analyze verify "$source_dir/systemd/v2/overseas-cache-recovery.service" ||
        die 'invalid recovery service'
    install_atomic "$source_dir/systemd/v2/overseas-cache-recovery.service" \
        /etc/systemd/system/overseas-cache-recovery.service 0644
    for unit in nginx.service overseas-cache-admin.service ws-fanout.service; do
        drop_in=/etc/systemd/system/$unit.d
        install -d -m 0755 "$drop_in"
        install_atomic "$source_dir/systemd/v2/recovery.conf" "$drop_in/overseas-cache-recovery.conf" 0644
    done
    systemctl daemon-reload
    systemctl show --value -p Requires nginx.service | grep -qw overseas-cache-recovery.service ||
        die 'Nginx recovery dependency is missing'
    systemctl show --value -p After nginx.service | grep -qw overseas-cache-recovery.service ||
        die 'Nginx recovery ordering is missing'
    sync
}

if [[ $action == install ]]; then
    for bin in overseas-admin ws-fanout config-helper; do
        [[ -x $source_dir/bin/$bin ]] || die 'run from a built V2 release archive (see docs/DEPLOYMENT.md)'
        "$source_dir/bin/$bin" --version >/dev/null || die "binary $bin cannot run on this host"
    done
    install_recovery_gate
fi
if [[ -e $journal ]]; then recover_install online; fi
rm -f -- "$in_progress"
if systemctl is-active --quiet nginx; then recover_config --recover; else recover_config --recover-before-nginx; fi
if [[ $action == recover ]]; then exit 0; fi

if [[ $action == rollback || $action == uninstall ]]; then
    original=$(realpath -e -- "$history/original") || die 'no pre-V2 installation snapshot'
    [[ $(dirname "$original") == "$history" ]] && verify_snapshot "$original" '' ||
        die 'original installation snapshot is missing or damaged'
    snapshot=$(mktemp -d -- "$history/pre-$action.XXXXXX")
    rmdir "$snapshot"
    remember "$snapshot"
    record_pending "$snapshot"
    if [[ -f $state/admin-password.json && -f $history/original/etc/systemd/system/overseas-cache-admin.service ]]; then
        modern_password=$snapshot$state/admin-password.json
    else
        modern_password=
    fi
    restore "$original" online || die "could not $action"
    if [[ -n $modern_password ]]; then
        install_atomic "$modern_password" "$etc/admin-password.json" 0600
        systemctl try-restart overseas-cache-admin.service
    fi
    finish_pending
    printf '%s complete; previous V2 snapshot: %s\n' "$action" "$snapshot"
    exit 0
fi

workspace=$(mktemp -d -- "$history/stage.XXXXXX")
snapshot=$(mktemp -d -- "$history/before.XXXXXX")
rmdir "$snapshot"
remember "$snapshot"
record_pending "$snapshot"

for account in overseas-admin overseas-fanout; do
    if ! id "$account" >/dev/null 2>&1; then
        useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$account"
    fi
done
install -d -m 0700 -o overseas-admin -g overseas-admin "$state"
# Set both managed directory modes explicitly. Some install providers
# apply this process's restrictive umask to implicit parents.
install -d -m 0755 "$app" "$app/bin" /var/log/overseas-cache
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
check_local_admin=0
if [[ ! -f $etc/admin-v2.env ]]; then
    check_local_admin=1
    networks=
    if [[ -f $etc/admin.env ]]; then
        networks=$(sed -n 's/^OVERSEAS_ADMIN_ALLOWED_NETWORKS=//p' "$etc/admin.env")
    fi
    {
        printf 'OVERSEAS_ADMIN_HOST=127.0.0.1\n'
        # Preserve the V1 management ACL and admit the new loopback listener.
        if [[ $networks =~ ^[0-9a-fA-F:./,[:space:]]+$ ]]; then
            printf 'OVERSEAS_ADMIN_ALLOWED_NETWORKS=%s,127.0.0.1/32\n' "$networks"
        fi
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
nginx -t -q >/dev/null 2>&1 || die 'Nginx validation failed before service activation'
allow_activation
systemctl reset-failed ws-fanout.service overseas-cache-admin.service 2>/dev/null || true
systemctl enable --now ws-fanout.service overseas-cache-config.socket overseas-cache-admin.service >/dev/null
nginx_reload || die 'Nginx validation or reload failed'
for service in ws-fanout.service overseas-cache-config.socket overseas-cache-admin.service; do
    systemctl is-active --quiet "$service" || die "unit failed to start: $service"
done
if command -v curl >/dev/null; then
    curl --noproxy '*' --fail --silent --show-error --retry 5 --retry-connrefused \
        --max-time 2 http://127.0.0.1:3001/healthz >/dev/null
    if (( check_local_admin )); then
        curl --noproxy '*' --fail --silent --show-error --retry 5 --retry-connrefused \
            --max-time 2 http://127.0.0.1:23000/login >/dev/null
    fi
fi
if [[ ! -e $history/original ]]; then ln -s -- "$(basename "$snapshot")" "$history/original"; fi
finish_pending
printf 'V2 installed. Management defaults to http://127.0.0.1:23000.\n'
