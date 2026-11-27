#!/usr/bin/env bash
set -Eeuo pipefail

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
version=${1:-$(git rev-parse --short=12 HEAD)}
[[ $version =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$ ]] || exit 2
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
export GOTOOLCHAIN=local
npm --prefix web ci --ignore-scripts
npm --prefix web run build

out=$repo/dist
mkdir -p "$out"
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
for arch in amd64 arm64; do
    name=nginx-overseas-cache-$version-linux-$arch
    root=$stage/$name
    mkdir -p "$root"/{bin,scripts,licenses}
    for command in overseas-admin ws-fanout config-helper; do
        GOOS=linux GOARCH=$arch CGO_ENABLED=0 go build -mod=readonly -trimpath -buildvcs=false \
            -ldflags="-s -w -buildid= -X main.version=$version" -o "$root/bin/$command" "./cmd/$command"
    done
    cp -r nginx systemd "$root/"
    cp scripts/install-admin.sh scripts/install-ws-fanout.sh scripts/change-upstreams.sh "$root/scripts/"
    cp README.md "$root/"
    cp docs/DEPLOYMENT.md docs/SECURITY.md "$root/"
    cp "$(go list -m -f '{{.Dir}}' github.com/gorilla/websocket)/LICENSE" "$root/licenses/gorilla-websocket.txt"
    cp "$(go list -m -f '{{.Dir}}' golang.org/x/net)/LICENSE" "$root/licenses/x-net.txt"
    cp "$(go list -m -f '{{.Dir}}' golang.org/x/net)/PATENTS" "$root/licenses/x-net-patents.txt"
    cp "$(go env GOROOT)/LICENSE" "$root/licenses/go-runtime.txt"
    tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner -C "$stage" -cf - "$name" |
        gzip -n > "$out/$name.tar.gz"
done
(cd "$out"; sha256sum "nginx-overseas-cache-$version-linux-amd64.tar.gz" \
    "nginx-overseas-cache-$version-linux-arm64.tar.gz" > SHA256SUMS)
