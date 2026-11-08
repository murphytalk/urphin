#!/usr/bin/env bash
CMD=$(echo $(basename "$0") | sed 's/\(.*\)-.*/\1/g')
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
CWD="$(pwd -P)"

if [[ "$CWD" == "$ROOT/extension" || "$CWD" == "$ROOT/extension"/* ]]; then
  echo "warning: refusing to run jj inside the extension/ submodule (cwd: $CWD)" >&2
  exit 1
fi

[ -f extension/hooks.sh ] && . extension/hooks.sh

if declare -F before_vcs >/dev/null; then
  before_vcs
fi

$CMD "$@"

if declare -F after_vcs >/dev/null; then
  after_vcs
fi
