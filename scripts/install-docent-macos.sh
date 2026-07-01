#!/usr/bin/env bash
# Install docentd + docent-wm-macos on macOS (binaries, launchd, Hammerspoon launcher, Cursor hooks when Cursor is installed).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${DOCENT_BIN_DIR:-$HOME/.local/bin}"
LAUNCH_AGENTS="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
# launchd starts services with a minimal PATH; Homebrew and ~/.local/bin are not included.
DOCENT_SERVICE_PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
CONFIG_DIR="${DOCENT_CONFIG_DIR:-$HOME/.config/docent}"
CONFIG_PATH="$CONFIG_DIR/docentd.yaml"
LEGACY_CONFIG_DIR="$ROOT/userdata"
WEB_ROOT="$ROOT/apps/docentd/web"
DOCENT_PORT=39787
WM_PORT=39788

INSTALL_HOOKS=auto
INSTALL_HAMMERSPOON=1
INSTALL_LAUNCHD=1
SKIP_BUILD=0
DRY_RUN=0
DOCENTD_MODE=""  # local | remote (resolved after arg parse)
DOCENTD_URL="${DOCENTD_URL:-}"
DOCENTD_TOKEN="${DOCENTD_TOKEN:-${DOCENT_TOKEN:-}}"

usage() {
  cat <<'EOF'
Usage: install-docent-macos.sh [options]

Installs docent-wm-macos (always) and optionally docentd locally, with
launchd LaunchAgents, the Hammerspoon launcher, and Cursor hooks when
Cursor.app is installed.

On first run, asks whether docentd runs on this Mac (local) or remotely.
Local installs build docentd, run docent-setup when needed, and register
both LaunchAgents. Remote installs only docent-wm-macos locally and point
hooks/launcher at the remote docentd URL you provide.

Options:
  --hooks           Install Cursor hooks even if Cursor.app is not found
  --no-hooks        Skip ~/.cursor/hooks install
  --no-hammerspoon  Skip docent.lua install (~/.hammerspoon)
  --no-launchd      Skip launchd plist install (binaries only)
  --no-build        Skip go build (reuse existing binaries in BIN_DIR)
  --bin-dir PATH    Install binaries here (default: ~/.local/bin)
  --dry-run         Print actions without changing the system
  -h, --help        Show this help

Environment:
  DOCENT_BIN_DIR     Same as --bin-dir
  DOCENT_CONFIG_DIR  Config root (default: ~/.config/docent)
  DOCENTD_URL        Remote docentd base URL (skips local docentd install)
  DOCENTD_TOKEN      Bearer token for remote docentd / hooks
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

while [ $# -gt 0 ]; do
  case "$1" in
    --hooks) INSTALL_HOOKS=1 ;;
    --no-hooks) INSTALL_HOOKS=0 ;;
    --no-hammerspoon) INSTALL_HAMMERSPOON=0 ;;
    --no-launchd) INSTALL_LAUNCHD=0 ;;
    --no-build) SKIP_BUILD=1 ;;
    --bin-dir) shift; BIN_DIR="${1:?--bin-dir requires a path}" ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || { echo "go is required on PATH" >&2; exit 1; }

cursor_installed() {
  for candidate in /Applications/Cursor.app "$HOME/Applications/Cursor.app"; do
    [ -d "$candidate" ] && return 0
  done
  return 1
}

resolve_install_hooks() {
  case "$INSTALL_HOOKS" in
    auto)
      if cursor_installed; then
        INSTALL_HOOKS=1
      else
        INSTALL_HOOKS=0
        log "Cursor not installed — skipping hooks (--hooks to install anyway)"
      fi
      ;;
  esac
}

resolve_docentd_location() {
  if [ -n "$DOCENTD_URL" ]; then
    DOCENTD_MODE=remote
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ] || [ ! -t 0 ]; then
    DOCENTD_MODE=local
    return 0
  fi
  echo ""
  echo "Where does docentd run?"
  echo "  1) This Mac (build + launchd docentd locally) [default]"
  echo "  2) Remote machine (only install docent-wm-macos here)"
  printf "Choice [1]: "
  read -r choice
  case "${choice:-1}" in
    2|remote|Remote) DOCENTD_MODE=remote ;;
    *) DOCENTD_MODE=local ;;
  esac
}

prompt_remote_endpoint() {
  if [ -n "$DOCENTD_URL" ]; then
    return 0
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    DOCENTD_URL="https://docent.example.invalid"
    DOCENTD_TOKEN="${DOCENTD_TOKEN:-dry-run-token}"
    return 0
  fi
  printf "Remote docentd base URL (e.g. https://ubuntu.example:39787): "
  read -r DOCENTD_URL
  DOCENTD_URL="${DOCENTD_URL%/}"
  if [ -z "$DOCENTD_URL" ]; then
    echo "remote docentd URL is required" >&2
    exit 2
  fi
  if [ -z "$DOCENTD_TOKEN" ]; then
    printf "Bearer token for %s: " "$DOCENTD_URL"
    read -r DOCENTD_TOKEN
  fi
}

write_remote_config() {
  local env_file="$CONFIG_DIR/.env"
  run mkdir -p "$CONFIG_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "write DOCENT_URL/DOCENT_TOKEN to $env_file"
    run printf '%s\n' "write $CONFIG_DIR/launcher.lua"
    return 0
  fi
  touch "$env_file"
  upsert_env() {
    local key="$1" val="$2" file="$3"
    if grep -q "^${key}=" "$file" 2>/dev/null; then
      sed -i.bak "s|^${key}=.*|${key}=${val}|" "$file" && rm -f "$file.bak"
    else
      printf '%s=%s\n' "$key" "$val" >>"$file"
    fi
  }
  upsert_env DOCENT_URL "$DOCENTD_URL" "$env_file"
  if [ -n "$DOCENTD_TOKEN" ]; then
    upsert_env DOCENT_TOKEN "$DOCENTD_TOKEN" "$env_file"
  fi
  chmod 600 "$env_file"
  cat >"$CONFIG_DIR/launcher.lua" <<EOF
return {
  url = "$DOCENTD_URL",
  token = "$DOCENTD_TOKEN",
  wmPort = $WM_PORT,
}
EOF
}

run_docent_setup_if_needed() {
  local directives="$CONFIG_DIR/config.yaml"
  if [ -f "$directives" ] && grep -q '^directives:' "$directives" 2>/dev/null; then
    if grep -A1 '^directives:' "$directives" | grep -q '^  -'; then
      log "directives config present at $directives"
      return 0
    fi
  fi
  log "running docent-setup to populate $directives"
  if [ "$DRY_RUN" -eq 1 ]; then
    run go run "$ROOT/apps/docent-setup" --config-dir "$CONFIG_DIR"
    return 0
  fi
  if [ -t 0 ]; then
    go run "$ROOT/apps/docent-setup" --config-dir "$CONFIG_DIR"
  else
    echo "No directives in $directives — run: go run ./apps/docent-setup --config-dir $CONFIG_DIR" >&2
  fi
}

resolve_docentd_location
if [ "$DOCENTD_MODE" = remote ]; then
  prompt_remote_endpoint
  write_remote_config
fi

DOCENTD_BIN="$BIN_DIR/docentd"
WM_BIN="$BIN_DIR/docent-wm-macos"
PLIST_DOCENTD="$LAUNCH_AGENTS/com.slakkr.docentd.plist"
PLIST_WM="$LAUNCH_AGENTS/com.slakkr.docent-wm-macos.plist"

if [ "$SKIP_BUILD" -eq 0 ]; then
  run mkdir -p "$BIN_DIR"
  if [ "$DOCENTD_MODE" = local ]; then
    log "building docentd and docent-wm-macos"
    run go build -o "$DOCENTD_BIN" "$ROOT/apps/docentd"
  else
    log "building docent-wm-macos (remote docentd)"
  fi
  run go build -o "$WM_BIN" "$ROOT/apps/docent-wm-macos"
else
  log "skipping build (--no-build)"
fi

bootstrap_docent_config() {
  log "docent config at $CONFIG_DIR"
  run mkdir -p "$CONFIG_DIR"

  if [ ! -f "$CONFIG_PATH" ]; then
    if [ -f "$LEGACY_CONFIG_DIR/docentd.yaml" ]; then
      log "migrating $LEGACY_CONFIG_DIR/docentd.yaml → $CONFIG_PATH"
      run cp "$LEGACY_CONFIG_DIR/docentd.yaml" "$CONFIG_PATH"
      if [ "$DRY_RUN" -eq 0 ]; then
        sed -i.bak '/^userdataDir:/d' "$CONFIG_PATH" 2>/dev/null || sed -i '/^userdataDir:/d' "$CONFIG_PATH"
        rm -f "$CONFIG_PATH.bak"
      fi
    elif [ -f "$ROOT/config/docent/docentd.yaml.example" ]; then
      run cp "$ROOT/config/docent/docentd.yaml.example" "$CONFIG_PATH"
    fi
  fi

  local directives="$CONFIG_DIR/config.yaml"
  if [ ! -f "$directives" ]; then
    if [ -f "$LEGACY_CONFIG_DIR/config.yaml" ]; then
      log "migrating $LEGACY_CONFIG_DIR/config.yaml → $directives"
      run cp "$LEGACY_CONFIG_DIR/config.yaml" "$directives"
    elif [ -f "$ROOT/config/docent/config.yaml.example" ]; then
      run cp "$ROOT/config/docent/config.yaml.example" "$directives"
    fi
  fi

  local env_file="$CONFIG_DIR/.env"
  if [ ! -f "$env_file" ] && [ -f "$LEGACY_CONFIG_DIR/.env" ]; then
    log "migrating $LEGACY_CONFIG_DIR/.env → $env_file"
    run cp "$LEGACY_CONFIG_DIR/.env" "$env_file"
  fi
}

bootstrap_docent_config

if [ "$DOCENTD_MODE" = local ]; then
  run_docent_setup_if_needed
fi

if [ "$INSTALL_LAUNCHD" -eq 1 ]; then
  log "writing launchd plists"
  run mkdir -p "$LAUNCH_AGENTS" "$LOG_DIR"

  if [ "$DRY_RUN" -eq 0 ]; then
    if [ "$DOCENTD_MODE" = local ]; then
      cat >"$PLIST_DOCENTD" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.slakkr.docentd</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DOCENTD_BIN</string>
    <string>-config</string>
    <string>$CONFIG_PATH</string>
    <string>-web</string>
    <string>$WEB_ROOT</string>
  </array>
  <key>WorkingDirectory</key><string>$CONFIG_DIR</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>$DOCENT_SERVICE_PATH</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$LOG_DIR/docentd.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/docentd.log</string>
</dict>
</plist>
EOF
    fi

    cat >"$PLIST_WM" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.slakkr.docent-wm-macos</string>
  <key>ProgramArguments</key>
  <array>
    <string>$WM_BIN</string>
    <string>-port</string>
    <string>$WM_PORT</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$LOG_DIR/docent-wm-macos.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/docent-wm-macos.log</string>
</dict>
</plist>
EOF
  else
    if [ "$DOCENTD_MODE" = local ]; then
      run printf '%s\n' "write $PLIST_DOCENTD"
    fi
    run printf '%s\n' "write $PLIST_WM"
  fi

  uid="$(id -u)"
  reload_agent() {
    local label="$1" plist="$2"
    if launchctl print "gui/$uid/$label" &>/dev/null; then
      run launchctl bootout "gui/$uid" "$plist" 2>/dev/null || run launchctl unload "$plist" 2>/dev/null || true
    fi
    run launchctl bootstrap "gui/$uid" "$plist" 2>/dev/null || run launchctl load "$plist"
  }

  unload_agent() {
    local label="$1" plist="$2"
    if launchctl print "gui/$uid/$label" &>/dev/null; then
      run launchctl bootout "gui/$uid" "$plist" 2>/dev/null || run launchctl unload "$plist" 2>/dev/null || true
    fi
  }

  log "loading launch agents"
  if [ "$DOCENTD_MODE" = local ]; then
    reload_agent com.slakkr.docentd "$PLIST_DOCENTD"
  else
    unload_agent com.slakkr.docentd "$PLIST_DOCENTD"
  fi
  reload_agent com.slakkr.docent-wm-macos "$PLIST_WM"
fi

install_hooks() {
  local hooks_dir="$HOME/.cursor/hooks"
  local hooks_json="$HOME/.cursor/hooks.json"
  local src="$ROOT/hooks/docent-notify.sh"

  log "installing Cursor hooks"
  run mkdir -p "$hooks_dir"
  run cp "$src" "$hooks_dir/docent-notify.sh"
  run chmod +x "$hooks_dir/docent-notify.sh"

  if ! command -v jq >/dev/null 2>&1; then
    echo "jq not found; merge hooks/hooks.snippet.json into $hooks_json manually" >&2
    return 0
  fi

  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "merge docent entries into $hooks_json"
    return 0
  fi

  mkdir -p "$(dirname "$hooks_json")"
  [ -f "$hooks_json" ] || echo '{"version":1,"hooks":{}}' >"$hooks_json"

  local tmp hook_base="$hooks_dir/docent-notify.sh"
  tmp="$(mktemp)"
  jq --arg hook "$hook_base" '
    def addhook(ev; suffix):
      .hooks[ev] = (((.hooks[ev]) // []) | map(select((.command // "") | contains("docent-notify.sh") | not))
        + [{command: ($hook + " " + suffix), timeout: 5}]);
    if (.hooks | type) == "object" then
      .version = (.version // 1)
      | .hooks = (.hooks // {})
      | addhook("beforeSubmitPrompt"; "prompt-submit")
      | addhook("stop"; "agent-stop")
      | addhook("sessionStart"; "session-start")
      | addhook("sessionEnd"; "session-end")
      | addhook("afterShellExecution"; "shell-done")
    else
      .["beforeSubmitPrompt"] = ([(.["beforeSubmitPrompt"] // [])[] | select((.command // "") | contains("docent-notify.sh") | not)] + [{command: ($hook + " prompt-submit")}])
      | .["stop"] = ([(.["stop"] // [])[] | select((.command // "") | contains("docent-notify.sh") | not)] + [{command: ($hook + " agent-stop")}])
      | .["sessionStart"] = ([(.["sessionStart"] // [])[] | select((.command // "") | contains("docent-notify.sh") | not)] + [{command: ($hook + " session-start")}])
      | .["sessionEnd"] = ([(.["sessionEnd"] // [])[] | select((.command // "") | contains("docent-notify.sh") | not)] + [{command: ($hook + " session-end")}])
      | .["afterShellExecution"] = ([(.["afterShellExecution"] // [])[] | select((.command // "") | contains("docent-notify.sh") | not)] + [{command: ($hook + " shell-done")}])
    end
  ' "$hooks_json" >"$tmp" && mv "$tmp" "$hooks_json"
}

install_hammerspoon() {
  local hs_dir="$HOME/.hammerspoon"
  local init_lua="$hs_dir/init.lua"
  log "installing Hammerspoon launcher"
  run mkdir -p "$hs_dir"
  run cp "$ROOT/apps/docent-launcher-macos/docent.lua" "$hs_dir/docent.lua"
  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "ensure require(\"docent\") in $init_lua"
    return 0
  fi
  touch "$init_lua"
  if ! grep -q 'require("docent")' "$init_lua" 2>/dev/null; then
    printf '\nrequire("docent")\n' >>"$init_lua"
  fi

  local hs_app=""
  for candidate in /Applications/Hammerspoon.app "$HOME/Applications/Hammerspoon.app"; do
    [ -d "$candidate" ] && hs_app="$candidate" && break
  done
  if [ -z "$hs_app" ]; then
    echo "" >&2
    echo "Hammerspoon is not installed — docent.lua is in place but nothing runs it yet." >&2
    echo "  brew install --cask hammerspoon" >&2
    echo "  open -a Hammerspoon" >&2
    echo "  Then: Hammerspoon menu bar icon → Reload Config" >&2
    echo "  Hotkey: Ctrl+Alt+Space" >&2
    return 0
  fi

  if ! pgrep -x Hammerspoon >/dev/null 2>&1; then
    echo "Starting Hammerspoon…" >&2
    open -a "$hs_app"
    sleep 2
  fi
  echo "Hammerspoon config installed. Hotkey: Ctrl+Alt+Space." >&2
  echo "If the chooser does not appear, use the menu bar icon → Reload Config." >&2
}

resolve_install_hooks

if [ "$INSTALL_HOOKS" -eq 1 ]; then
  install_hooks
fi

if [ "$INSTALL_HAMMERSPOON" -eq 1 ]; then
  install_hammerspoon
fi

open_accessibility_settings() {
  # Ventura+ URL; older releases ignore unknown schemes harmlessly.
  open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Accessibility" \
    2>/dev/null \
    || open "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility" \
    2>/dev/null \
    || true
}

# macOS only adds a binary to the Accessibility list after it actually calls
# assistive APIs (osascript / System Events). We cannot pre-enable the toggle;
# probing /windows registers docent-wm-macos as interested.
prompt_accessibility_if_needed() {
  local resp
  resp="$(curl -sf --max-time 5 "http://127.0.0.1:$WM_PORT/windows" 2>/dev/null || true)"
  if [ -n "$resp" ] && ! printf '%s' "$resp" | grep -qi 'assistive access\|osascript'; then
    echo "  docent-wm   accessibility ok (/windows)"
    return 0
  fi

  echo "  docent-wm   needs Accessibility (enable this exact binary in System Settings):" >&2
  echo "              $WM_BIN" >&2
  open_accessibility_settings
  if command -v osascript >/dev/null 2>&1; then
    osascript -e "display notification \"Enable docent-wm-macos in Privacy & Security → Accessibility\" with title \"docent install\"" 2>/dev/null || true
  fi
}

if [ "$DRY_RUN" -eq 0 ]; then
  if [ "$DOCENTD_MODE" = local ]; then
    log "running doctor"
    "$DOCENTD_BIN" doctor -config "$CONFIG_PATH" || true
  fi

  log "health checks"
  sleep 1
  if [ "$DOCENTD_MODE" = local ]; then
    curl -sf "http://127.0.0.1:$DOCENT_PORT/health" >/dev/null && echo "  docentd     http://127.0.0.1:$DOCENT_PORT/  ok" || echo "  docentd     FAIL (see $LOG_DIR/docentd.log)" >&2
  else
    echo "  docentd     remote  $DOCENTD_URL"
  fi
  curl -sf "http://127.0.0.1:$WM_PORT/health" >/dev/null && echo "  docent-wm   http://127.0.0.1:$WM_PORT/  ok" || echo "  docent-wm   FAIL (see $LOG_DIR/docent-wm-macos.log)" >&2

  log "accessibility probe (registers docent-wm-macos with TCC)"
  prompt_accessibility_if_needed
fi

if [ "$DOCENTD_MODE" = local ]; then
  cat <<EOF

Installed:
  docentd           $DOCENTD_BIN
  docent-wm-macos   $WM_BIN
  config            $CONFIG_DIR/
    docentd.yaml    daemon settings
    config.yaml     collector directives
    .env            secrets (optional)
  dashboard         http://127.0.0.1:$DOCENT_PORT/
  window manager    http://127.0.0.1:$WM_PORT/

LaunchAgents:
  $PLIST_DOCENTD
  $PLIST_WM
  logs: $LOG_DIR/docentd.log, $LOG_DIR/docent-wm-macos.log

Unload: launchctl bootout gui/\$(id -u) <plist>   (or launchctl unload <plist>)

If /windows still fails, enable docent-wm-macos under
Privacy & Security → Accessibility (the install probes /windows to register it).
EOF
else
  cat <<EOF

Installed (remote docentd):
  docent-wm-macos   $WM_BIN
  docentd           $DOCENTD_URL  (remote — not installed locally)
  config            $CONFIG_DIR/
    .env            DOCENT_URL, DOCENT_TOKEN
    launcher.lua    Hammerspoon overrides
  window manager    http://127.0.0.1:$WM_PORT/

LaunchAgents:
  $PLIST_WM
  logs: $LOG_DIR/docent-wm-macos.log

If /windows still fails, enable docent-wm-macos under
Privacy & Security → Accessibility (the install probes /windows to register it).
EOF
fi
