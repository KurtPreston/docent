# Binary build, probe, setup, and hook helpers for the macOS installer.

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


# ensure_probe_tunnel sets PROBE_TUNNEL_BIN to a docent-tunnel binary for the
# wizard's host listing and checks: the downloaded release's, a fresh temp
# build with --from-source, or the installed one with --no-build. Returns
# non-zero when none is available.
PROBE_TUNNEL_BIN=""
ensure_probe_tunnel() {
  [ -n "$PROBE_TUNNEL_BIN" ] && return 0
  if [ "$SKIP_BUILD" -eq 1 ]; then
    [ -x "$BIN_DIR/docent-tunnel" ] || return 1
    PROBE_TUNNEL_BIN="$BIN_DIR/docent-tunnel"
    return 0
  fi
  if [ "$FROM_SOURCE" -eq 0 ]; then
    docent_release_fetch "$RELEASE_TAG" || return 1
    [ -x "$DOCENT_RELEASE_DIR/docent-tunnel" ] || return 1
    PROBE_TUNNEL_BIN="$DOCENT_RELEASE_DIR/docent-tunnel"
    return 0
  fi
  command -v go >/dev/null 2>&1 || return 1
  local dir
  dir="$(mktemp -d)" || return 1
  trap 'rm -rf "'"$dir"'"' EXIT
  log "building docent-tunnel (for host discovery)"
  (cd "$ROOT" && go build -o "$dir/docent-tunnel" ./apps/docent-tunnel) || return 1
  PROBE_TUNNEL_BIN="$dir/docent-tunnel"
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
  # Prefer the installed release binary; fall back to the checkout
  local setup=(go run "$ROOT/apps/docent-setup") hint="go run ./apps/docent-setup"
  if [ "$FROM_SOURCE" -eq 0 ] && { [ -x "$BIN_DIR/docent-setup" ] || [ "$DRY_RUN" -eq 1 ]; }; then
    setup=("$BIN_DIR/docent-setup")
    hint="$BIN_DIR/docent-setup"
  fi
  if [ "$DRY_RUN" -eq 1 ]; then
    run "${setup[@]}" --config-dir "$CONFIG_DIR"
    return 0
  fi
  if [ -t 0 ]; then
    "${setup[@]}" --config-dir "$CONFIG_DIR"
  else
    echo "No directives in $directives — run: $hint --config-dir $CONFIG_DIR" >&2
  fi
}


# install_hooks delegates to `docent install-hooks`, which embeds the canonical
# script and merges hooks.json itself. Keeping the merge in one place (rather
# than here in jq and again on Linux) is deliberate: the hook's whole job is
# reporting agent activity, and a second implementation is a second way for it
# to drift out of sync and go quiet.
#
# Note this only covers a Cursor GUI running on this Mac. A Remote-SSH window
# executes its agent on the remote box, which needs its own install there.
install_hooks() {
  log "installing Cursor agent-activity hook"
  if [ "$DRY_RUN" -eq 1 ]; then
    run printf '%s\n' "$DOCENT_BIN install-hooks"
    return 0
  fi
  if [ ! -x "$DOCENT_BIN" ]; then
    echo "no CLI at $DOCENT_BIN; rerun the installer without --no-build" >&2
    return 1
  fi
  run "$DOCENT_BIN" install-hooks || echo "hook install failed; run '$DOCENT_BIN install-hooks' manually" >&2
}
