#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
UPSTREAM_URL="${WECLAW_UPSTREAM_URL:-https://github.com/fastclaw-ai/weclaw.git}"

if git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  git fetch "$UPSTREAM_REMOTE" main --quiet
  UPSTREAM_SHA="$(git rev-parse "$UPSTREAM_REMOTE/main")"
else
  UPSTREAM_SHA="$(git ls-remote --heads "$UPSTREAM_URL" main | awk '{print $1}')"
fi

LOCAL_SHA="$(git rev-parse HEAD)"

echo "local:    $LOCAL_SHA"
echo "upstream: $UPSTREAM_SHA"

if git rev-parse "$UPSTREAM_REMOTE/main" >/dev/null 2>&1; then
  echo "==> commit distance (left=local only, right=upstream only)"
  git rev-list --left-right --count HEAD..."$UPSTREAM_REMOTE/main"

  echo "==> bridge-core files changed vs upstream"
  git diff --stat "${UPSTREAM_REMOTE}/main...HEAD" -- \
    README.md \
    docs \
    config \
    messaging \
    cmd \
    ilink || true

  echo "==> runtime-like paths present in this repo diff"
  git diff --name-only "${UPSTREAM_REMOTE}/main...HEAD" | rg 'launchd|guardian|scheduler|install-v2|runtime-v2' || true
fi
