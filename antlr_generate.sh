#!/usr/bin/env bash
# Generates Go parser code for the public and (when present) extension grammars.
# Invariants enforced here: -Werror (F7), distinct packages (F6), broker name
# state per mode: public needs codes, extension needs real names (F12).
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
BASE_DIR="$ROOT/tui/commands/antlr"
EXT_DIR="$ROOT/extension/tui/commands/antlr"
ANTLR=${ANTLR:-antlr4}
COMMON=(-Dlanguage=Go -Werror -Xexact-output-dir)

# Generated parser code is gitignored, so every Go build needs it produced locally from the .g4 files.
require_antlr() {
  local missing=""
  command -v "$ANTLR" >/dev/null 2>&1 || missing="$ANTLR (ANTLR 4 tool)"
  command -v java >/dev/null 2>&1 || missing="${missing:+$missing, }java (a JDK)"
  [ -n "$missing" ] || return 0
  {
    echo "antlr_generate.sh: missing dependency: $missing"
    echo "  Generated Go parser code is not committed, so it must be produced locally from the"
    echo "  .g4 grammars before any Go build can work."
    echo "  See README.md, section \"Installing ANTLR\", or"
    echo "  https://github.com/antlr/antlr4/blob/master/doc/getting-started.md"
  } >&2
  exit 1
}

gen_public() {
  require_antlr
  echo "==> public grammar"
  if [ -x "$ROOT/extension/obscure.sh" ]; then "$ROOT/extension/obscure.sh"; fi
  rm -rf "$BASE_DIR/generated"
  mkdir -p "$BASE_DIR/generated"
  "$ANTLR" "${COMMON[@]}" -package pubparser -lib "$BASE_DIR" -o "$BASE_DIR/generated" \
    "$BASE_DIR/PublicLexer.g4" "$BASE_DIR/PublicParser.g4"
  # Obscure before generating; if the state flipped back, the output is poisoned.
  grep -q "'B1'" "$BASE_DIR/BaseLexer.g4" || {
    echo "gen_public: BaseLexer.g4 is not in code state" >&2; exit 1; }
}

gen_extension() {
  if [ ! -d "$EXT_DIR" ]; then echo "==> extension absent, skipped"; return 0; fi
  require_antlr
  echo "==> extension grammar"
  if [ -x "$ROOT/extension/restore.sh" ]; then "$ROOT/extension/restore.sh"; fi
  rm -rf "$EXT_DIR/generated"
  mkdir -p "$EXT_DIR/generated"
  "$ANTLR" "${COMMON[@]}" -package extparser -lib "$BASE_DIR" -o "$EXT_DIR/generated" \
    "$EXT_DIR/UrphinCommandLexer.g4" "$EXT_DIR/UrphinCommandParser.g4"
}

leave_resting() {  # resting state = real names, matching after_jj
  [ -x "$ROOT/extension/restore.sh" ] && "$ROOT/extension/restore.sh" || true
}
trap leave_resting EXIT

case "${1:-}" in
  public)    gen_public ;;
  extension) gen_extension ;;
  all)       gen_public; gen_extension ;;
  *)         echo "usage: $0 [public|extension|all]" >&2; exit 2 ;;
esac
