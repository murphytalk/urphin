#!/usr/bin/env bash
set -euo pipefail

tests="$(cd "$(dirname "$0")" && pwd)"
modules="$(node -p 'require("node:path").dirname(require("node:path").dirname(require.resolve("playwright/package.json")))')"
args=(--rm --network host --user "$(id -u):$(id -g)"
  --volume "$tests:/tests:ro" --volume "$modules:/node_modules:ro"
  --env NODE_PATH=/node_modules)
if [[ -n "${URPHIN_DASHBOARD_ASSETS:-}" ]]; then
  args+=(--volume "$URPHIN_DASHBOARD_ASSETS:/assets:ro" --env URPHIN_DASHBOARD_ASSETS=/assets)
fi
exec docker run "${args[@]}" mcr.microsoft.com/playwright:v1.59.1-noble node /tests/browser.cjs "$@"
