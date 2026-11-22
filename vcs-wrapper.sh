#!/usr/bin/env bash
CMD=$(echo $(basename "$0") | sed 's/\(.*\)-.*/\1/g')
# resolve symlinks so the extension/ guard holds no matter how the wrapper is invoked
ROOT="$(cd -- "$(dirname -- "$(readlink -f -- "${BASH_SOURCE[0]}")")" && pwd -P)"
CWD="$(pwd -P)"

if [[ "$CWD" == "$ROOT/extension" || "$CWD" == "$ROOT/extension"/* ]]; then
  echo "warning: refusing to run $CMD inside the extension/ submodule (cwd: $CWD)" >&2
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
