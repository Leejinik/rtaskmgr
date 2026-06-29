#!/usr/bin/env bash
# Rebuilds the embedded Linux sampler. Run this whenever cmd/sampler changes,
# BEFORE `wails build` — the binary is embedded into the app via internal/agent.
set -euo pipefail
cd "$(dirname "$0")/.."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" \
  -o internal/agent/sampler-linux-amd64 ./cmd/sampler
echo "built internal/agent/sampler-linux-amd64 ($(wc -c < internal/agent/sampler-linux-amd64) bytes)"
