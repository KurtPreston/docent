#!/usr/bin/env bash
# Install docentd on macOS (binary, launchd, Hammerspoon launcher, Cursor hooks
# when Cursor is installed). The local window manager is a separate project --
# install it from https://github.com/KurtPreston/wsm (default port 39788).
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "install-docent-macos.sh must be run on macOS (detected: $(uname -s))." >&2
  echo "On Linux use: scripts/install-docent-linux.sh" >&2
  echo "On Windows use: scripts/install-docent-windows.ps1" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${DOCENT_BIN_DIR:-$HOME/.local/bin}"
LAUNCH_AGENTS="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs"
# launchd starts services with a minimal PATH; Homebrew and ~/.local/bin are not included.
DOCENT_SERVICE_PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
CONFIG_DIR="${DOCENT_CONFIG_DIR:-$HOME/.config/docent}"
CONFIG_PATH="$CONFIG_DIR/docentd.yaml"
# Installer-owned record of the last remote install (real endpoint + tunnel
# choice), so a re-run can reuse it. The runtime .env keeps DOCENT_URL pointed at
# the tunnel loopback, so the real host can't be recovered from it.
REMOTE_STATE="$CONFIG_DIR/remote.conf"
LEGACY_CONFIG_DIR="$ROOT/userdata"
WEB_ROOT="$ROOT/apps/docentd/web"
DOCENT_PORT=39787
WSM_PORT=39788

INSTALL_HOOKS=auto
INSTALL_HAMMERSPOON=1
INSTALL_LAUNCHD=1
SKIP_BUILD=0
DRY_RUN=0
DOCENTD_MODE=""  # local | remote (resolved after arg parse)
DOCENTD_URL="${DOCENTD_URL:-}"
DOCENTD_TOKEN="${DOCENTD_TOKEN:-${DOCENT_TOKEN:-}}"
USE_TUNNEL=auto  # auto (default: forward a remote docentd via docent-tunnel) | 0 (--no-tunnel)
SSH_HOST="${DOCENT_TUNNEL_HOST:-}"
SSH_IDENTITY="${DOCENT_TUNNEL_IDENTITY:-}"
NODE="${DOCENT_NODE:-node}"
NPM="${DOCENT_NPM:-npm}"

# Populated by read_saved_remote_state from $REMOTE_STATE (last remote install).
SAVED_URL=""
SAVED_TOKEN=""
SAVED_USE_TUNNEL=""
SAVED_SSH_HOST=""
SAVED_SSH_IDENTITY=""

usage() {
  cat <<'EOF'
Usage: install-docent-macos.sh [options]

Installs docentd (optionally, locally) with a launchd LaunchAgent, the
Hammerspoon launcher, and Cursor hooks when Cursor.app is installed. The
window manager itself lives in the separate wsm project -- install it from
https://github.com/KurtPreston/wsm.

On first run, asks whether docentd runs on this Mac (local) or remotely.
Local installs build the dashboard (Vite/React) + docentd (dashboard embedded
into the binary via -tags embed), run docent-setup when needed, and register
its LaunchAgent. Remote installs point hooks/launcher at the remote docentd
URL you provide (nothing is built locally). On a re-run it first offers to reuse
the previous install's config (the local/remote choice, plus the remote
URL/token/tunnel host), so updating never forces you to re-pick the deployment
or retype credentials; DOCENTD_URL still overrides.

Options:
  --hooks           Install Cursor hooks even if Cursor.app is not found
  --no-hooks        Skip ~/.cursor/hooks install
  --no-hammerspoon  Skip docent.lua install (~/.hammerspoon)
  --no-launchd      Skip launchd plist install (binaries only)
  --no-build        Skip go build (reuse existing binaries in BIN_DIR)
  --bin-dir PATH    Install binaries here (default: ~/.local/bin)
  --ssh-host HOST   SSH host for the docent-tunnel forward to a remote docentd
                    (default: the host in the remote URL). Aliases from
                    ~/.ssh/config are resolved to their real HostName via
                    `ssh -G`, since the tunnel does not read ssh_config.
  --ssh-identity P  SSH private key for the forward (default: the IdentityFile
                    from `ssh -G`, else ssh-agent)
  --no-tunnel       Don't set up the SSH forward; hit the remote URL directly
  --dry-run         Print actions without changing the system
  -h, --help        Show this help

Environment:
  DOCENT_BIN_DIR       Same as --bin-dir
  DOCENT_CONFIG_DIR    Config root (default: ~/.config/docent)
  DOCENTD_URL          Remote docentd base URL (skips local docentd install)
  DOCENTD_TOKEN        Bearer token for remote docentd / hooks
  DOCENT_TUNNEL_HOST   Same as --ssh-host (implies --tunnel)
  DOCENT_TUNNEL_IDENTITY  Same as --ssh-identity
  DOCENT_NODE          Node binary for the dashboard build (default: node; needs >= 18)
  DOCENT_NPM           npm binary (default: npm)
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
    --ssh-host) shift; SSH_HOST="${1:?--ssh-host requires a host}" ;;
    --ssh-identity) shift; SSH_IDENTITY="${1:?--ssh-identity requires a path}" ;;
    --no-tunnel) USE_TUNNEL=0 ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || { echo "go is required on PATH" >&2; exit 1; }

# The dashboard is a Vite/React build embedded into docentd via -tags embed.
node_major() { "$1" -v 2>/dev/null | sed -E 's/^v([0-9]+).*/\1/'; }
build_frontend() {
  if [ "$DRY_RUN" -eq 0 ]; then
    command -v "$NODE" >/dev/null 2>&1 || { echo "need Node >= 18 on PATH to build the dashboard (set DOCENT_NODE; e.g. 'nvm use 20' or 'brew install node')" >&2; exit 1; }
    command -v "$NPM" >/dev/null 2>&1 || { echo "need npm on PATH to build the dashboard (set DOCENT_NPM)" >&2; exit 1; }
    local nmajor; nmajor="$(node_major "$NODE")"
    if [ -z "$nmajor" ] || [ "$nmajor" -lt 18 ]; then
      echo "need Node >= 18 to build the dashboard (found $("$NODE" -v 2>/dev/null || echo none)); set DOCENT_NODE" >&2
      exit 1
    fi
    log "building dashboard with Node $("$NODE" -v)"
  else
    log "building dashboard (requires Node >= 18)"
  fi
  run "$NPM" --prefix "$WEB_ROOT" ci
  run "$NPM" --prefix "$WEB_ROOT" run build
}

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

# read_saved_remote_state loads the last remote install's endpoint/tunnel choice
# from $REMOTE_STATE (written by write_remote_config) into SAVED_* globals.
read_saved_remote_state() {
  [ -f "$REMOTE_STATE" ] || return 0
  local line key val
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      \#*|"") continue ;;
    esac
    key="${line%%=*}"; val="${line#*=}"
    case "$key" in
      DOCENTD_URL) SAVED_URL="$val" ;;
      DOCENTD_TOKEN) SAVED_TOKEN="$val" ;;
      USE_TUNNEL) SAVED_USE_TUNNEL="$val" ;;
      SSH_HOST) SAVED_SSH_HOST="$val" ;;
      SSH_IDENTITY) SAVED_SSH_IDENTITY="$val" ;;
    esac
  done <"$REMOTE_STATE"
}

# offer_reuse_config offers to reuse a previous install before asking anything,
# restoring the prior local/remote choice (and the remote URL/token/tunnel host).
# Sets DOCENTD_MODE when the config is reused; leaves it unset otherwise.
offer_reuse_config() {
  local ans
  read_saved_remote_state
  if [ -n "$SAVED_URL" ]; then
    echo ""
    echo "Found existing docent config (remote docentd):"
    echo "  URL    $SAVED_URL"
    if [ -n "$SAVED_TOKEN" ]; then echo "  token  set"; else echo "  token  (none)"; fi
    if [ "$SAVED_USE_TUNNEL" = 1 ] && [ -n "$SAVED_SSH_HOST" ]; then echo "  tunnel via $SAVED_SSH_HOST"; fi
    printf "Reuse this config? [Y/n]: "
    read -r ans
    case "${ans:-Y}" in
      n|N|no|No) return 0 ;;
    esac
    DOCENTD_MODE=remote
    DOCENTD_URL="$SAVED_URL"
    [ -n "$DOCENTD_TOKEN" ] || DOCENTD_TOKEN="$SAVED_TOKEN"
    if [ "$SAVED_USE_TUNNEL" = 0 ]; then USE_TUNNEL=0; else USE_TUNNEL=1; fi
    [ -n "$SSH_HOST" ] || SSH_HOST="$SAVED_SSH_HOST"
    [ -n "$SSH_IDENTITY" ] || SSH_IDENTITY="$SAVED_SSH_IDENTITY"
  elif [ -f "$CONFIG_PATH" ]; then
    echo ""
    echo "Found existing docent config (local docentd)."
    printf "Reuse this config? [Y/n]: "
    read -r ans
    case "${ans:-Y}" in
      n|N|no|No) return 0 ;;
    esac
    DOCENTD_MODE=local
  fi
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
  offer_reuse_config
  [ -n "$DOCENTD_MODE" ] && return 0
  echo ""
  echo "Where does docentd run?"
  echo "  1) This Mac (build + launchd docentd locally) [default]"
  echo "  2) Remote machine (configure hooks/launcher for a remote docentd)"
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

# url_host extracts the bare hostname from a base URL (http://desktop:39787 -> desktop).
url_host() {
  local u="$1"
  u="${u#*://}"   # strip scheme
  u="${u%%/*}"    # strip path
  u="${u%%:*}"    # strip port
  printf '%s' "$u"
}

# expand_tilde expands a leading ~ / ~/ to $HOME.
expand_tilde() {
  case "$1" in
    "~") printf '%s' "$HOME" ;;
    "~/"*) printf '%s/%s' "$HOME" "${1#"~/"}" ;;
    *) printf '%s' "$1" ;;
  esac
}

# ssh_effective resolves a host through `ssh -G` and prints "HOSTNAME\tIDENTITY".
# docent-tunnel dials the host directly and does NOT read ~/.ssh/config, so an
# SSH alias like `desktop` must be turned into its real HostName here (and its
# configured IdentityFile picked up) or the tunnel can never connect. Falls back
# to the input host and an empty identity when ssh is unavailable.
ssh_effective() {
  local host="$1" key val rhost="" ident="" expanded
  if ! command -v ssh >/dev/null 2>&1; then
    printf '%s\t%s\n' "$host" ""
    return 0
  fi
  while IFS=' ' read -r key val; do
    case "$key" in
      hostname) [ -n "$val" ] && rhost="$val" ;;
      identityfile)
        if [ -z "$ident" ]; then
          expanded="$(expand_tilde "$val")"
          [ -f "$expanded" ] && ident="$expanded"
        fi
        ;;
    esac
  done < <(ssh -G "$host" 2>/dev/null)
  [ -n "$rhost" ] || rhost="$host"
  printf '%s\t%s\n' "$rhost" "$ident"
}

# resolve_tunnel sets up the launcher/dashboard to reach a remote docentd through
# a local SSH forward (docent-tunnel). This is the default in remote mode; pass
# --no-tunnel to hit the remote URL directly instead. It sets USE_TUNNEL to 1/0
# and, when 1, fills SSH_HOST (defaulting to the host in the remote URL).
resolve_tunnel() {
  if [ "$USE_TUNNEL" = 0 ]; then
    return 0
  fi
  USE_TUNNEL=1

  local raw_host="$SSH_HOST"
  [ -n "$raw_host" ] || raw_host="$(url_host "$DOCENTD_URL")"

  if [ "$DRY_RUN" -eq 0 ] && [ -t 0 ] && [ -z "$SSH_HOST" ]; then
    local host_in
    printf "SSH host for the dev box [%s]: " "$raw_host"
    read -r host_in
    raw_host="${host_in:-$raw_host}"
  fi

  # Turn SSH aliases into the real hostname/identity — the tunnel can't use ssh_config.
  local resolved ident
  IFS=$'\t' read -r resolved ident < <(ssh_effective "$raw_host")
  if [ "$resolved" != "$raw_host" ]; then
    log "resolved SSH host $raw_host -> $resolved (via ssh -G)"
  fi
  SSH_HOST="$resolved"
  [ -n "$SSH_IDENTITY" ] || SSH_IDENTITY="$ident"
}

# remote_connection_ok returns 0 when the launcher can actually reach the remote
# docentd. In tunnel mode it runs `docent-tunnel -check` (the exact SSH + auth +
# host-key path the daemon uses); in direct mode it curls /health then an authed
# endpoint, treating 401 as a bad token. Diagnostics go to stderr.
remote_connection_ok() {
  if [ "$USE_TUNNEL" = 1 ]; then
    if [ ! -x "$DOCENT_TUNNEL_BIN" ]; then
      echo "  docent-tunnel binary not found at $DOCENT_TUNNEL_BIN" >&2
      return 1
    fi
    local -a args=(-check -host "$SSH_HOST" -local "127.0.0.1:$DOCENT_PORT" -remote "127.0.0.1:$DOCENT_PORT")
    [ -n "$SSH_IDENTITY" ] && args+=(-identity "$SSH_IDENTITY")
    DOCENT_TUNNEL_TOKEN="$DOCENTD_TOKEN" "$DOCENT_TUNNEL_BIN" "${args[@]}"
    return $?
  fi
  if ! curl -sf --max-time 8 "$DOCENTD_URL/health" >/dev/null 2>&1; then
    echo "  $DOCENTD_URL/health not reachable" >&2
    return 1
  fi
  local code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 -H "Authorization: Bearer $DOCENTD_TOKEN" "$DOCENTD_URL/api/config" 2>/dev/null)"
  if [ "$code" = "401" ]; then
    echo "  $DOCENTD_URL rejected the bearer token (401)" >&2
    return 1
  fi
  return 0
}

# reprompt_tunnel / reprompt_direct re-collect connection details after a failed
# validation attempt, re-resolving aliases so a corrected host takes effect.
reprompt_tunnel() {
  local host_in ident_in token_in resolved ident
  printf "SSH host for the dev box [%s]: " "$SSH_HOST"
  read -r host_in
  [ -n "$host_in" ] && SSH_HOST="$host_in"
  IFS=$'\t' read -r resolved ident < <(ssh_effective "$SSH_HOST")
  SSH_HOST="$resolved"
  printf "SSH identity key [%s]: " "${SSH_IDENTITY:-${ident:-ssh-agent}}"
  read -r ident_in
  SSH_IDENTITY="${ident_in:-${SSH_IDENTITY:-$ident}}"
  printf "Bearer token [%s]: " "$([ -n "$DOCENTD_TOKEN" ] && echo 'keep current' || echo none)"
  read -r token_in
  [ -n "$token_in" ] && DOCENTD_TOKEN="$token_in"
}

reprompt_direct() {
  local url_in token_in
  printf "Remote docentd base URL [%s]: " "$DOCENTD_URL"
  read -r url_in
  [ -n "$url_in" ] && DOCENTD_URL="${url_in%/}"
  printf "Bearer token [%s]: " "$([ -n "$DOCENTD_TOKEN" ] && echo 'keep current' || echo none)"
  read -r token_in
  [ -n "$token_in" ] && DOCENTD_TOKEN="$token_in"
}

# validate_remote_connection blocks the wizard until the remote docentd is
# actually reachable, so an install can never finish in a broken state. Only
# interactive TTY runs loop; dry-run / non-interactive do a single best-effort
# check and warn (keeping CI/automation non-blocking).
validate_remote_connection() {
  if [ "$DRY_RUN" -eq 1 ]; then
    log "dry-run: skipping remote connection check"
    return 0
  fi
  local interactive=1
  [ -t 0 ] || interactive=0

  log "verifying connection to remote docentd"
  while :; do
    if remote_connection_ok; then
      log "remote docentd connection verified"
      return 0
    fi
    if [ "$interactive" -eq 0 ]; then
      echo "  WARNING: could not verify the remote docentd connection (continuing non-interactively)" >&2
      return 0
    fi
    echo ""
    echo "Could not connect to the remote docentd yet."
    echo "  1) Re-enter connection details and retry [default]"
    echo "  2) Skip the SSH forward; connect to the URL directly (--no-tunnel)"
    echo "  3) Continue anyway (write config without a verified connection)"
    echo "  4) Abort"
    printf "Choice [1]: "
    local choice
    read -r choice
    case "${choice:-1}" in
      2) USE_TUNNEL=0; reprompt_direct ;;
      3) echo "  continuing without a verified connection" >&2; return 0 ;;
      4) echo "aborted." >&2; exit 1 ;;
      *) if [ "$USE_TUNNEL" = 1 ]; then reprompt_tunnel; else reprompt_direct; fi ;;
    esac
  done
}

write_remote_config() {
  local env_file="$CONFIG_DIR/.env"
  local sessions_url="$DOCENTD_URL"
  if [ "$USE_TUNNEL" = 1 ]; then
    sessions_url="http://127.0.0.1:$DOCENT_PORT"
  fi
  run mkdir -p "$CONFIG_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "write DOCENT_URL=$sessions_url / DOCENT_TOKEN to $env_file"
    run printf '%s\n' "write $CONFIG_DIR/launcher.lua (url=$sessions_url)"
    run printf '%s\n' "write $REMOTE_STATE (real endpoint $DOCENTD_URL for reuse)"
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
  upsert_env DOCENT_URL "$sessions_url" "$env_file"
  if [ -n "$DOCENTD_TOKEN" ]; then
    upsert_env DOCENT_TOKEN "$DOCENTD_TOKEN" "$env_file"
  fi
  chmod 600 "$env_file"
  cat >"$CONFIG_DIR/launcher.lua" <<EOF
return {
  url = "$sessions_url",
  token = "$DOCENTD_TOKEN",
  wsmPort = $WSM_PORT,
}
EOF

  # Installer-only record of the *real* endpoint + tunnel choice so the next run
  # can reuse it (the .env DOCENT_URL above is the tunnel loopback, not the host).
  ( umask 177
    cat >"$REMOTE_STATE" <<EOF
# Written by install-docent-macos.sh so a re-run can reuse this config.
DOCENTD_URL=$DOCENTD_URL
DOCENTD_TOKEN=$DOCENTD_TOKEN
USE_TUNNEL=$USE_TUNNEL
SSH_HOST=$SSH_HOST
SSH_IDENTITY=$SSH_IDENTITY
EOF
  )
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
  resolve_tunnel
  # write_remote_config is deferred until after the tunnel binary is built and
  # the connection is validated (see below), so we never persist broken config.
else
  USE_TUNNEL=0 # a local docentd is already on this machine's loopback
fi

DOCENTD_BIN="$BIN_DIR/docentd"
DOCENT_TUNNEL_BIN="$BIN_DIR/docent-tunnel"
PLIST_DOCENTD="$LAUNCH_AGENTS/com.docent.docentd.plist"
PLIST_TUNNEL="$LAUNCH_AGENTS/com.docent.docent-tunnel.plist"

uid="$(id -u)"

# unload_agent boots out a loaded LaunchAgent. Prefer the plist path when it
# exists (works on older launchctl); fall back to domain/label.
unload_agent() {
  local label="$1" plist="${2:-}"
  if ! launchctl print "gui/$uid/$label" &>/dev/null; then
    return 0
  fi
  log "unloading launch agent $label"
  if [ -n "$plist" ] && [ -f "$plist" ]; then
    run launchctl bootout "gui/$uid" "$plist" 2>/dev/null \
      || run launchctl unload "$plist" 2>/dev/null \
      || run launchctl bootout "gui/$uid/$label" 2>/dev/null \
      || true
  else
    run launchctl bootout "gui/$uid/$label" 2>/dev/null || true
  fi
}

# stop_previous_docent_services clears the way for a reinstall: unload any
# LaunchAgent whose program is docentd/docent-tunnel, then kill stray processes
# left from manual runs or a prior KeepAlive race.
stop_previous_docent_services() {
  local plist prog base label
  log "stopping previous docentd / docent-tunnel services"

  if [ -d "$LAUNCH_AGENTS" ]; then
    for plist in "$LAUNCH_AGENTS"/*.plist; do
      [ -f "$plist" ] || continue
      prog="$(/usr/libexec/PlistBuddy -c 'Print :ProgramArguments:0' "$plist" 2>/dev/null)" || continue
      base="$(basename "$prog")"
      case "$base" in
        docentd|docent-tunnel) ;;
        *) continue ;;
      esac
      label="$(/usr/libexec/PlistBuddy -c 'Print :Label' "$plist" 2>/dev/null)" || continue
      unload_agent "$label" "$plist"
    done
  fi

  # Known labels even if their plists were already removed.
  unload_agent com.docent.docentd "$PLIST_DOCENTD"
  unload_agent com.docent.docent-tunnel "$PLIST_TUNNEL"

  for name in docentd docent-tunnel; do
    if pgrep -x "$name" >/dev/null 2>&1; then
      log "stopping running $name process(es)"
      run pkill -x "$name" || true
    fi
  done

  # Give the listen port a moment to release after KeepAlive teardown.
  if [ "$DRY_RUN" -eq 0 ]; then
    sleep 0.5
  fi
}

stop_previous_docent_services

if [ "$SKIP_BUILD" -eq 0 ]; then
  if [ "$DOCENTD_MODE" = local ]; then
    run mkdir -p "$BIN_DIR"
    build_frontend
    log "building docentd (dashboard embedded via -tags embed)"
    run go build -tags embed -o "$DOCENTD_BIN" "$ROOT/apps/docentd"
  else
    log "remote docentd — nothing to build locally"
  fi
  if [ "$USE_TUNNEL" = 1 ]; then
    run mkdir -p "$BIN_DIR"
    log "building docent-tunnel"
    run go build -o "$DOCENT_TUNNEL_BIN" "$ROOT/apps/docent-tunnel"
  fi
else
  log "skipping build (--no-build)"
fi

# With the tunnel binary in place, block until the remote docentd is actually
# reachable (or the user opts to skip/abort), then persist the validated config.
if [ "$DOCENTD_MODE" = remote ]; then
  validate_remote_connection
  write_remote_config
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

# open_trigger is left unset on macOS, and no `cursor` collector directive is
# added. A cursor directive shells `cursor --status` on every dashboard poll;
# from launchd that path does `open -n` and briefly spawns a second Cursor GUI
# (window flicker). Prefer open_trigger.provider: wsm (with the separate wsm
# daemon) plus a `wsm` directive, open_trigger.provider: cursor alone for
# deep-link open only (no cursor directive), or leave unset for no session
# column. Existing config.yaml blocks are left untouched.

bootstrap_docent_config

if [ "$DOCENTD_MODE" = local ]; then
  run_docent_setup_if_needed
fi

reload_agent() {
  local label="$1" plist="$2"
  unload_agent "$label" "$plist"
  run launchctl bootstrap "gui/$uid" "$plist" 2>/dev/null || run launchctl load "$plist"
}

if [ "$INSTALL_LAUNCHD" -eq 1 ] && [ "$DOCENTD_MODE" = local ]; then
  log "writing launchd plist"
  run mkdir -p "$LAUNCH_AGENTS" "$LOG_DIR"

  if [ "$DRY_RUN" -eq 0 ]; then
    cat >"$PLIST_DOCENTD" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.docent.docentd</string>
  <key>ProgramArguments</key>
  <array>
    <string>$DOCENTD_BIN</string>
    <string>-config</string>
    <string>$CONFIG_PATH</string>
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
  else
    run printf '%s\n' "write $PLIST_DOCENTD"
  fi

  log "loading launch agent"
  reload_agent com.docent.docentd "$PLIST_DOCENTD"
fi

if [ "$INSTALL_LAUNCHD" -eq 1 ] && [ "$USE_TUNNEL" = 1 ]; then
  log "writing docent-tunnel launchd plist"
  run mkdir -p "$LAUNCH_AGENTS" "$LOG_DIR"

  if [ "$DRY_RUN" -eq 0 ]; then
    {
      printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>'
      printf '%s\n' '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">'
      printf '%s\n' '<plist version="1.0">'
      printf '%s\n' '<dict>'
      printf '%s\n' '  <key>Label</key><string>com.docent.docent-tunnel</string>'
      printf '%s\n' '  <key>ProgramArguments</key>'
      printf '%s\n' '  <array>'
      printf '    <string>%s</string>\n' "$DOCENT_TUNNEL_BIN"
      printf '    <string>-host</string><string>%s</string>\n' "$SSH_HOST"
      printf '    <string>-local</string><string>127.0.0.1:%s</string>\n' "$DOCENT_PORT"
      printf '    <string>-remote</string><string>127.0.0.1:%s</string>\n' "$DOCENT_PORT"
      [ -n "$SSH_IDENTITY" ] && printf '    <string>-identity</string><string>%s</string>\n' "$SSH_IDENTITY"
      printf '%s\n' '  </array>'
      printf '  <key>WorkingDirectory</key><string>%s</string>\n' "$HOME"
      printf '%s\n' '  <key>EnvironmentVariables</key>'
      printf '%s\n' '  <dict>'
      printf '    <key>PATH</key><string>%s</string>\n' "$DOCENT_SERVICE_PATH"
      printf '%s\n' '  </dict>'
      printf '%s\n' '  <key>RunAtLoad</key><true/>'
      printf '%s\n' '  <key>KeepAlive</key><true/>'
      printf '  <key>StandardOutPath</key><string>%s/docent-tunnel.log</string>\n' "$LOG_DIR"
      printf '  <key>StandardErrorPath</key><string>%s/docent-tunnel.log</string>\n' "$LOG_DIR"
      printf '%s\n' '</dict>'
      printf '%s\n' '</plist>'
    } >"$PLIST_TUNNEL"
  else
    run printf '%s\n' "write $PLIST_TUNNEL (host=$SSH_HOST, 127.0.0.1:$DOCENT_PORT -> remote 127.0.0.1:$DOCENT_PORT)"
  fi

  log "loading docent-tunnel launch agent"
  reload_agent com.docent.docent-tunnel "$PLIST_TUNNEL"
fi

# Drop LaunchAgent plists that don't belong in this mode so a leftover
# local/tunnel install does not come back at next login.
remove_unused_launch_agent() {
  local label="$1" plist="$2"
  unload_agent "$label" "$plist"
  if [ -f "$plist" ]; then
    log "removing unused launch agent $plist"
    run rm -f "$plist"
  fi
}
if [ "$DOCENTD_MODE" != local ]; then
  remove_unused_launch_agent com.docent.docentd "$PLIST_DOCENTD"
fi
if [ "$USE_TUNNEL" != 1 ]; then
  remove_unused_launch_agent com.docent.docent-tunnel "$PLIST_TUNNEL"
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

if [ "$DRY_RUN" -eq 0 ]; then
  if [ "$DOCENTD_MODE" = local ]; then
    log "running doctor"
    "$DOCENTD_BIN" doctor -config "$CONFIG_PATH" || true

    log "health checks"
    sleep 1
    curl -sf "http://127.0.0.1:$DOCENT_PORT/health" >/dev/null && echo "  docentd     http://127.0.0.1:$DOCENT_PORT/  ok" || echo "  docentd     FAIL (see $LOG_DIR/docentd.log)" >&2
  fi

  if [ "$USE_TUNNEL" = 1 ]; then
    log "health check via docent-tunnel"
    sleep 1
    if curl -sf --max-time 5 "http://127.0.0.1:$DOCENT_PORT/health" >/dev/null 2>&1; then
      echo "  docentd     http://127.0.0.1:$DOCENT_PORT/ (via docent-tunnel -> $SSH_HOST)  ok"
    else
      echo "  docentd     not reachable through docent-tunnel yet (see $LOG_DIR/docent-tunnel.log)" >&2
    fi
  fi

  if curl -sf --max-time 5 "http://127.0.0.1:$WSM_PORT/health" >/dev/null 2>&1; then
    echo "  wsm         http://127.0.0.1:$WSM_PORT/  ok"
  else
    echo "  wsm         not reachable on :$WSM_PORT — install it from https://github.com/KurtPreston/wsm" >&2
  fi
fi

if [ "$DOCENTD_MODE" = local ]; then
  cat <<EOF

Installed:
  docentd           $DOCENTD_BIN
  config            $CONFIG_DIR/
    docentd.yaml    daemon settings
    config.yaml     collector directives
    .env            secrets (optional)
  dashboard         http://127.0.0.1:$DOCENT_PORT/

LaunchAgents:
  $PLIST_DOCENTD
  logs: $LOG_DIR/docentd.log

Unload: launchctl bootout gui/\$(id -u) <plist>   (or launchctl unload <plist>)

Window manager: install the wsm daemon from https://github.com/KurtPreston/wsm
(it serves the window manager on http://127.0.0.1:$WSM_PORT/ and handles its own
Accessibility permission).
EOF
else
  if [ "$USE_TUNNEL" = 1 ]; then
    cat <<EOF

Installed (remote docentd, via docent-tunnel):
  docentd           $DOCENTD_URL  (remote — reached through the local forward)
  docent-tunnel     $DOCENT_TUNNEL_BIN  (127.0.0.1:$DOCENT_PORT -> $SSH_HOST:127.0.0.1:$DOCENT_PORT)
  launcher/dash     http://127.0.0.1:$DOCENT_PORT/
  config            $CONFIG_DIR/
    .env            DOCENT_URL, DOCENT_TOKEN
    launcher.lua    Hammerspoon overrides

LaunchAgents:
  $PLIST_TUNNEL
  logs: $LOG_DIR/docent-tunnel.log

The forward is owned by docent-tunnel (launchd KeepAlive), so it is live
whenever you are logged in — independent of any Cursor Remote-SSH session.

Window manager: install the wsm daemon from https://github.com/KurtPreston/wsm
(it serves the window manager on http://127.0.0.1:$WSM_PORT/).
EOF
  else
    cat <<EOF

Installed (remote docentd):
  docentd           $DOCENTD_URL  (remote — not installed locally)
  config            $CONFIG_DIR/
    .env            DOCENT_URL, DOCENT_TOKEN
    launcher.lua    Hammerspoon overrides

Window manager: install the wsm daemon from https://github.com/KurtPreston/wsm
(it serves the window manager on http://127.0.0.1:$WSM_PORT/).
EOF
  fi
fi
