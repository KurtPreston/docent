# Check and report the completed macOS installation.

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
  docentd           127.0.0.1:$DOCENT_PORT on $SSH_HOST  (remote — reached through the local forward)
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
