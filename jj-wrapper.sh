#!/usr/bin/env bash

[ -f extension/hooks.sh ] && . extension/hooks.sh

if declare -F before_jj >/dev/null; then
  before_jj
fi

jj "$@"

if declare -F after_jj >/dev/null; then
  after_jj
fi
