#!/usr/bin/env bash
set -Eeuo pipefail
# This test modifies services and host name resolution on a disposable runner.
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] ||
    { echo 'Use a GitHub-hosted Linux runner for the installation test' >&2; exit 2; }
root=$(pwd)
mkdir -p artifacts/install
exec > >(tee artifacts/install/install.log) 2>&1
tmp=$(mktemp -d)
cleanup() {
    sudo systemctl stop v2-fixture-origin.service 2>/dev/null || true
    rm -rf -- "$tmp"
}
trap cleanup EXIT
diagnostics() {
    local at=$1
    printf 'Installation fixture failed at line %s\n' "$at"
    sudo journalctl -u nginx -u overseas-cache-config -u overseas-cache-admin --no-pager -n 50 || true
    sudo journalctl -k --no-pager -n 20 --grep 'apparmor="DENIED"' || true
    sudo tail -n 15 /var/log/overseas-cache/traffic.log || true
    sudo namei -l /var/cache/nginx/web_cache || true
}
trap 'diagnostics "$LINENO"' ERR
tar -xzf "$(find dist -maxdepth 1 -name '*-linux-amd64.tar.gz' -print -quit)" -C "$tmp"
package=$(find "$tmp" -maxdepth 1 -type d -name 'nginx-overseas-cache-*' -print -quit)
[[ -x $package/bin/overseas-admin ]]
origin=93.184.215.254
sudo ip addr add "$origin/32" dev lo
printf '%s webcoder.atum.live webcodercdn.atum.live origin-v2.example cdn-v2.example\n' "$origin" | sudo tee -a /etc/hosts >/dev/null
openssl req -x509 -nodes -newkey rsa:2048 -days 1 -subj /CN=webcoder.atum.live \
    -addext 'subjectAltName=DNS:webcoder.atum.live,DNS:webcodercdn.atum.live,DNS:origin-v2.example,DNS:cdn-v2.example' \
    -keyout "$tmp/key.pem" -out "$tmp/cert.pem" >/dev/null 2>&1
sudo systemd-run --unit v2-fixture-origin /usr/bin/python3 "$root/tests/origin_fixture.py" "$tmp/cert.pem" "$tmp/key.pem"
sudo systemctl enable --now nginx

install() { sudo bash "$package/scripts/install-admin.sh" "$@"; }
admin=127.0.0.1:23000
relay=127.0.0.1:3001
data=127.0.0.1:23333
curl_local() { curl --noproxy '*' --fail --silent --show-error --max-time 20 "$@"; }
password='Isolated installation password'
legacy_password='Isolated legacy credential'
initialize() {
    sudo mkdir -p /etc/nginx/overseas-cache
    printf '%s' "$1" | sudo "$package/bin/overseas-admin" --init-password-file /etc/nginx/overseas-cache/admin-password.json
}
login() {
    local value=$1
    curl_local -c "$tmp/cookies" -H "Origin: http://$admin" -H 'Content-Type: application/json' \
        --data "{\"username\":\"admin\",\"password\":\"$value\"}" "http://$admin/api/login" >/dev/null
}
check_services() {
    for service in overseas-cache-admin.service ws-fanout.service nginx.service; do
        systemctl is-active --quiet "$service"
    done
    curl_local --retry 8 --retry-connrefused --retry-delay 1 "http://$relay/healthz"
    curl_local --retry 8 --retry-connrefused --retry-delay 1 "http://$admin/healthz"
}
cache_status() {
    curl_local --write-out "\\nfixture cache request %{url_effective}: HTTP %{http_code}\\n" -D - -o /dev/null "$@" | awk 'tolower($1)=="x-cache-status:" {gsub("\r","",$2); print $2} /^fixture cache request/ { print > "/dev/stderr" }'
}

initialize "$password"
install
check_services
login "$password"
# The upstream fixture is deliberately untrusted until its CA is installed.
[[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "http://$data/render") == 502 ]]
sudo install -m 0644 "$tmp/cert.pem" /usr/local/share/ca-certificates/overseas-fixture.crt
sudo update-ca-certificates
sudo systemctl reload nginx
for _ in {1..20}; do
    if [[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' "http://$data/render") == 200 ]]; then break; fi
    sleep 0.2
done
first=$(curl_local "http://$data/render")
second=$(curl_local "http://$data/render")
[[ $first != "$second" ]]
if [[ $(cache_status "http://$data/templates/sample.woff2") != MISS ]]; then
    diagnostics "$LINENO"
    # Only a failed hosted fixture enables the raw error log for one
    # synthetic, credential-free cache request.
    sudo sed -i 's@error_log /dev/null crit;@error_log /var/log/nginx/overseas-fixture-error.log error;@' \
        /etc/nginx/sites-available/overseas-cache
    sudo systemctl reload nginx
    for _ in {1..5}; do curl -sS --noproxy '*' -o /dev/null "http://$data/templates/sample.woff2"; sleep 0.2; done
    sudo tail -n 15 /var/log/nginx/overseas-fixture-error.log || true
    exit 1
fi
[[ $(cache_status "http://$data/templates/sample.woff2") == HIT ]]
[[ $(cache_status -H 'Cookie: fixture=personal' "http://$data/templates/sample.woff2") == BYPASS ]]
[[ $(cache_status -H 'Authorization: Bearer fixture' "http://$data/templates/sample.woff2") == BYPASS ]]
[[ $(cache_status "http://$data/templates/private.woff2") == MISS ]]
[[ $(cache_status "http://$data/templates/private.woff2") == MISS ]]
[[ $(cache_status "http://$data/templates-auth/fixture/synthetic-path-key/static/js/main.1234abcd.js") == MISS ]]
[[ $(cache_status "http://$data/templates-auth/fixture/synthetic-path-key/static/js/main.1234abcd.js") == HIT ]]
[[ -z $(cache_status "http://$data/templates-auth/fixture/synthetic-path-key/static/js/main.js") ]]
[[ $(curl --noproxy '*' -s -o /dev/null -w '%{http_code}' -H 'Origin: http://other.invalid' "http://$data/ws") == 403 ]]
if sudo grep -q 'synthetic-path-key' /var/log/overseas-cache/traffic.log; then exit 1; fi
echo 'Fresh V2, Nginx TLS and cache boundaries passed'
install
check_services
login "$password"
curl_local -b "$tmp/cookies" "http://$admin/api/v2/config" > "$tmp/config"
nonce=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["csrfToken"])' < "$tmp/config")
curl --noproxy '*' --fail-with-body --silent --show-error --max-time 75 -b "$tmp/cookies" -H "Origin: http://$admin" -H "X-CSRF-Token: $nonce" -H 'Content-Type: application/json' \
    --data '{"originHost":"origin-v2.example","cdnHost":"cdn-v2.example","channelId":"fixture","accountId":"render","token":"synthetic-installed-token"}' \
    "http://$admin/api/v2/config" > "$tmp/saved" || {
        python3 -c 'import json,sys; print(json.load(sys.stdin).get("message",""))' < "$tmp/saved" || true
        diagnostics "$LINENO"
        # This fixture uses only synthetic Nginx data. Reproduce validation
        # under the installed helper's systemd restrictions for its stderr.
        sudo systemctl stop overseas-cache-config.service
        sudo mkdir -p /run/systemd/system/overseas-cache-config.service.d
        printf '[Service]\nType=oneshot\nExecStart=\nExecStart=/usr/sbin/nginx -t\n' | \
            sudo tee /run/systemd/system/overseas-cache-config.service.d/fixture-diagnostic.conf >/dev/null
        sudo systemctl daemon-reload
        sudo systemctl start overseas-cache-config.service || true
        sudo journalctl -u overseas-cache-config --no-pager -n 30 || true
        exit 1
    }
python3 -c 'import json,sys; r=json.load(sys.stdin); assert r["ok"] and r["backup"] and r["config"]["tokenPresent"]; assert "token" not in r["config"]' < "$tmp/saved"
sudo systemctl restart overseas-cache-admin.service ws-fanout.service
check_services
login "$password"
install uninstall
[[ $(systemctl is-active overseas-cache-admin.service || true) != active ]]
echo 'Repeat install, real config apply, restarts and uninstall passed'

git archive 0e2bb77bdacc4e1f825858a0d59e2070828dd1b7 | tar -xf - -C "$tmp"
initialize "$legacy_password"
sudo bash "$tmp/scripts/install-admin.sh"
# Preserve a non-default password, domain data, token and cache file across upgrade.
sudo sed -i -e 's/pgs26_p/legacy_channel/' -e 's/REPLACE_WITH_REAL_TOKEN/synthetic-legacy-token/' /etc/nginx/overseas-cache/settings.conf
sudo systemctl reload nginx
sudo cp /etc/nginx/overseas-cache/settings.conf "$tmp/old-settings"
sudo chmod 0644 "$tmp/old-settings"
sudo touch /var/cache/nginx/web_cache/fixture-sentinel
# Start a distinct upgrade history after the clean install scenario.
sudo mv /var/lib/overseas-cache-installer/original /var/lib/overseas-cache-installer/first-clean
install
check_services
login "$legacy_password"
sudo cmp /etc/nginx/overseas-cache/settings.conf "$tmp/old-settings"
sudo test -f /var/cache/nginx/web_cache/fixture-sentinel
# Inject malformed Nginx site config into a throwaway copy of the archive.
cp -r "$package" "$tmp/invalid"
printf 'invalid_directive_v2_fixture;\n' >> "$tmp/invalid/nginx/sites-available/overseas-cache"
if sudo bash "$tmp/invalid/scripts/install-admin.sh"; then echo 'invalid install unexpectedly succeeded'; exit 1; fi
check_services
sudo nginx -t -q
login "$legacy_password"
install rollback
systemctl is-active --quiet overseas-cache-admin.service
login "$legacy_password"
sudo cmp /etc/nginx/overseas-cache/settings.conf "$tmp/old-settings"
sudo test -f /var/cache/nginx/web_cache/fixture-sentinel
echo 'V1 upgrade, injected install failure, and V2-to-V1 rollback passed'

sudo systemctl stop v2-fixture-origin.service
sudo /opt/ws-fanout/venv/bin/python "$root/tests/compare_fanout.py" \
    --v1 "$tmp/ws-fanout/ws_fanout.py" --v2 "$package/bin/ws-fanout" \
    --cert "$tmp/cert.pem" --key "$tmp/key.pem" --output "$root/artifacts/install/fanout-v1-v2.json" --soak-seconds 60
