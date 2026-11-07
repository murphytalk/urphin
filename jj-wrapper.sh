#!/usr/bin/env bash

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
CWD="$(pwd -P)"

if [[ "$CWD" == "$ROOT/extension" || "$CWD" == "$ROOT/extension"/* ]]; then
  echo "warning: refusing to run jj inside the extension/ submodule (cwd: $CWD)" >&2
  exit 1
fi

[ -f extension/hooks.sh ] && . extension/hooks.sh

if declare -F before_jj >/dev/null; then
  before_jj
fi

jj "$@"

if declare -F after_jj >/dev/null; then
  after_jj
fi
