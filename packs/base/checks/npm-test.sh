#!/bin/sh
set -eu
cd /workspace
if ! node -e 'const p = require("./package.json"); if (!p.scripts || typeof p.scripts.test !== "string" || !p.scripts.test.trim()) process.exit(1)' 2>/dev/null; then
  echo 'acceptance requires a package.json test script' >&2
  exit 1
fi
export npm_config_offline=true
export npm_config_cache=/tmp/npm-cache
export npm_config_update_notifier=false
exec npm test --offline
