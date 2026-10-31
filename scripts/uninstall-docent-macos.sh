#!/usr/bin/env bash
# Uninstall docent from macOS: everything scripts/install-docent-macos.sh can
# set up (LaunchAgents, binaries, logs, Hammerspoon launcher, Cursor hooks, IDE
# extension and its settings). Config in ~/.config/docent is kept unless
# --purge is passed or confirmed at the prompt.
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "uninstall-docent-macos.sh must be run on macOS (detected: $(uname -s))." >&2
  exit 1
fi

BIN_DIR="${DOCENT_BIN_DIR:-$HOME/.local/bin}"
LAUNCH_AGENTS="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
CONFIG_DIR="${DOCENT_CONFIG_DIR:-$HOME/.config/docent}"
CURSOR_DIR="${CURSOR_CONFIG_DIR:-$HOME/.cursor}"
HS_DIR="$HOME/.hammerspoon"
EXTENSION_ID="docent.docent-ide-extension"
HOOK_SCRIPT="docent-notify.sh"

PURGE=ask
DRY_RUN=0

usage() {
  cat <<'EOF'
Usage: uninstall-docent-macos.sh [options]

Removes what install-docent-macos.sh installed: the docentd and docent-tunnel
LaunchAgents, binaries, and logs; the Hammerspoon launcher; the Cursor
agent-activity hook (other hooks in hooks.json are kept); and the docent IDE
extension plus its docent.* editor settings. The window manager (wsm) is a
separate project and is left alone.

Options:
  --purge         Also delete the config directory (token, config, sessions)
  --keep-config   Keep the config directory without asking
  --bin-dir PATH  Where the binaries were installed (default: ~/.local/bin)
  --dry-run       Print actions without changing the system
  -h, --help      Show this help

Environment:
  DOCENT_BIN_DIR     Same as --bin-dir
  DOCENT_CONFIG_DIR  Config root (default: ~/.config/docent)
  CURSOR_CONFIG_DIR  Cursor config root (default: ~/.cursor)
EOF
}

log() { printf '==> %s\n' "$*"; }
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '[dry-run]'; printf ' %q' "$@"; printf '\n'
  else
    "$@"
  fi
}

# remove_paths deletes each existing file, logging what it removes.
remove_paths() {
  local path
  for path in "$@"; do
    if [ -e "$path" ] || [ -L "$path" ]; then
      log "removing $path"
      run rm -f "$path"
    fi
  done
}

while [ $# -gt 0 ]; do
  case "$1" in
    --purge) PURGE=1 ;;
    --keep-config) PURGE=0 ;;
    --bin-dir) shift; BIN_DIR="${1:?--bin-dir requires a path}" ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

# --- LaunchAgents -------------------------------------------------------------
# Stop each service before deleting its plist so launchd doesn't restart it.
uid="$(id -u)"
for label in com.docent.docentd com.docent.docent-tunnel; do
  plist="$LAUNCH_AGENTS/$label.plist"
  if launchctl print "gui/$uid/$label" &>/dev/null; then
    log "stopping $label"
    run launchctl bootout "gui/$uid/$label" 2>/dev/null \
      || { [ -f "$plist" ] && run launchctl unload "$plist" 2>/dev/null; } \
      || echo "  could not stop $label" >&2
  fi
  remove_paths "$plist"
done

# --- Binaries and logs --------------------------------------------------------
remove_paths "$BIN_DIR/docentd" "$BIN_DIR/docent-tunnel" \
  "$LOG_DIR/docentd.log" "$LOG_DIR/docent-tunnel.log"

# --- Hammerspoon launcher -----------------------------------------------------
# Drop docent.lua and its require line; delete init.lua if nothing else is left.
remove_paths "$HS_DIR/docent.lua"
init_lua="$HS_DIR/init.lua"
if [ -f "$init_lua" ] && grep -q 'require("docent")' "$init_lua"; then
  log "removing require(\"docent\") from $init_lua"
  run sed -i '' '/require("docent")/d' "$init_lua"
  if [ "$DRY_RUN" -eq 0 ] && ! grep -q '[^[:space:]]' "$init_lua"; then
    remove_paths "$init_lua"
  fi
fi

# --- Cursor hooks -------------------------------------------------------------
# Remove the hook script and token, then unwire only docent's hooks.json entries.
remove_paths "$CURSOR_DIR/hooks/$HOOK_SCRIPT" "$CURSOR_DIR/docent-token"
hooks_json="$CURSOR_DIR/hooks.json"
if [ -f "$hooks_json" ] && grep -q "$HOOK_SCRIPT" "$hooks_json"; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "  jq not found; remove the $HOOK_SCRIPT entries from $hooks_json manually" >&2
  elif [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "remove $HOOK_SCRIPT entries from $hooks_json"
  else
    log "removing docent hooks from $hooks_json"
    tmp="$(mktemp)"
    jq --arg s "$HOOK_SCRIPT" '
      .hooks |= (with_entries(.value |= map(select((.command // "") | contains($s) | not)))
                 | with_entries(select(.value | length > 0)))' \
      "$hooks_json" >"$tmp"
    # Delete the file outright when docent was its only user.
    if jq -e '(.hooks // {}) | length == 0' "$tmp" >/dev/null; then
      rm -f "$tmp"
      remove_paths "$hooks_json"
    else
      mv "$tmp" "$hooks_json"
    fi
  fi
fi
if [ -d "$CURSOR_DIR/hooks" ] && [ -z "$(ls -A "$CURSOR_DIR/hooks")" ]; then
  run rmdir "$CURSOR_DIR/hooks"
fi

# --- IDE extension and settings -----------------------------------------------
ide_settings() {
  case "$1" in
    cursor) printf '%s' "$HOME/Library/Application Support/Cursor/User/settings.json" ;;
    code)   printf '%s' "$HOME/Library/Application Support/Code/User/settings.json" ;;
  esac
}
for editor in cursor code; do
  # Uninstall the extension when the editor's CLI reports it.
  if command -v "$editor" >/dev/null 2>&1 \
    && "$editor" --list-extensions 2>/dev/null | grep -qix "$EXTENSION_ID"; then
    log "uninstalling $EXTENSION_ID from $editor"
    run "$editor" --uninstall-extension "$EXTENSION_ID" || echo "  $editor extension uninstall failed" >&2
  fi

  # Strip the docent.url / docent.token keys the installer wrote.
  settings="$(ide_settings "$editor")"
  [ -f "$settings" ] && grep -q '"docent\.' "$settings" || continue
  if ! command -v jq >/dev/null 2>&1 || ! jq -e . "$settings" >/dev/null 2>&1; then
    echo "  remove docent.url / docent.token from $settings manually" >&2
  elif [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "remove docent.url/docent.token from $settings"
  else
    log "removing docent settings from $settings"
    tmp="$(mktemp)"
    jq 'del(."docent.url", ."docent.token")' "$settings" >"$tmp" && mv "$tmp" "$settings"
  fi
done

# --- Config directory ---------------------------------------------------------
if [ -d "$CONFIG_DIR" ]; then
  if [ "$PURGE" = ask ]; then
    PURGE=0
    if [ -t 0 ]; then
      printf 'Delete %s (token, config, saved sessions)? [y/N] ' "$CONFIG_DIR"
      read -r ans
      case "$ans" in y|Y|yes|Yes) PURGE=1 ;; esac
    fi
  fi
  if [ "$PURGE" = 1 ]; then
    log "removing $CONFIG_DIR"
    run rm -rf "$CONFIG_DIR"
  else
    log "keeping $CONFIG_DIR (rerun with --purge to delete it)"
  fi
fi

# A running Hammerspoon keeps the launcher hotkey until its config reloads.
if pgrep -x Hammerspoon >/dev/null 2>&1; then
  echo "Hammerspoon is running: menu bar icon → Reload Config to drop the docent hotkey." >&2
fi

log "docent uninstalled; restart Cursor windows to drop the hook wiring"
