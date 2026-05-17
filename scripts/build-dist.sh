#!/usr/bin/env bash
set -Eeuo pipefail

# Build both Linux binaries and write checksums.
# This script mirrors `make dist` for environments where make is unavailable.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/backup-cleanup-linux-amd64 ./cmd/backup-cleanup
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/backup-cleanup-linux-arm64 ./cmd/backup-cleanup
(cd dist && sha256sum backup-cleanup-linux-* > SHA256SUMS)
