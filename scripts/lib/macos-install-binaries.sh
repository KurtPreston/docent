# Install the selected macOS binaries before configuring services.

# Download the release before stopping anything, so a failed download leaves
# the running services alone
RELEASE_BINS=()
if [ "$DOCENTD_MODE" = local ]; then
  RELEASE_BINS+=(docentd docent-setup)
fi
if [ "$USE_TUNNEL" = 1 ]; then
  RELEASE_BINS+=(docent-tunnel)
fi
if [ "$SKIP_BUILD" -eq 0 ] && [ "$FROM_SOURCE" -eq 0 ] && [ "${#RELEASE_BINS[@]}" -gt 0 ]; then
  docent_release_fetch "$RELEASE_TAG" || exit 1
fi

stop_previous_docent_services

if [ "$SKIP_BUILD" -eq 1 ]; then
  log "skipping install (--no-build)"
elif [ "$FROM_SOURCE" -eq 0 ]; then
  if [ "${#RELEASE_BINS[@]}" -gt 0 ]; then
    docent_release_install "$BIN_DIR" "${RELEASE_BINS[@]}" || exit 1
  else
    log "remote docentd without a tunnel — nothing to install locally"
  fi
else
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
fi

