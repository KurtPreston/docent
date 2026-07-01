#!/usr/bin/env bash
# Install docentd on Linux (binary + config + systemd --user service).
#
# This is the Linux counterpart to install-docent-macos.sh. It only installs
# docentd (the dashboard/collector daemon); there is no window manager for Linux
# — the wsm window manager (https://github.com/KurtPreston/wsm) + launcher run on
# the Windows/macOS host that connects here.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN_DIR="${DOCENT_BIN_DIR:-$HOME/.local/bin}"
CONFIG_DIR="${DOCENT_CONFIG_DIR:-$HOME/.config/docent}"
CONFIG_PATH="$CONFIG_DIR/docentd.yaml"
WEB_ROOT="$ROOT/apps/docentd/web"
SYSTEMD_DIR="$HOME/.config/systemd/user"
SERVICE_NAME="docentd.service"
SERVICE_PATH="$SYSTEMD_DIR/$SERVICE_NAME"
DOCENT_PORT="${DOCENT_PORT:-39787}"
WM_PORT="${WM_PORT:-39788}"

# Source config (the directives + ai) we repurpose into CONFIG_DIR/config.yaml.
SRC_CONFIG="$ROOT/userdata/config.yaml"
SRC_ENV="$ROOT/userdata/.env"

INSTALL_SYSTEMD=1
SKIP_BUILD=0
ENABLE_LINGER=0
DRY_RUN=0
GO="${DOCENT_GO:-}"

usage() {
  cat <<'EOF'
Usage: install-docent-linux.sh [options]

Builds docentd, lays down config under ~/.config/docent, and registers a
systemd --user service that serves the dashboard on 127.0.0.1:39787.

config.yaml and .env are canonical real files in ~/.config/docent. On first
run they are seeded from the repo's userdata/ (or the bundled examples) when
missing; an existing config is left untouched. A leftover symlink from an
earlier install is converted into a real file in place.

Options:
  --no-systemd      Skip systemd unit install (build + config only)
  --no-build        Skip go build (reuse existing binary in BIN_DIR)
  --linger          Enable lingering so docentd runs without an active login
  --port N          Dashboard port (default: 39787)
  --bin-dir PATH    Install binary here (default: ~/.local/bin)
  --dry-run         Print actions without changing the system
  -h, --help        Show this help

Environment:
  DOCENT_BIN_DIR     Same as --bin-dir
  DOCENT_CONFIG_DIR  Config root (default: ~/.config/docent)
  DOCENT_PORT        Same as --port
  DOCENT_GO          Path to a Go >= 1.22 toolchain (auto-detected otherwise)
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
    --no-systemd) INSTALL_SYSTEMD=0 ;;
    --no-build) SKIP_BUILD=1 ;;
    --linger) ENABLE_LINGER=1 ;;
    --port) shift; DOCENT_PORT="${1:?--port requires a number}" ;;
    --bin-dir) shift; BIN_DIR="${1:?--bin-dir requires a path}" ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

# --- Go toolchain selection (go.mod needs >= 1.22) ----------------------------
go_version() { "$1" version 2>/dev/null | awk '{print $3}' | sed 's/^go//'; }
go_ge_122() {
  local mm="$1" major minor rest
  [ -n "$mm" ] || return 1
  major="${mm%%.*}"; rest="${mm#*.}"; minor="${rest%%.*}"
  [ "$major" -gt 1 ] && return 0
  [ "$major" -eq 1 ] && [ "$minor" -ge 22 ] && return 0
  return 1
}
choose_go() {
  if [ -n "$GO" ]; then
    command -v "$GO" >/dev/null 2>&1 || [ -x "$GO" ] || { echo "DOCENT_GO=$GO not executable" >&2; exit 1; }
    go_ge_122 "$(go_version "$GO")" || { echo "DOCENT_GO=$GO is older than 1.22" >&2; exit 1; }
    return
  fi
  local cand
  for cand in go /usr/lib/go-1.23/bin/go /usr/lib/go-1.22/bin/go /usr/local/go/bin/go; do
    command -v "$cand" >/dev/null 2>&1 || [ -x "$cand" ] || continue
    if go_ge_122 "$(go_version "$cand")"; then GO="$cand"; return; fi
  done
  echo "need Go >= 1.22 on PATH (or set DOCENT_GO). Found:" >&2
  command -v go >/dev/null 2>&1 && echo "  go -> $(go_version go)" >&2
  exit 1
}

if [ "$SKIP_BUILD" -eq 0 ]; then
  choose_go
  log "using Go $(go_version "$GO") ($GO)"
fi

DOCENTD_BIN="$BIN_DIR/docentd"

# --- Build --------------------------------------------------------------------
if [ "$SKIP_BUILD" -eq 0 ]; then
  log "building docentd -> $DOCENTD_BIN"
  run mkdir -p "$BIN_DIR"
  run "$GO" build -o "$DOCENTD_BIN" "$ROOT/apps/docentd"
else
  log "skipping build (--no-build)"
fi

# --- Config -------------------------------------------------------------------
# Seed dst as a real file (from the first existing source) when missing. An
# existing real file is left untouched. A leftover symlink (from an earlier
# install that symlinked into the repo) is converted to a real file in place.
seed_real_file() {
  local dst="$1"; shift
  if [ -L "$dst" ]; then
    local target; target="$(readlink -f "$dst" 2>/dev/null || true)"
    log "converting symlink $dst -> real file"
    if [ "$DRY_RUN" -eq 0 ]; then
      if [ -n "$target" ] && [ -f "$target" ]; then
        cp "$target" "$dst.tmp.$$" && rm -f "$dst" && mv "$dst.tmp.$$" "$dst"
      else
        rm -f "$dst"
      fi
    fi
  fi
  if [ -f "$dst" ] && [ ! -L "$dst" ]; then
    log "$(basename "$dst") present (leaving as-is)"
    return 0
  fi
  local src
  for src in "$@"; do
    [ -n "$src" ] && [ -f "$src" ] || continue
    log "seed $dst from $src"
    run cp "$src" "$dst"
    return 0
  done
  log "no source for $dst (skipping)"
  return 0
}

bootstrap_config() {
  log "docent config at $CONFIG_DIR"
  run mkdir -p "$CONFIG_DIR"

  if [ ! -f "$CONFIG_PATH" ]; then
    log "writing $CONFIG_PATH"
    if [ "$DRY_RUN" -eq 1 ]; then
      run printf '%s\n' "write daemon config $CONFIG_PATH"
    else
      cat >"$CONFIG_PATH" <<EOF
# docentd daemon settings (generated by install-docent-linux.sh).
port: $DOCENT_PORT
refreshSec: 30
docentWmUrl: http://127.0.0.1:$WM_PORT
configDir: $CONFIG_DIR
# Collector directives + optional ai live in $CONFIG_DIR/config.yaml
# Secrets (credential_refs) live in $CONFIG_DIR/.env
EOF
    fi
  else
    log "daemon config present at $CONFIG_PATH (leaving as-is)"
  fi

  seed_real_file "$CONFIG_DIR/config.yaml" "$SRC_CONFIG" "$ROOT/config/docent/config.yaml.example"

  seed_real_file "$CONFIG_DIR/.env" "$SRC_ENV"
  if [ ! -f "$CONFIG_DIR/.env" ] && [ "$DRY_RUN" -eq 0 ]; then
    touch "$CONFIG_DIR/.env"
  fi
  [ -f "$CONFIG_DIR/.env" ] && [ "$DRY_RUN" -eq 0 ] && chmod 600 "$CONFIG_DIR/.env"
  return 0
}

bootstrap_config

# --- systemd --user service ---------------------------------------------------
if [ "$INSTALL_SYSTEMD" -eq 1 ]; then
  log "writing systemd unit $SERVICE_PATH"
  run mkdir -p "$SYSTEMD_DIR"
  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "write $SERVICE_PATH"
  else
    cat >"$SERVICE_PATH" <<EOF
[Unit]
Description=docentd (developer activity dashboard + collectors)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# gh lives in /usr/bin, cursor-agent/git tooling in ~/.local/bin
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=$DOCENTD_BIN -config $CONFIG_PATH -web $WEB_ROOT -port $DOCENT_PORT
WorkingDirectory=$CONFIG_DIR
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
  fi

  log "enabling + (re)starting $SERVICE_NAME"
  run systemctl --user daemon-reload
  run systemctl --user enable "$SERVICE_NAME"
  # restart (not just start) so re-runs pick up unit/port changes
  run systemctl --user restart "$SERVICE_NAME"

  if [ "$ENABLE_LINGER" -eq 1 ]; then
    log "enabling linger so docentd survives logout"
    run loginctl enable-linger "$USER"
  fi
fi

# --- Doctor + health ----------------------------------------------------------
if [ "$DRY_RUN" -eq 0 ]; then
  log "running doctor"
  "$DOCENTD_BIN" doctor -config "$CONFIG_PATH" || true

  if [ "$INSTALL_SYSTEMD" -eq 1 ]; then
    log "health check"
    sleep 1
    if curl -sf "http://127.0.0.1:$DOCENT_PORT/health" >/dev/null; then
      echo "  docentd     http://127.0.0.1:$DOCENT_PORT/  ok"
    else
      echo "  docentd     FAIL — journalctl --user -u $SERVICE_NAME -n 50" >&2
    fi
  fi
fi

cat <<EOF

Installed:
  docentd      $DOCENTD_BIN
  config       $CONFIG_DIR/   (canonical, real files)
    docentd.yaml   daemon settings
    config.yaml    collector directives + optional ai
    .env           secrets (credential_refs)
  dashboard    http://127.0.0.1:$DOCENT_PORT/
EOF

if [ "$INSTALL_SYSTEMD" -eq 1 ]; then
  cat <<EOF
  service      $SERVICE_PATH

Manage:
  systemctl --user status $SERVICE_NAME
  systemctl --user restart $SERVICE_NAME
  journalctl --user -u $SERVICE_NAME -f
EOF
fi

cat <<'EOF'

Note: the local-wm collector (127.0.0.1:39788) will report a failure in doctor
on this Linux host — that is expected; the window manager runs on Windows.
EOF
