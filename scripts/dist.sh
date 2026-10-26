#!/bin/sh
# Builds the release assets in dist/: static binaries, service files and
# checksums.txt. Used by `make dist` and by the release workflow.
set -eu

version=${1:?usage: dist.sh <version>}
rm -rf dist
mkdir -p dist
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" \
    -o "dist/vpowerd-linux-$arch" ./cmd/vpowerd
done
cp deploy/vpowerd.openrc deploy/vpowerd.service deploy/vpowerd.env.example deploy/99-vpowerd.rules dist/
(cd dist && sha256sum vpowerd-linux-amd64 vpowerd-linux-arm64 vpowerd.openrc vpowerd.service vpowerd.env.example 99-vpowerd.rules > checksums.txt)
