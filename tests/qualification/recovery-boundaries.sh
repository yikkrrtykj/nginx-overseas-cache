#!/usr/bin/env bash
# Run only on a disposable GitHub-hosted VM with real systemd.
set -Eeuo pipefail
[[ $EUID == 0 && $GITHUB_ACTIONS == true && $RUNNER_ENVIRONMENT == github-hosted ]]
out=$PWD/artifacts/recovery-boundaries
mkdir -p "$out"
exec > >(tee "$out/boundaries.log") 2>&1
root=$PWD
home=/home/qualification
etc=/etc/nginx/overseas-cache
history=/var/lib/overseas-cache-installer
journal=$history/pending-install
active=/run/overseas-cache-installer.active
gate=/usr/local/lib/overseas-cache/install-admin.sh
service=overseas-cache-recovery.service
admin=overseas-cache-admin.service
fanout=ws-fanout.service
testdir=$(mktemp -d)
hook=$testdir/hook
mkdir "$hook"
fix=$home/fixed
trial=$home/trial
boot=$(cat /proc/sys/kernel/random/boot_id)
p() { printf '\nPASS %s\n' "$*"; }
fail() { printf '\nFAIL %s\n' "$*" >&2; exit 1; }
cleanup() {
    local code=$?
    trap - EXIT
    if mountpoint -q /etc/nginx/conf.d; then umount /etc/nginx/conf.d || true; fi
    if mountpoint -q /usr/sbin/nginx; then umount /usr/sbin/nginx || true; fi
    rm -f /etc/systemd/system/overseas-cache-recovery.service.d/qualification.conf
    journalctl -b -u "$service" -u nginx -u "$admin" -u "$fanout" --no-pager -n 130 > "$out/systemd.log" || true
    exit "$code"
}
trap cleanup EXIT
trap 'printf "Boundary failure at line %s\n" "$LINENO" >&2' ERR

git rev-parse HEAD 'HEAD^{tree}'
( cd dist && sha256sum -c SHA256SUMS )
( cd prior-dist && sha256sum -c SHA256SUMS )
arch=$(dpkg --print-architecture)
mkdir -p "$home" "$fix"
git archive 0e2bb77bdacc4e1f825858a0d59e2070828dd1b7 > "$home/v1.tar"
install -m 0644 tests/origin_fixture.py "$home/origin.py"
install -m 0644 "$(find prior-dist -maxdepth 1 -name "*-linux-$arch.tar.gz")" "$home/v2.tar.gz"
tar -xzf "$(find dist -maxdepth 1 -name "*-linux-$arch.tar.gz")" --strip-components=1 -C "$fix"
for bin in overseas-admin ws-fanout config-helper; do
    [[ $("$fix/bin/$bin" --version 2>&1) == "$(git rev-parse --short=12 HEAD)" ]]
    "$fix/bin/$bin" --version
done
cp -a "$fix" "$trial"
sed -i '0,/^server {/s|^server {|server {\n    location = /__qualification_trial { return 200 "qualification-uncommitted"; }|' "$trial/nginx/sites-available/overseas-cache"

cat > "$hook/dispatch" <<'HOOK'
#!/usr/bin/env bash
set -eu
command=$(basename "$0")
real=/usr/bin/$command
journal=/var/lib/overseas-cache-installer/pending-install
path=${!#}
mode=${QUAL_MODE:-}
matched=false
case $mode:$command in
    pending:mv|hold:mv)
        [[ $path != "$journal" ]] || matched=true ;;
    files:systemctl|reload:systemctl|systemd-failure:systemctl)
        [[ $1 != daemon-reload || ! -e $journal ]] || matched=true ;;
    start:systemctl)
        [[ $1 != enable || ${2:-} != --now || ${3:-} != ws-fanout.service ]] || matched=true ;;
    copy:cp)
        [[ $path != /etc/nginx/conf.d/proxy-cache.conf || ! -e $journal ]] || matched=true ;;
    gate:install)
        # Interrupt after the entrypoint is copied and before the service
        # temporary file can replace the installed gate.
        [[ $* != *'/systemd/v2/overseas-cache-recovery.service '* ]] || matched=true ;;
esac
if [[ $matched == false ]]; then exec "$real" "$@"; fi
case $mode in
    hold)
        "$real" "$@"
        touch "$QUAL_SYNC/waiting"
        while [[ ! -e $QUAL_SYNC/resume ]]; do sleep 0.05; done
        exit ;;
    pending|reload|start|copy)
        "$real" "$@" ;;
    systemd-failure)
        echo 'Injected systemd daemon-reload failure' >&2
        exit 1 ;;
esac
sync
kill -KILL "$PPID"
exit 137
HOOK
chmod 0755 "$hook/dispatch"
for cmd in mv cp install systemctl; do ln -s dispatch "$hook/$cmd"; done
op() { timeout -k 5 60 bash "$fix/scripts/install-admin.sh" "$@"; }
crash() {
    local mode=$1 source=$2 action=$3 result=0
    timeout -k 5 65 env "PATH=$hook:$PATH" "QUAL_MODE=$mode" \
        bash "$source/scripts/install-admin.sh" "$action" || result=$?
    [[ $result == 137 ]] || fail "$mode/$action returned $result, expected SIGKILL"
}
positive_v1() {
    [[ ! -e $journal ]] || fail 'restoration did not commit'
    systemctl is-active --quiet nginx
    systemctl is-active --quiet "$admin"
    systemctl is-active --quiet "$fanout"
    systemctl show -p ExecStart "$admin" | grep -Fq nginx-overseas-cache-admin/server.py
    nginx -t -q
}
positive_v2() {
    [[ ! -e $journal ]] || fail 'V2 journal remains'
    for unit in nginx "$admin" "$fanout" overseas-cache-config.socket; do systemctl is-active --quiet "$unit"; done
    curl --noproxy '*' -fsS --retry 4 --retry-connrefused http://127.0.0.1:3001/healthz >/dev/null
}
no_trial() {
    local response
    response=$(curl --noproxy '*' -fsS --max-time 2 http://127.0.0.1:23333/__qualification_trial 2>/dev/null || true)
    if [[ $response == qualification-uncommitted ]]; then fail 'Nginx exposed unconfirmed configuration'; fi
}
reject() {
    local code=0
    timeout -k 3 16 "$@" || code=$?
    [[ $code != 0 && $code != 124 && $code != 137 ]] || fail "expected bounded rejection: $* (status $code)"
}
start_v1() {
    systemctl reset-failed "$service" nginx "$admin" "$fanout" 2>/dev/null || true
    systemctl start nginx "$admin" "$fanout"
    positive_v1
}
recover_v1() { op recover; start_v1; no_trial; }
prepare_v1() {
    bash "$root/tests/qualification/boot-guest.sh" provision
    op install
    positive_v2
    p 'upgrade original Draft V2 to exact fixed package'
    op install
    positive_v2
    p 'fixed package reinstall'
    op rollback
    positive_v1
}

# Interrupt installation of the gate on a pristine host before any package
# runtime files or journal exist. Use the old package only to make V1.
apt-get update -q
DEBIAN_FRONTEND=noninteractive apt-get install -y nginx python3-venv
crash gate "$fix" install
[[ ! -e $journal && ! -e /opt/overseas-cache-v2 ]]
systemctl restart nginx
p 'fresh gate bootstrap interruption left nginx usable'
prepare_v1

for phase in pending files reload start; do
    crash "$phase" "$trial" install
    [[ -s $journal ]]
    if [[ $phase == start ]]; then
        systemctl is-active --quiet "$admin"
        [[ -s $active ]] || fail 'trial activation permit missing'
    else
        printf '99999999 1\n' > "$active"
    fi
    flock -n "$history/install.lock" true
    systemctl daemon-reload
    code=0
    timeout -k 3 16 systemctl restart nginx || code=$?
    [[ $code != 124 && $code != 137 ]] || fail 'startup gate deadlocked'
    no_trial
    if [[ -e $journal ]]; then
        if [[ $phase == start ]]; then systemctl stop "$admin" "$fanout"; fi
        # With nginx unavailable, an immediate worker start can instead
        # complete restoration. During any blocked phase the trial must
        # never become a newly started runtime.
        code=0
        timeout -k 3 16 systemctl start "$admin" "$fanout" || code=$?
        [[ $code != 124 && $code != 137 ]]
        if [[ -e $journal ]]; then
            reject systemctl start "$service"
            recover_v1
        fi
    fi
    start_v1
    p "$phase same-boot crash and start ordering"
done

# A real installer holding the journal and its lock has not reached the
# activation phase. A concurrent gate is refused without waiting on it.
timeout -k 5 85 env "PATH=$hook:$PATH" QUAL_MODE=hold "QUAL_SYNC=$testdir" \
    bash "$fix/scripts/install-admin.sh" install &
installer=$!
for _ in {1..150}; do
    [[ ! -e $testdir/waiting ]] || break
    sleep 0.05
done
[[ -e $testdir/waiting && -s $journal ]]
reject systemctl start "$service"
touch "$testdir/resume"
wait "$installer"
positive_v2
p 'concurrent live installer blocked during staging, then activated without deadlock'

# A corrupt gate package must not replace a valid unit or open a runtime journal.
cp -a "$fix" "$testdir/bad-gate"
printf '[Service]\nType=oneshot\n' > "$testdir/bad-gate/systemd/v2/overseas-cache-recovery.service"
before=$(sha256sum /etc/systemd/system/overseas-cache-recovery.service)
reject bash "$testdir/bad-gate/scripts/install-admin.sh"
[[ $(sha256sum /etc/systemd/system/overseas-cache-recovery.service) == "$before" ]]
positive_v2
p 'invalid recovery unit upgrade preserved existing gate and deployment'

for action in rollback uninstall; do
    crash copy "$fix" "$action"
    [[ -s $journal ]]
    crash copy "$fix" recover
    [[ -s $journal ]]
    op recover
    positive_v2
    p "interrupted $action and interrupted online recovery replayed idempotently"
done

op rollback
positive_v1
crash files "$trial" install
systemctl daemon-reload
cp -a "$gate" "$testdir/entrypoint"
for fault in missing permissions syntax; do
    case $fault in
        missing) rm "$gate" ;;
        permissions) chmod 0600 "$gate" ;;
        syntax) printf '#!/usr/bin/env bash\n(\n' > "$gate" ;;
    esac
    systemctl reset-failed "$service" nginx "$admin" "$fanout" 2>/dev/null || true
    reject systemctl restart nginx
    [[ -s $journal ]]
    no_trial
    install -m 0755 "$testdir/entrypoint" "$gate"
    p "$fault recovery entrypoint blocked managed startup"
done
recover_v1

crash pending "$trial" install
# A repeated install performs recovery before beginning its own transaction.
op install
positive_v2
p 'same-boot installer retry recovered the pending transaction'
op rollback
positive_v1
crash files "$trial" install
read -r checkpoint _ < "$journal"
mv "$history/$checkpoint" "$testdir/snapshot"
reject bash "$fix/scripts/install-admin.sh" recover
[[ -s $journal ]]
cp -a "$testdir/snapshot" "$history/$checkpoint"
printf 'damaged\n' >> "$history/$checkpoint/etc/nginx/overseas-cache/settings.conf"
reject bash "$fix/scripts/install-admin.sh" recover
[[ -s $journal ]]
rm -rf "$history/$checkpoint"
mv "$testdir/snapshot" "$history/$checkpoint"
p 'missing and damaged snapshot preserved the journal'

mount -t tmpfs -o size=64k tmpfs /etc/nginx/conf.d
dd if=/dev/zero of=/etc/nginx/conf.d/.padding bs=1024 count=64 status=none
code=0
timeout -k 3 20 bash "$fix/scripts/install-admin.sh" recover > "$out/disk.log" 2>&1 || code=$?
[[ $code == 1 && -s $journal ]]
grep -Fq 'No space left on device' "$out/disk.log"
umount /etc/nginx/conf.d
p 'actual ENOSPC preserved pending recovery'
printf 'invalid_qualification_directive;\n' > /etc/nginx/conf.d/qualification-external.conf
reject bash "$fix/scripts/install-admin.sh" recover
[[ -s $journal ]]
rm /etc/nginx/conf.d/qualification-external.conf
p 'Nginx validation failure preserved pending recovery'
reject env "PATH=$hook:$PATH" QUAL_MODE=systemd-failure bash "$fix/scripts/install-admin.sh" recover
[[ -s $journal ]]
p 'systemd restore failure preserved pending recovery'
recover_v1

op install
positive_v2
case_id=admin-20261128T070000Z-qualification
backup=$etc/backups/$case_id
mkdir -m 0700 "$backup"
install -m 0600 "$etc/settings.conf" "$backup/settings.conf"
install -m 0600 /etc/nginx/conf.d/proxy-cache.conf "$backup/proxy-cache.conf"
printf '%s' "$case_id" > "$etc/backups/pending"
sed -i 's/qualification-boot/qualification-uncommitted/' "$etc/settings.conf"
cat > "$testdir/intercept-nginx" <<'CONFIG'
#!/usr/bin/env bash
printf 'Interrupted config recovery during validation\n' >&2
kill -KILL "$PPID"
exit 137
CONFIG
chmod 0755 "$testdir/intercept-nginx"
mount --bind "$testdir/intercept-nginx" /usr/sbin/nginx
code=0
/opt/overseas-cache-v2/bin/config-helper --recover || code=$?
[[ $code == 137 && -s $etc/backups/pending ]]
umount /usr/sbin/nginx
op recover
[[ ! -e $etc/backups/pending ]]
grep -Fq 'qualification-boot' "$etc/settings.conf"
positive_v2
p 'interrupted configuration recovery replayed idempotently'

op uninstall
positive_v1
for component in "$gate" "/etc/systemd/system/$service" /etc/systemd/system/{nginx,overseas-cache-admin,ws-fanout}.service.d/overseas-cache-recovery.conf; do
    [[ -f $component ]]
done
[[ $(cat /proc/sys/kernel/random/boot_id) == "$boot" ]]
p 'uninstall retained inert recovery components and V1 in the same boot'
