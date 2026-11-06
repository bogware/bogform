#!/usr/bin/env bash
# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0

# Builds the dashboard for the AWS deployment module, which reads the output directory.
#
# Usage: scripts/build-server.sh [output directory]

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output="${1:-$root/build/server}"

rm -rf "$output"
mkdir -p "$output"

echo "Building the server..."
(
  cd "$root"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o "$output/bootstrap" ./cmd/bogform-server
)

echo "Building the UI..."
(
  cd "$root/web"
  npm ci --no-audit --no-fund
  npm run build
)
cp -r "$root/web/dist" "$output/ui"

echo "Done: $output"
