#!/usr/bin/env bash
# build-extension.sh -- build the docent IDE extension into a .vsix.
#
# Installs the extension's dev dependencies, type-checks/compiles the
# TypeScript, and packages a distributable .vsix with @vscode/vsce. The output
# path is printed on the last line so callers (e.g. the installers) can consume
# it.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXT_DIR="$ROOT/apps/docent-ide-extension"
NODE="${DOCENT_NODE:-node}"
NPM="${DOCENT_NPM:-npm}"
OUT="$EXT_DIR/docent-ide-extension.vsix"

if [ ! -d "$EXT_DIR" ]; then
  echo "extension dir not found: $EXT_DIR" >&2
  exit 1
fi

echo "[build-extension] installing dev dependencies" >&2
if [ -f "$EXT_DIR/package-lock.json" ]; then
  "$NODE" "$(command -v "$NPM")" --prefix "$EXT_DIR" ci
else
  "$NODE" "$(command -v "$NPM")" --prefix "$EXT_DIR" install
fi

echo "[build-extension] compiling TypeScript" >&2
"$NODE" "$(command -v "$NPM")" --prefix "$EXT_DIR" run compile

echo "[build-extension] packaging .vsix" >&2
# Run vsce from the extension dir so it packages the right manifest. --no-dependencies
# is safe: the extension ships no runtime deps (only Node built-ins + the vscode API).
( cd "$EXT_DIR" && "$NODE" "$(command -v "$NPM")" exec --no -- @vscode/vsce package --no-dependencies -o "$OUT" )

echo "[build-extension] wrote $OUT" >&2
echo "$OUT"
