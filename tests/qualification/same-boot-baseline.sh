#!/usr/bin/env bash
set -Eeuo pipefail
[[ $GITHUB_ACTIONS == true && $RUNNER_ENVIRONMENT == github-hosted ]]
out=$PWD/artifacts/same-boot
mkdir -p "$out"
exec > >(tee "$out/systemd.log") 2>&1
( cd dist && sha256sum -c SHA256SUMS )
sudo install -d /home/qualification
git archive 0e2bb77bdacc4e1f825858a0d59e2070828dd1b7 | sudo tee /home/qualification/v1.tar >/dev/null
sudo install -m 0644 "$(find dist -maxdepth 1 -name '*-linux-amd64.tar.gz')" /home/qualification/v2.tar.gz
sudo install -m 0644 tests/origin_fixture.py /home/qualification/origin.py
sudo bash tests/qualification/boot-guest.sh provision
sudo bash tests/qualification/boot-guest.sh interrupt-install
before=$(cat /proc/sys/kernel/random/boot_id)
sudo test -f /run/overseas-cache-installer.active
sudo test -f /var/lib/overseas-cache-installer/pending-install
sudo flock -n /var/lib/overseas-cache-installer/install.lock true
sudo systemctl daemon-reload
sudo systemctl restart nginx
sudo systemctl start overseas-cache-admin ws-fanout
sudo test -f /var/lib/overseas-cache-installer/pending-install
sudo systemctl is-active --quiet overseas-cache-admin
sudo systemctl show -p ExecStart overseas-cache-admin | grep -F /opt/overseas-cache-v2/bin/overseas-admin
sudo journalctl -b -u overseas-cache-recovery --no-pager | grep -F /run/overseas-cache-installer.active
[[ $(cat /proc/sys/kernel/random/boot_id) == "$before" ]]
printf '%s\n' 'REPRODUCED: stale runtime marker bypassed recovery; replacement Admin started with journal pending in the same boot.'
