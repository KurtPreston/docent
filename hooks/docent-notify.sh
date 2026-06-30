#!/usr/bin/env bash
# docent-notify.sh -- Cursor hook -> docentd POST /ingest (generic webhook).
set -u

input="$(cat 2>/dev/null || true)"
have_jq=1
command -v jq >/dev/null 2>&1 || have_jq=0

json_get() {
  [ "$have_jq" -eq 1 ] || { echo ""; return; }
  printf '%s' "$input" | jq -r "$1 // empty" 2>/dev/null
}

kind="${1:-}"
if [ -z "$kind" ]; then
  event="$(json_get '.hook_event_name')"
  case "$event" in
    beforeSubmitPrompt)  kind="prompt-submit" ;;
    stop)                kind="agent-stop" ;;
    afterShellExecution) kind="shell-done" ;;
    sessionStart)        kind="session-start" ;;
    sessionEnd)          kind="session-end" ;;
    *)                   kind="" ;;
  esac
fi
[ -z "$kind" ] && exit 0

root="$(json_get '.workspace_roots[0]')"
[ -z "$root" ] && root="$(json_get '.projectPath')"
[ -z "$root" ] && root="${CURSOR_PROJECT_DIR:-}"
[ -z "$root" ] && exit 0
name="$(basename "$root")"

convo="$(json_get '.conversation_id')"
[ -z "$convo" ] && convo="$(json_get '.session_id')"
host="${CURSOR_REMOTE_SSH_HOST:-}"
[ -z "$host" ] && host="$(hostname 2>/dev/null || true)"

color=""
if [ "$kind" = "session-start" ] && [ "$have_jq" -eq 1 ]; then
  settings="$root/.vscode/settings.json"
  if [ -f "$settings" ]; then
    color="$(jq -r '."workbench.colorCustomizations"."titleBar.activeBackground" // empty' "$settings" 2>/dev/null)"
  fi
fi

token="${DOCENT_TOKEN:-}"
if [ -z "$token" ] && [ -f "$HOME/.cursor/docent-token" ]; then
  token="$(tr -d '\r\n' < "$HOME/.cursor/docent-token" 2>/dev/null || true)"
fi

port="${DOCENT_PORT:-39787}"
url="http://127.0.0.1:${port}/ingest"

if [ "$have_jq" -eq 1 ]; then
  payload="$(jq -nc \
    --arg source "cursor" \
    --arg name "$name" \
    --arg kind "$kind" \
    --arg host "$host" \
    --arg path "$root" \
    --arg convo "$convo" \
    --arg color "$color" \
    '{source:$source, name:$name, kind:$kind, host:$host, path:$path}
     + (if $convo != "" then {conversationId:$convo} else {} end)
     + (if $color != "" then {color:$color} else {} end)')"
else
  payload="{\"source\":\"cursor\",\"name\":\"${name}\",\"kind\":\"${kind}\",\"host\":\"${host}\",\"path\":\"${root}\"}"
fi

if [ -n "$token" ]; then
  curl -s --max-time 2 -X POST "$url" \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer ${token}" \
    --data "$payload" >/dev/null 2>&1 || true
else
  curl -s --max-time 2 -X POST "$url" \
    -H 'Content-Type: application/json' \
    --data "$payload" >/dev/null 2>&1 || true
fi
exit 0
