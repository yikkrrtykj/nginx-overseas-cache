#!/usr/bin/env bash
set -Eeuo pipefail
[[ $GITHUB_ACTIONS == true && $RUNNER_ENVIRONMENT == github-hosted ]]
release=$1
case $release in 24.04|26.04) ;; *) exit 2 ;; esac
root=$PWD
ev=$root/artifacts/boot
mkdir -p "$ev"
exec > >(tee "$ev/host.log") 2>&1
git rev-parse HEAD 'HEAD^{tree}' | tee "$ev/source.txt"
tmp=$(mktemp -d)
pid=
cleanup() {
    if [[ -n $pid ]]; then kill "$pid" 2>/dev/null || true; fi
    if [[ -e $tmp/serial.log ]]; then
        # Do not retain the interactive console or cloud-init credential output.
        grep -E 'Linux version|Cloud-init v|reboot: Restarting' "$tmp/serial.log" > "$ev/boot-console-events.log" || true
    fi
}
trap cleanup EXIT
trap 'printf "Boot lab failed at line %s\n" "$LINENO"' ERR
( cd dist; sha256sum -c SHA256SUMS ) | tee "$ev/checksums.txt"
( cd legacy-dist; sha256sum --check --strict SHA256SUMS ) | tee "$ev/legacy-checksums.txt"
cd "$tmp"
image=ubuntu-$release-server-cloudimg-amd64.img
case "$release" in 24.04) series=noble;; 26.04) series=resolute;; esac
url=https://cloud-images.ubuntu.com/releases/$series/release
for file in SHA256SUMS SHA256SUMS.gpg "$image"; do
    curl --fail --location --silent --show-error --retry 3 --max-time 420 --output "$file" "$url/$file"
done
gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg SHA256SUMS.gpg SHA256SUMS
grep -E "[ *]$image$" SHA256SUMS > image.sum
sha256sum -c image.sum
cp image.sum "$ev/ubuntu-image.sha256"
qemu-img create -F qcow2 -b "$image" -f qcow2 lab.qcow2 12G
ssh-keygen -q -N '' -t ed25519 -f guest-key
cat > user-data <<EOF
#cloud-config
users:
  - name: qualification
    lock_passwd: true
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat guest-key.pub)
ssh_pwauth: false
package_update: false
package_upgrade: false
EOF
printf 'instance-id: qualification-%s\nlocal-hostname: overseas-qualification\n' "$release" > meta-data
cloud-localds seed.iso user-data meta-data
accel=tcg
cpu=max
if [[ -e /dev/kvm ]]; then
    sudo chmod a+rw /dev/kvm
    accel=kvm
    cpu=host
fi
echo "QEMU acceleration: $accel"
cp /usr/share/OVMF/OVMF_VARS_4M.fd vars.fd
qemu-system-x86_64 -machine q35,accel="$accel" -cpu "$cpu" -smp 2 -m 2304 -display none \
    -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive if=pflash,format=raw,file=vars.fd \
    -drive file=lab.qcow2,format=qcow2,if=virtio -drive file=seed.iso,format=raw,media=cdrom \
    -device virtio-net-pci,netdev=net0 -netdev user,id=net0,hostfwd=tcp:127.0.0.1:24231-:22 \
    -serial file:serial.log -monitor none -qmp "unix:$tmp/qmp,server=on,wait=off" > qemu.log 2>&1 &
pid=$!
opts=(-i "$tmp/guest-key" -o BatchMode=yes -o ConnectTimeout=3 -o ServerAliveInterval=3 -o ServerAliveCountMax=5 -o UserKnownHostsFile="$tmp/known_hosts" -o StrictHostKeyChecking=accept-new)
guest() { ssh "${opts[@]}" -p 24231 qualification@127.0.0.1 "$@"; }
previous=
wait_boot() {
    local id=
    for _ in {1..42}; do
        if ! kill -0 "$pid"; then cat qemu.log; return 1; fi
        id=$(timeout 7s ssh "${opts[@]}" -p 24231 qualification@127.0.0.1 cat /proc/sys/kernel/random/boot_id 2>/dev/null || true)
        if [[ $id =~ ^[0-9a-f-]+$ && $id != "$previous" ]]; then
            previous=$id
            printf '%s %s\n' "$release" "$id" | tee -a "$ev/boot-ids.txt"
            return
        fi
        sleep 3
    done
    echo 'Guest boot did not reach SSH' >&2
    return 1
}
reboot_guest() {
    guest 'sudo systemctl reboot --no-block' || true
    wait_boot
    guest 'sudo cloud-init status --wait' || true
}
wait_boot
guest 'sudo cloud-init status --wait'
git -C "$root" archive 0e2bb77bdacc4e1f825858a0d59e2070828dd1b7 > v1.tar
cp "$root"/dist/nginx-overseas-cache-*-linux-amd64.tar.gz v2.tar.gz
legacy=nginx-overseas-cache-8eaeb2aa8d8b-linux-amd64
tar -xOzf "$root/legacy-dist/$legacy.tar.gz" "$legacy/bin/config-helper" > legacy-config-helper
cp "$root/tests/qualification/boot-guest.sh" guest.sh
cp "$root/tests/origin_fixture.py" origin.py
scp "${opts[@]}" -P 24231 v1.tar v2.tar.gz legacy-config-helper guest.sh origin.py qualification@127.0.0.1:
run() {
    local label=${2:-$1}
    printf 'Guest %s stage %s\n' "$release" "$label"
    timeout --kill-after=8s 150s ssh "${opts[@]}" -p 24231 qualification@127.0.0.1 \
        "sudo bash /home/qualification/guest.sh $1" 2>&1 | tee "$ev/$label.log"
}
run provision
reboot_guest
run normal-reboot || normal_failed=1
run stage-https
reboot_guest
run verify-https-reboot
run pending-config
reboot_guest
run check-pending-config
run legacy-pending-config
reboot_guest
run check-legacy-pending-config
run interrupt-install
run pause-recovery
python3 - "$tmp/qmp" <<'QMP'
import json
import socket
import sys

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
    peer.settimeout(8)
    peer.connect(sys.argv[1])
    with peer.makefile("rb") as replies:
        if "QMP" not in json.loads(replies.readline()):
            raise RuntimeError("invalid QMP greeting")
        for command in ("qmp_capabilities", "system_reset"):
            peer.sendall((json.dumps({"execute": command}) + "\r\n").encode())
            for line in replies:
                reply = json.loads(line)
                if "event" in reply:
                    continue
                if "return" not in reply:
                    raise RuntimeError("QMP command failed: " + command)
                break
            else:
                raise RuntimeError("QMP ended before replying")
print("Hard reset confirmed while the guest held the restoration lock")
QMP
wait_boot
run check-interrupt after-reset-during-recovery
run committed-rollback
reboot_guest
run check-interrupt after-rollback-reboot
run committed-uninstall
reboot_guest
run check-interrupt after-uninstall-reboot
[[ ${normal_failed:-0} == 0 ]]
