#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

echo "==> fork-core verification"
echo "repo: $ROOT_DIR"
echo "local head: $(git rev-parse HEAD)"
echo "origin/main: $(git rev-parse origin/main 2>/dev/null || echo unavailable)"
echo "upstream/main: $(git ls-remote --heads "${WECLAW_UPSTREAM_URL:-https://github.com/fastclaw-ai/weclaw.git}" main | awk '{print $1}')"

echo "==> go build ./..."
go build ./...

TEST_PKGS=(./config ./messaging ./cmd)
if go list ./obsidian >/dev/null 2>&1; then
  TEST_PKGS+=(./obsidian)
fi

echo "==> go test ${TEST_PKGS[*]}"
go test "${TEST_PKGS[@]}"

echo "==> fork-core verification passed"
