#!/usr/bin/env bash
# docent-notify.sh -- Cursor hook -> docentd POST /api/sessions/events.
#
# Reports AGENT activity only. Window lifecycle (open/close/focus) and
# heartbeats are owned by the docent IDE extension; this hook exists solely
# because Cursor's agent request/response events are not exposed to the
# extension API. It maps Cursor's beforeSubmitPrompt/stop hooks to the
# agent_request_sent/agent_response_received session events.
set -u

input="$(cat 2>/dev/null || true)"
have_jq=1
command -v jq >/dev/null 2>&1 || have_jq=0

json_get() {
  [ "$have_jq" -eq 1 ] || { echo ""; return; }
  printf '%s' "$input" | jq -r "$1 // empty" 2>/dev/null
}

# Resolve the session event from an explicit arg or the hook_event_name.
event="${1:-}"
case "$event" in
  agent_request_sent|agent_response_received) ;;      # explicit passthrough
  prompt-submit) event="agent_request_sent" ;;        # legacy arg aliases
  agent-stop)    event="agent_response_received" ;;
  "")
    case "$(json_get '.hook_event_name')" in
      beforeSubmitPrompt) event="agent_request_sent" ;;
      stop)               event="agent_response_received" ;;
      *)                  event="" ;;
    esac
    ;;
  *) event="" ;;
esac
[ -z "$event" ] && exit 0

root="$(json_get '.workspace_roots[0]')"
[ -z "$root" ] && root="$(json_get '.projectPath')"
[ -z "$root" ] && root="${CURSOR_PROJECT_DIR:-}"
[ -z "$root" ] && exit 0
name="$(basename "$root")"

# ideHost identifies the machine the editor's GUI runs on. For a Remote-SSH
# window this hook runs on the *remote* box, so `hostname` would be wrong and
# the ssh alias is unknown; Cursor sets CURSOR_CODE_REMOTE=true in that case, so
# we report ideHost as the boolean true ("remote, host unknown") and let docentd
# bind the event to the client-side extension's window record. For a local
# session the hostname is correct. targetHost is only sent when Cursor happens
# to expose the ssh alias (usually unset here).
ide_host=""
remote=0
if [ "${CURSOR_CODE_REMOTE:-}" = "true" ]; then
  remote=1
else
  ide_host="$(hostname 2>/dev/null || true)"
fi
target_host="${CURSOR_REMOTE_SSH_HOST:-}"

token="${DOCENT_TOKEN:-}"
if [ -z "$token" ] && [ -f "$HOME/.cursor/docent-token" ]; then
  token="$(tr -d '\r\n' < "$HOME/.cursor/docent-token" 2>/dev/null || true)"
fi

env_file="${DOCENT_ENV_FILE:-$HOME/.config/docent/.env}"
if [ -f "$env_file" ]; then
  set -a
  # shellcheck disable=SC1090
  . "$env_file"
  set +a
  token="${DOCENT_TOKEN:-$token}"
fi

if [ -n "${DOCENT_URL:-}" ]; then
  url="${DOCENT_URL%/}/api/sessions/events"
else
  port="${DOCENT_PORT:-39787}"
  url="http://127.0.0.1:${port}/api/sessions/events"
fi

if [ "$have_jq" -eq 1 ]; then
  payload="$(jq -nc \
    --arg ide "cursor" \
    --argjson remote "$remote" \
    --arg ideHost "$ide_host" \
    --arg targetHost "$target_host" \
    --arg path "$root" \
    --arg name "$name" \
    --arg event "$event" \
    '{ide:$ide, path:$path, name:$name, event:$event}
     + (if $remote == 1 then {ideHost:true} else {ideHost:$ideHost} end)
     + (if $targetHost != "" then {targetHost:$targetHost} else {} end)')"
else
  if [ "$remote" -eq 1 ]; then
    ide_host_json="true"
  else
    ide_host_json="\"${ide_host}\""
  fi
  payload="{\"ide\":\"cursor\",\"ideHost\":${ide_host_json},\"path\":\"${root}\",\"name\":\"${name}\",\"event\":\"${event}\"}"
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
