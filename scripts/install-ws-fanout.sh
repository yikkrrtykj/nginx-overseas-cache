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
install_dir=/opt/ws-fanout
service_target=/etc/systemd/system/ws-fanout.service

for command in python3 systemctl install; do
    command -v "$command" >/dev/null || die "required command is missing: $command"
done

for source in \
    "$repo_dir/ws-fanout/ws_fanout.py" \
    "$repo_dir/ws-fanout/requirements.txt" \
    "$repo_dir/systemd/ws-fanout.service"; do
    [[ -f "$source" ]] || die "repository file is missing: $source"
done

install -d -o root -g root -m 0755 "$install_dir"
install -o root -g root -m 0755 \
    "$repo_dir/ws-fanout/ws_fanout.py" "$install_dir/ws_fanout.py"
install -o root -g root -m 0644 \
    "$repo_dir/ws-fanout/requirements.txt" "$install_dir/requirements.txt"

if [[ ! -x "$install_dir/venv/bin/python" ]]; then
    if ! python3 -m venv "$install_dir/venv"; then
        # A minimal Ubuntu installation may omit ensurepip/python3-venv.
        if command -v apt-get >/dev/null; then
            apt-get update
            DEBIAN_FRONTEND=noninteractive apt-get install -y python3-venv
            python3 -m venv --clear "$install_dir/venv"
        else
            die "python3 venv support is missing; install it and run this script again"
        fi
    fi
fi

"$install_dir/venv/bin/python" -m pip install \
    --disable-pip-version-check \
    --requirement "$install_dir/requirements.txt"

install -o root -g root -m 0644 \
    "$repo_dir/systemd/ws-fanout.service" "$service_target"

systemctl daemon-reload
systemctl enable ws-fanout.service >/dev/null
systemctl restart ws-fanout.service

if ! systemctl is-active --quiet ws-fanout.service; then
    systemctl --no-pager --full status ws-fanout.service || true
    die "ws-fanout.service did not start"
fi

printf 'WebSocket fanout installed and listening on 127.0.0.1:3001.\n'
