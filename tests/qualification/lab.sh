#!/usr/bin/env bash
set -Eeuo pipefail
[[ $EUID == 0 && $GITHUB_ACTIONS == true && $RUNNER_ENVIRONMENT == github-hosted ]]
root=$PWD
ev=$root/artifacts/qualification
mkdir -p "$ev"
tmp=$(mktemp -d)
export no_proxy='*' NO_PROXY='*'
config=/etc/nginx/overseas-cache
cache=/var/cache/nginx/web_cache
state=/var/lib/overseas-cache-admin
history=/var/lib/overseas-cache-installer
test_password=$(openssl rand -hex 20)
test_token=$(openssl rand -hex 20)
# Register the masks before the public evidence log starts.
printf '::add-mask::%s\n::add-mask::%s\n' "$test_password" "$test_token"
printf '::add-mask::%s_rotated\n' "$test_password"
exec > >(tee "$ev/lab.log") 2>&1
date -u --iso-8601=seconds
cat /etc/os-release
uname -a
systemctl --version | head -n 1
printf 'workflow %s\n' "$(git rev-parse HEAD)"
git rev-parse 01746cc861e1151fd8530ef0e8adb85fb5892525^{tree} | tee "$ev/tree.txt"
grep -qx fda8ff30da6d79a2fe6c629a4c641cc507ed905f "$ev/tree.txt"
[[ $(dpkg --print-architecture) == arm64 ]]
( cd dist; sha256sum -c SHA256SUMS ) | tee "$ev/archive-checksums.txt"
arch=$(dpkg --print-architecture)
archive=$root/dist/nginx-overseas-cache-01746cc861e1-linux-$arch.tar.gz
tar -xzf "$archive" -C "$tmp"
package=$(find "$tmp" -mindepth 1 -maxdepth 1 -type d -name 'nginx-overseas-cache-*')
find "$package/licenses" -type f -printf '%f\n' | sort | tee "$ev/licenses.txt"
for binary in overseas-admin ws-fanout config-helper; do
    "$package/bin/$binary" --version 2>&1 | tee -a "$ev/build-version.txt"
done
[[ $(grep -c '01746cc861e1' "$ev/build-version.txt") == 3 ]]

ns_ip() { ip netns exec "$@"; }
capture() {
    local label=$1
    {
        for unit in nginx.service ws-fanout.service overseas-cache-admin.service overseas-cache-config.socket overseas-cache-config.service; do
            systemctl show "$unit" -p Id -p ActiveState -p SubState -p UnitFileState -p MainPID -p User -p FragmentPath
        done
        ss -lntp '( sport = :23000 or sport = :23333 or sport = :3001 or sport = :24222 )'
        stat -c '%a %U:%G %i %n' "$config" "$config"/*.conf "$config"/*.env "$config"/*password.json "$cache"
        if [[ -f $state/admin-password.json ]]; then stat -c '%a %U:%G %i %n' "$state/admin-password.json"; fi
        nginx -t
    } > "$ev/services-$label.txt" 2>&1
}
cleanup() {
    local rc=$?
    trap - EXIT
    for job in $(jobs -pr); do kill "$job" 2>/dev/null || true; done
    capture end || true
    journalctl -u nginx -u ws-fanout -u overseas-cache-admin -u overseas-cache-config -u qual-origin -u qual-sshd --no-pager -o short-iso \
        | sed -e "s/$test_token/[REDACTED]/g" -e "s/$test_password/[REDACTED]/g" -E -e 's/lab-(ws|timing)-[0-9a-f]{32}/[REDACTED-WS]/g' > "$ev/journal.log"
    for net in qual-mgmt qual-biz qual-deny; do ip netns delete "$net" 2>/dev/null || true; done
    status=$rc
    if [[ -e $ev/leak-detected ]]; then status=1; fi
    exit "$status"
}
trap cleanup EXIT
trap 'echo "Failure near lab.sh:$LINENO"' ERR
mkdir -p "$tmp/v1" "$tmp/tls"
git archive 0e2bb77bdacc4e1f825858a0d59e2070828dd1b7 | tar -x -C "$tmp/v1"

# Each path has its own VLAN-tagged point-to-point L2 link, with no gateway out
# of the namespaces. The underlay interfaces have no addresses.
vlan_path() {
    local role=$1 vlan=$2 third=$3
    ip netns add "qual-$role"
    ip link add "lh-$role" type veth peer name "ln-$role"
    ip link set "ln-$role" netns "qual-$role"
    ip link set "lh-$role" up
    ip link add link "lh-$role" name "lv-$role" type vlan id "$vlan"
    ip addr add "10.84.$third.1/24" dev "lv-$role"
    ip link set "lv-$role" up
    ns_ip "qual-$role" ip link set lo up
    ns_ip "qual-$role" ip link set "ln-$role" up
    ns_ip "qual-$role" ip link add link "ln-$role" name client type vlan id "$vlan"
    ns_ip "qual-$role" ip addr add "10.84.$third.2/24" dev client
    ns_ip "qual-$role" ip link set client up
    ns_ip "qual-$role" ip route add 10.84.0.0/16 via "10.84.$third.1"
}
vlan_path mgmt 71 71
vlan_path biz 72 72
vlan_path deny 73 73
{
    ip -d -br link
    ip -br addr
    for net in qual-mgmt qual-biz qual-deny; do ns_ip "$net" ip -d addr; ns_ip "$net" ip route; done
} > "$ev/topology.txt"
ip addr add 93.184.215.254/32 dev lo
printf '93.184.215.254 webcoder.atum.live webcodercdn.atum.live\n' >> /etc/hosts
for domain in webcoder.atum.live webcodercdn.atum.live; do
    [[ $(getent ahostsv4 "$domain" | awk '{print $1}' | sort -u) == 93.184.215.254 ]]
done
openssl req -x509 -nodes -newkey rsa:2048 -days 2 -subj '/CN=Synthetic Origin' \
    -addext 'subjectAltName=DNS:webcoder.atum.live,DNS:webcodercdn.atum.live' \
    -keyout "$tmp/tls/origin.key" -out "$tmp/tls/origin.crt" >/dev/null 2>&1
install -m 0644 "$tmp/tls/origin.crt" /usr/local/share/ca-certificates/qualification-origin.crt
update-ca-certificates
python3 -m venv "$tmp/python"
py=$tmp/python/bin/python
"$py" -m pip install -r "$tmp/v1/ws-fanout/requirements.txt"
systemd-run --unit qual-origin "$py" "$root/tests/qualification/origin.py" "$tmp/tls/origin.crt" "$tmp/tls/origin.key"
systemctl enable --now nginx
install -d -m 0750 -o root -g www-data /var/cache/nginx
install -d -m 0750 -o www-data -g www-data "$cache"
mkdir -p "$config"
printf 'OVERSEAS_ADMIN_ALLOWED_NETWORKS=10.84.71.0/24\n' > "$config/admin.env"
printf '%s' "$test_password" | python3 "$tmp/v1/admin/server.py" --init-password-file "$config/admin-password.json"
bash "$tmp/v1/scripts/install-admin.sh"
sed -i -e 's/pgs26_p/qualification/' -e "s/REPLACE_WITH_REAL_TOKEN/$test_token/" "$config/settings.conf"
grep -qF "$test_token" "$config/settings.conf"
cp "$config/settings.conf" "$tmp/v1-settings"
cp "$config/admin-password.json" "$tmp/v1-password"
cp "$config/admin.env" "$tmp/v1-admin-env"
systemctl reload nginx

c() { curl --noproxy '*' -sS --max-time 4 "$@"; }
login() {
    local env=$1 base=$2 cookie=$3
    shift 3
    local args=()
    if [[ $env != host ]]; then args=(ip netns exec "qual-$env"); fi
    printf '{"username":"admin","password":"%s"}' "$test_password" |
        "${args[@]}" curl --noproxy '*' -fsS --retry 10 --retry-connrefused --retry-delay 1 --max-time 5 \
        -H "Origin: $base" -H 'Content-Type: application/json' --data @- -c "$cookie" -D "$tmp/login-headers" \
        "$@" "$base/api/login" | python3 -c 'import json,sys; assert json.load(sys.stdin)["ok"]'
}
http_code() {
    local role=$1 uri=$2
    shift 2
    ns_ip "qual-$role" curl --noproxy '*' -s --max-time 3 -o /dev/null -w '%{http_code}' "$@" "$uri" || true
}
data=http://10.84.72.1:23333
static=$data/templates/qualification.woff2
cache_status() {
    ns_ip qual-biz curl --noproxy '*' -fsS -D - -o /dev/null "$static" |
        awk 'tolower($1)=="x-cache-status:" {gsub("\r","",$2); print $2}'
}
dynamic() {
    local first second
    first=$(ns_ip qual-biz curl --noproxy '*' -fsS "$data/render")
    second=$(ns_ip qual-biz curl --noproxy '*' -fsS "$data/render")
    [[ $first == "qualified HTTPS sample "* && $second == "qualified HTTPS sample "* && $first != "$second" ]]
}
phase() { printf '%s,%s\n' "$1" "$(python3 -c 'import time; print(time.monotonic_ns())')" | tee -a "$ev/phases.csv"; }
browser() {
    QUAL_PASSWORD="$test_password" node tests/qualification/browser.cjs "$1" "$2" "$ev" "${@:3}"
}
login mgmt http://10.84.71.1:23000 "$tmp/cookies"
[[ $(http_code deny http://10.84.71.1:23000/) == 403 ]]
[[ $(http_code biz http://10.84.71.1:23000/) == 403 ]]
[[ $(http_code biz "$data/render") == 200 ]]
[[ $(cache_status) == MISS && $(cache_status) == HIT ]]
dynamic
"$py" tests/qualification/probe.py verify
find "$cache" -type f -printf '%P %i\n' | sort > "$ev/v1-cache-inodes.txt"
[[ -s $ev/v1-cache-inodes.txt ]]
capture v1
ns_ip qual-mgmt env QUAL_PASSWORD="$test_password" PLAYWRIGHT_BROWSERS_PATH="$PLAYWRIGHT_BROWSERS_PATH" \
    node tests/qualification/browser.cjs v1 http://10.84.71.1:23000 "$ev"
echo 'PASS V1 ACL management login, business/unapproved deny, actual cache, distinct dynamic responses, shared WS'

installer() { bash "$package/scripts/install-admin.sh" "$@"; }
begin_samples() {
    "$py" tests/qualification/probe.py http "$ev/$1-http.csv" &
    http_pid=$!
    "$py" tests/qualification/probe.py ws "$ev/$1-ws.csv" &
    ws_pid=$!
    sleep 1.5
}
end_samples() {
    sleep 1.5
    kill "$http_pid" "$ws_pid"
    wait "$http_pid" "$ws_pid" 2>/dev/null || true
}
begin_samples upgrade
phase upgrade-start
installer
phase upgrade-complete
end_samples
capture v2-local
cmp "$tmp/v1-settings" "$config/settings.conf"
cmp "$tmp/v1-password" "$state/admin-password.json"
grep -q '^OVERSEAS_ADMIN_ALLOWED_NETWORKS=10.84.71.0/24' "$config/admin-v2.env"
grep -q '^OVERSEAS_ADMIN_HOST=127.0.0.1$' "$config/admin-v2.env"
login host http://127.0.0.1:23000 "$tmp/cookies"
[[ $(http_code mgmt http://10.84.71.1:23000/) == 000 ]]
[[ $(http_code deny http://10.84.71.1:23000/) == 000 ]]
[[ $(http_code biz "$data/render") == 200 ]]
find "$cache" -type f -printf '%P %i\n' | sort > "$ev/v2-cache-inodes.txt"
[[ $(comm -23 "$ev/v1-cache-inodes.txt" "$ev/v2-cache-inodes.txt" | wc -l) == 0 ]]
[[ $(cache_status) == MISS && $(cache_status) == HIT ]]
dynamic
"$py" tests/qualification/probe.py verify
cp "$config/admin-v2.env" "$tmp/loopback-env"
echo 'PASS V2 default ACL migration, loopback login, no direct listener, V1 cache inode preservation and V2 cache MISS/HIT'

# SSH is available only on the test management VLAN and only with the generated public key.
mkdir -p /run/sshd
ssh-keygen -q -N '' -t ed25519 -f "$tmp/ssh-host"
ssh-keygen -q -N '' -t ed25519 -f "$tmp/ssh-client"
install -m 0644 "$tmp/ssh-client.pub" /etc/qualification-ssh-authorized-key
[ -n "$SUDO_USER" ]
cat > "$tmp/sshd.conf" <<EOF
Port 24222
ListenAddress 10.84.71.1
HostKey $tmp/ssh-host
PidFile $tmp/sshd.pid
AuthorizedKeysFile /etc/qualification-ssh-authorized-key
AllowUsers $SUDO_USER
UsePAM yes
PermitRootLogin no
PubkeyAuthentication yes
PasswordAuthentication no
AllowTcpForwarding local
PermitOpen 127.0.0.1:23000
EOF
printf '[10.84.71.1]:24222 %s\n' "$(cat "$tmp/ssh-host.pub")" > "$tmp/known_hosts"
systemd-run --unit qual-sshd /usr/sbin/sshd -D -e -f "$tmp/sshd.conf"
sleep 0.8
ns_ip qual-mgmt ssh -n -f -N -p 24222 -i "$tmp/ssh-client" -o BatchMode=yes -o ExitOnForwardFailure=yes \
    -o UserKnownHostsFile="$tmp/known_hosts" -o StrictHostKeyChecking=yes \
    -L 127.0.0.1:24300:127.0.0.1:23000 "$SUDO_USER@10.84.71.1"
login mgmt http://127.0.0.1:24300 "$tmp/cookies"
echo 'PASS real SSH forwarding and authenticated HTTP from isolated management client'

# Root -> intermediate -> leaf. Trust only the lab root, serve the full chain.
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj '/CN=Qualification Root' \
    -keyout "$tmp/tls/root.key" -out "$tmp/tls/root.crt" -addext 'basicConstraints=critical,CA:true' >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=Qualification Intermediate' \
    -keyout "$tmp/tls/intermediate.key" -out "$tmp/tls/intermediate.csr" >/dev/null 2>&1
printf 'basicConstraints=critical,CA:true,pathlen:0\nkeyUsage=critical,keyCertSign,cRLSign\n' > "$tmp/tls/ca.ext"
openssl x509 -req -in "$tmp/tls/intermediate.csr" -CA "$tmp/tls/root.crt" -CAkey "$tmp/tls/root.key" \
    -CAcreateserial -days 2 -extfile "$tmp/tls/ca.ext" -out "$tmp/tls/intermediate.crt"
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=admin.qualification.invalid' \
    -keyout "$tmp/tls/admin.key" -out "$tmp/tls/admin.csr" >/dev/null 2>&1
printf 'basicConstraints=critical,CA:false\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:admin.qualification.invalid\n' > "$tmp/tls/leaf.ext"
openssl x509 -req -in "$tmp/tls/admin.csr" -CA "$tmp/tls/intermediate.crt" -CAkey "$tmp/tls/intermediate.key" \
    -CAcreateserial -days 2 -extfile "$tmp/tls/leaf.ext" -out "$tmp/tls/admin.crt"
install -d -m 0755 /etc/qualification-tls
cat "$tmp/tls/admin.crt" "$tmp/tls/intermediate.crt" > /etc/qualification-tls/fullchain.pem
install -m 0640 -o root -g overseas-admin "$tmp/tls/admin.key" /etc/qualification-tls/admin.key
cat > "$config/admin-v2.env" <<EOF
OVERSEAS_ADMIN_HOST=10.84.71.1
OVERSEAS_ADMIN_ALLOWED_NETWORKS=10.84.71.0/24
OVERSEAS_ADMIN_ALLOWED_HOSTS=admin.qualification.invalid
OVERSEAS_ADMIN_TLS_CERT=/etc/qualification-tls/fullchain.pem
OVERSEAS_ADMIN_TLS_KEY=/etc/qualification-tls/admin.key
EOF
cp "$config/admin-v2.env" "$tmp/https-env"
systemctl restart overseas-cache-admin
secure=https://admin.qualification.invalid:23000
tls=(--cacert "$tmp/tls/root.crt" --resolve admin.qualification.invalid:23000:10.84.71.1)
login mgmt "$secure" "$tmp/cookies" "${tls[@]}"
sed -nE 's/^(Set-Cookie: [^=]+)=[^;]+/\1=[REDACTED]/Ip' "$tmp/login-headers" > "$ev/https-cookie.txt"
grep -iq 'HttpOnly' "$ev/https-cookie.txt"
grep -iq 'Secure' "$ev/https-cookie.txt"
grep -iq 'SameSite=Strict' "$ev/https-cookie.txt"
ns_ip qual-mgmt openssl s_client -connect 10.84.71.1:23000 -servername admin.qualification.invalid \
    -verify_hostname admin.qualification.invalid -verify_return_error -CAfile "$tmp/tls/root.crt" \
    </dev/null > "$ev/https-chain.txt" 2>&1
[[ $(http_code deny "$secure/" "${tls[@]}") == 403 ]]
[[ $(http_code biz "$secure/" "${tls[@]}") == 403 ]]
[[ $(http_code mgmt "$secure/" --resolve admin.qualification.invalid:23000:10.84.71.1) == 000 ]]
[[ $(http_code mgmt https://invalid.qualification.invalid:23000/ --cacert "$tmp/tls/root.crt" \
    --resolve invalid.qualification.invalid:23000:10.84.71.1) == 000 ]]
[[ $(http_code mgmt http://10.84.71.1:23000/) == 400 ]]
[[ $(http_code biz "$data/render") == 200 ]]
capture v2-https
echo 'PASS explicit management TLS, chain, hostname, secure cookie and denied networks with data plane healthy'

# Invalid TLS material is rejected; confirm no plaintext endpoint appears.
mv /etc/qualification-tls/admin.key /etc/qualification-tls/admin.key.good
systemctl restart overseas-cache-admin || true
sleep 1
[[ $(http_code mgmt "$secure/" "${tls[@]}") == 000 ]]
[[ $(http_code mgmt http://10.84.71.1:23000/) == 000 ]]
mv /etc/qualification-tls/admin.key.good /etc/qualification-tls/admin.key
systemctl reset-failed overseas-cache-admin
chmod 0600 /etc/qualification-tls/admin.key
systemctl restart overseas-cache-admin || true
sleep 1
[[ $(http_code mgmt "$secure/" "${tls[@]}") == 000 ]]
[[ $(http_code mgmt http://10.84.71.1:23000/) == 000 ]]
chmod 0640 /etc/qualification-tls/admin.key
systemctl reset-failed overseas-cache-admin
systemctl restart overseas-cache-admin ws-fanout nginx
login mgmt "$secure" "$tmp/cookies" "${tls[@]}"
"$py" tests/qualification/probe.py verify
echo 'PASS invalid TLS and key permission failed closed; restart and authenticated recovery'
cp "$tmp/loopback-env" "$config/admin-v2.env"
systemctl restart overseas-cache-admin
login host http://127.0.0.1:23000 "$tmp/cookies"
browser v2 http://127.0.0.1:23000
cp "$tmp/v1-settings" "$config/settings.conf"
systemctl reload nginx

# Apply via installed, socket-activated helper. Then make it unavailable without
# altering Admin, verify the failed write leaves protected settings intact.
apply() {
    local channel=$1 expected=$2
    local base=http://127.0.0.1:23000 nonce code
    nonce=$(c -b "$tmp/cookies" "$base/api/v2/config" | python3 -c 'import json,sys; print(json.load(sys.stdin)["csrfToken"])')
    code=$(c --max-time 85 -o "$tmp/apply-response" -w '%{http_code}' -b "$tmp/cookies" -H "Origin: $base" \
        -H "X-CSRF-Token: $nonce" -H 'Content-Type: application/json' \
        --data "{\"originHost\":\"webcoder.atum.live\",\"cdnHost\":\"webcodercdn.atum.live\",\"channelId\":\"$channel\",\"accountId\":\"render\",\"token\":\"$test_token\"}" \
        "$base/api/v2/config")
    printf 'configuration apply %s %s\n' "$channel" "$code"
    if [[ $expected == yes ]]; then
        [[ $code == 200 ]]
        python3 -c 'import json,sys; r=json.load(sys.stdin); assert r["ok"] and r["backup"] and r["config"]["tokenPresent"] and "token" not in r["config"]' < "$tmp/apply-response"
    else
        [[ $code == 400 || $code == 503 ]]
        python3 -c 'import json,sys; r=json.load(sys.stdin); assert r["ok"] is False and r["message"] == "配置服务不可用"' < "$tmp/apply-response"
    fi
}
systemctl stop overseas-cache-config.socket overseas-cache-config.service
apply failed-helper no
cmp "$tmp/v1-settings" "$config/settings.conf"
systemctl start overseas-cache-config.socket
apply helper-recovered yes
grep -q helper-recovered "$config/settings.conf"
cp "$tmp/v1-settings" "$config/settings.conf"
systemctl reload nginx
echo 'PASS unavailable config Helper leaves stored config intact, actual successful apply after recovery'

cp -a "$package" "$tmp/invalid"
printf 'invalid_qualification_directive;\n' >> "$tmp/invalid/nginx/sites-available/overseas-cache"
if bash "$tmp/invalid/scripts/install-admin.sh"; then echo 'unexpected nginx -t success'; exit 1; fi
nginx -t
login host http://127.0.0.1:23000 "$tmp/cookies"
cmp "$tmp/v1-settings" "$config/settings.conf"
echo 'PASS nginx config validation failure restored prior config and management'
begin_samples rollback
phase rollback-start
installer rollback
phase rollback-complete
end_samples
capture v1-rollback
cmp "$tmp/v1-settings" "$config/settings.conf"
cmp "$tmp/v1-password" "$config/admin-password.json"
cmp "$tmp/v1-admin-env" "$config/admin.env"
login mgmt http://10.84.71.1:23000 "$tmp/cookies"
[[ $(http_code deny http://10.84.71.1:23000/) == 403 ]]
[[ $(cache_status) == HIT ]]
dynamic
"$py" tests/qualification/probe.py verify
installer
installer
login host http://127.0.0.1:23000 "$tmp/cookies"
cmp "$tmp/v1-settings" "$config/settings.conf"
cmp "$tmp/v1-password" "$state/admin-password.json"
[[ $(cache_status) == HIT ]]
"$py" tests/qualification/probe.py verify
capture v2-repeat
echo 'PASS rollback to V1 credentials, ACL, cache, and WS; second upgrade and repeat install'
"$py" tests/qualification/timings.py "$ev"


healthy() {
    nginx -t -q
    local unit
    for unit in nginx ws-fanout overseas-cache-admin overseas-cache-config.socket; do systemctl is-active --quiet "$unit"; done
    login host http://127.0.0.1:23000 "$tmp/cookies"
    cmp "$tmp/v1-settings" "$config/settings.conf"
    cmp "$tmp/v1-password" "$state/admin-password.json"
    [[ $(http_code biz "$data/render") == 200 ]]
}
install_fails() {
    local tag=$1 candidate=$2
    local code=0
    bash "$candidate/scripts/install-admin.sh" > "$ev/fault-$tag.log" 2>&1 || code=$?
    printf '%s exit %s\n' "$tag" "$code"
    [[ $code != 0 ]]
    grep -q 'Previous installation restored' "$ev/fault-$tag.log"
    healthy
    echo "PASS prior config and services after $tag"
}

# Fail one actual Nginx reload. The next one is the installer's restoration.
drop=/run/systemd/system/nginx.service.d/qualification.conf
mkdir -p "$(dirname "$drop")"
cat > "$tmp/reload-once" <<EOF
#!/bin/bash
if [[ ! -e $tmp/injected-reload ]]; then
    touch $tmp/injected-reload
    exit 1
fi
exec /usr/sbin/nginx -s reload
EOF
printf '[Service]\nExecReload=\nExecReload=/bin/bash %s\n' "$tmp/reload-once" > "$drop"
systemctl daemon-reload
install_fails nginx-reload "$package"
[[ -e $tmp/injected-reload ]]
rm "$drop"
systemctl daemon-reload
for service in ws-fanout overseas-cache-admin; do
    copy="$tmp/fail-$service"
    cp -a "$package" "$copy"
    sed -i -e 's/^Type=simple$/Type=oneshot/' -e 's/^Restart=.*$/Restart=no/' \
        -e 's@^ExecStart=.*$@ExecStart=/bin/false@' "$copy/systemd/v2/$service.service"
    install_fails "$service-start" "$copy"
done

# Fill only an isolated 64 KiB filesystem mapped over the installer state, then
# prove the actual candidate command stops before modifying live files.
mount -t tmpfs -o size=64k,mode=0700 tmpfs "$history"
dd if=/dev/zero of="$history/fill" bs=1024 count=64 status=none
code=0
installer > "$ev/fault-enospc.log" 2>&1 || code=$?
umount "$history"
[[ $code != 0 ]]
grep -q 'No space left on device' "$ev/fault-enospc.log"
healthy
echo 'PASS ENOSPC in installer staging did not alter confirmed runtime'
# Exercise migration from the original main V2 release on this same arm64 VM.
installer rollback
systemctl show -p ExecStart overseas-cache-admin | grep -Fq nginx-overseas-cache-admin/server.py
mkdir "$tmp/original-package"
tar --strip-components=1 -xzf "$root/original-dist/nginx-overseas-cache-8eaeb2aa8d8b-linux-$arch.tar.gz" -C "$tmp/original-package"
bash "$tmp/original-package/scripts/install-admin.sh"
healthy
for binary in overseas-admin ws-fanout config-helper; do
    [[ $(/opt/overseas-cache-v2/bin/"$binary" --version 2>&1) == 8eaeb2aa8d8b ]]
done
"$py" tests/qualification/probe.py verify
installer
healthy
for binary in overseas-admin ws-fanout config-helper; do
    [[ $(/opt/overseas-cache-v2/bin/"$binary" --version 2>&1) == 01746cc861e1 ]]
    /opt/overseas-cache-v2/bin/"$binary" --version
done
"$py" tests/qualification/probe.py verify
echo 'PASS original main V2 to exact PR candidate, with installed binary checks and shared WebSocket frames'

QUAL_TOKEN="$test_token" browser maintenance http://127.0.0.1:23000

# Ignore unpublished lab inputs; scan only service journals and emitted logs.
if (journalctl -u nginx -u ws-fanout -u overseas-cache-admin -u overseas-cache-config -u qual-origin --no-pager;
    cat /var/log/overseas-cache/traffic.log /var/log/nginx/overseas-cache.access.log "$ev/lab.log") |
    grep -F -e "$test_password" -e "$test_token" -e 'lab-timing-' -e 'lab-ws-' > /dev/null; then
    touch "$ev/leak-detected"
    echo 'FAIL synthetic secret present in service logs'
    exit 1
fi
echo 'PASS synthetic admin password, renderer token and WS probe tokens absent from service logs'
