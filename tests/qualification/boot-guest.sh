#!/usr/bin/env bash
set -Eeuo pipefail
[[ $EUID == 0 ]]
cd /home/qualification
action=$1
guest_error() {
    printf 'Guest %s failed near line %s\n' "$action" "$1"
    journalctl -k -b -n 12 --grep 'DENIED|integrity|IMA' --no-pager || true
    journalctl -n 35 -u nginx -u qual-boot-origin -u ws-fanout -u overseas-cache-admin -u overseas-cache-recovery --no-pager || true
}
trap 'guest_error "$LINENO"' ERR
config=/etc/nginx/overseas-cache
history=/var/lib/overseas-cache-installer
password_file=/home/qualification/lab-password
origin_ip=93.184.215.254
package=/home/qualification/package
product_install() { bash "$package/scripts/install-admin.sh" "$@"; }
local_curl() { curl --noproxy '*' --retry 10 --retry-connrefused --retry-delay 1 -sSf --max-time 4 "$@"; }
local_login() {
    local base=$1
    shift
    printf '{"username":"admin","password":"%s"}' "$(cat "$password_file")" |
        local_curl -H "Origin: $base" -H 'Content-Type: application/json' --data @- "$@" "$base/api/login" |
        python3 -c 'import json,sys; assert json.load(sys.stdin)["ok"]'
}
target_channel() {
    curl --noproxy '*' -fsS --max-time 5 -D - -o /dev/null http://127.0.0.1:23333/ |
        python3 -c 'from urllib.parse import parse_qs,urlsplit; import sys; values=[l.split(":",1)[1].strip() for l in sys.stdin if l.lower().startswith("location:")]; assert len(values)==1; print(parse_qs(urlsplit(values[0]).query)["channelId"][0])'
}
setup_v1() {
    mkdir -p "$config"
    echo 'OVERSEAS_ADMIN_ALLOWED_NETWORKS=127.0.0.1/32,10.84.71.0/24' > "$config/admin.env"
    cat "$password_file" | python3 v1/admin/server.py --init-password-file "$config/admin-password.json"
    bash v1/scripts/install-admin.sh
    sed -i 's/pgs26_p/qualification-boot/; s/REPLACE_WITH_REAL_TOKEN/fixture-boot-data-token/' "$config/settings.conf"
    systemctl reload nginx
    local_login http://127.0.0.1:23000
}

case "$1" in
provision)
    cat /etc/os-release
    uname -a
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -q
    apt-get install -y nginx python3-venv
    mkdir -p v1 "$package"
    tar -xf v1.tar -C v1
    tar --strip-components=1 -xzf v2.tar.gz -C "$package"
    chmod 0755 v1/admin/server.py
    openssl rand -hex 25 | tr -d '\n' > "$password_file"
    chmod 0600 "$password_file"
    mkdir -p /etc/qual-boot
    printf '%s webcoder.atum.live webcodercdn.atum.live\n' "$origin_ip" >> /etc/hosts
    openssl req -nodes -x509 -newkey rsa:2048 -days 2 -subj '/CN=Qualification Boot Origin' \
        -addext 'subjectAltName=DNS:webcoder.atum.live,DNS:webcodercdn.atum.live' \
        -keyout /etc/qual-boot/key -out /etc/qual-boot/ca.crt >/dev/null 2>&1
    install /etc/qual-boot/ca.crt /usr/local/share/ca-certificates/qualification-boot.crt
    update-ca-certificates
    cat > /etc/systemd/system/qual-boot-origin.service <<EOF
[Unit]
Before=nginx.service network.target

[Service]
Type=simple
ExecStartPre=/bin/ip addr replace $origin_ip/32 dev lo
ExecStart=/usr/bin/python3 /home/qualification/origin.py /etc/qual-boot/ca.crt /etc/qual-boot/key

[Install]
WantedBy=multi-user.target
EOF
    systemctl enable --now qual-boot-origin nginx
    install -d -m 0750 -o root -g www-data /var/cache/nginx
    install -d -m 0750 -o www-data -g www-data /var/cache/nginx/web_cache
    setup_v1
    [[ $(target_channel) == qualification-boot ]]
    mkdir -p install-observe
    cat > install-observe/systemctl <<'OBSERVE'
#!/usr/bin/env bash
if [[ $1 == enable && $2 == --now && $3 == ws-fanout.service ]]; then
    namei -l /opt/overseas-cache-v2/bin/ws-fanout
    findmnt -T /opt/overseas-cache-v2/bin/ws-fanout -o TARGET,FSTYPE,OPTIONS
    runuser -u overseas-fanout -- /opt/overseas-cache-v2/bin/ws-fanout --version || true
fi
exec /usr/bin/systemctl "$@"
OBSERVE
    chmod +x install-observe/systemctl
    PATH=/home/qualification/install-observe:$PATH product_install
    local_login http://127.0.0.1:23000
    printf 'PASS baseline V1 to candidate V2 on reboot VM\n'
    ;;
normal-reboot)
    local_login http://127.0.0.1:23000
    systemctl is-active nginx ws-fanout overseas-cache-config.socket overseas-cache-admin
    [[ $(target_channel) == qualification-boot ]]
    body=$(local_curl http://127.0.0.1:23333/render)
    [[ $body == fixture-response-* ]]
    grep '^OVERSEAS_ADMIN_ALLOWED_NETWORKS=' "$config/admin-v2.env"
    grep -Eq '^OVERSEAS_ADMIN_ALLOWED_NETWORKS=.*10[.]84[.]71[.]0/24' "$config/admin-v2.env"
    printf 'PASS guest reboot, original password/ACL and data plane\n'
    ;;
pending-config)
    # Seed the exact persistent state of a transaction after atomic replacement
    # of settings and before successful Nginx validation/reload.
    snap=admin-20261128T070000Z-qualification
    install -d -m 0700 "$config/backups/$snap"
    install -m 0600 "$config/settings.conf" "$config/backups/$snap/settings.conf"
    install -m 0600 /etc/nginx/conf.d/proxy-cache.conf "$config/backups/$snap/proxy-cache.conf"
    printf '%s' "$snap" > "$config/backups/pending"
    chmod 0600 "$config/backups/pending"
    sed -i 's/qualification-boot/qualification-uncommitted/' "$config/settings.conf"
    chmod 0600 "$config/settings.conf"
    [[ $(target_channel) == qualification-boot ]]
    sync
    printf 'Seeded on-disk pending config, active Nginx still confirmed baseline\n'
    ;;
check-pending-config)
    channel=$(target_channel)
    if [[ $channel == qualification-boot && ! -e $config/backups/pending ]]; then
        echo 'PASS configuration recovered before any management request'
    elif [[ $channel == qualification-uncommitted && -f $config/backups/pending ]]; then
        echo 'FAIL boot activated uncommitted settings before the first Admin configuration request'
        touch /home/qualification/config-boot-failed
    else
        printf 'Unexpected post-boot state for staged config: %s\n' "$channel"
        exit 1
    fi
    if [[ -f $config/backups/pending ]]; then
        /opt/overseas-cache-v2/bin/config-helper --recover
        for _ in {1..12}; do
            if [[ $(target_channel) == qualification-boot ]]; then break; fi
            sleep 0.25
        done
        [[ $(target_channel) == qualification-boot ]]
        echo 'PASS explicit helper recovery restored the prior active config'
    fi
    ;;
interrupt-install)
    product_install rollback
    local_login http://127.0.0.1:23000
    # Stop precisely after replacements, at systemd's reload boundary.
    mkdir install-hook
    cat > install-hook/systemctl <<'EOF'
#!/usr/bin/env bash
if [[ $1 == daemon-reload ]]; then
    printf 'installer process terminated at filesystem-to-systemd boundary\n'
    sync
    kill -KILL "$PPID"
    exit 137
fi
exec /usr/bin/systemctl "$@"
EOF
    chmod +x install-hook/systemctl
    code=0
    if PATH=/home/qualification/install-hook:$PATH product_install; then
        echo 'Interrupted install unexpectedly succeeded'
    else
        code=$?
    fi
    [[ $code == 137 ]]
    echo "interrupted installer status $code"
    [[ $(systemctl is-active overseas-cache-admin || true) == inactive ]]
    systemctl show -p ExecStart overseas-cache-admin | grep -q nginx-overseas-cache-admin/server.py
    grep -q /opt/overseas-cache-v2/bin/overseas-admin /etc/systemd/system/overseas-cache-admin.service
    ;;
check-interrupt)
    local_login http://127.0.0.1:23000
    if systemctl show -p ExecStart overseas-cache-admin | grep -q nginx-overseas-cache-admin/server.py; then
        echo 'PASS interrupted installation boot restored the V1 service definition'
    else
        echo 'FAIL boot started the V2 service from the interrupted, uncommitted upgrade'
        touch /home/qualification/install-boot-failed
    fi
    [[ $(target_channel) == qualification-boot ]]
    [[ ! -e /home/qualification/install-boot-failed && ! -e /home/qualification/config-boot-failed ]]
    ;;
*) exit 2 ;;
esac
