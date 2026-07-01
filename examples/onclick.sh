#!/usr/bin/env bash
# Default docent work-item launch hook (~/.config/docent/onclick.sh).
#
# docentd invokes this script with DOCENT_* environment variables describing the
# clicked work item. Customize freely; docent-setup installs this file only when
# absent.
#
# Fallback chain:
#   1. grove (cd to repo / grove project root, then grove "$DOCENT_BRANCH")
#   2. local Cursor (when DOCENT_OPEN_PATH is a directory and cursor is on PATH)
#   3. remote docent-wm (when DOCENT_OPEN_PATH, DOCENT_WM_URL, DOCENT_HOST are set)
#   4. otherwise print guidance and exit non-zero

set -euo pipefail

die() {
  echo "$*" >&2
  exit 1
}

leaf_name() {
  local p="${1%/}"
  basename "$p"
}

# grove expects to run from the project root (directory beside .base). When
# DOCENT_OPEN_PATH is a worktree checkout, walk up to that root.
grove_project_dir() {
  local d="${DOCENT_OPEN_PATH:-}"
  if [[ -z "$d" || ! -d "$d" ]]; then
    return 1
  fi
  while [[ "$d" != "/" ]]; do
    if [[ -d "$d/.base" ]]; then
      echo "$d"
      return 0
    fi
    d="$(dirname "$d")"
  done
  echo "${DOCENT_OPEN_PATH}"
}

# 1. grove-managed worktrees
if [[ -n "${DOCENT_BRANCH:-}" ]] && command -v grove >/dev/null 2>&1; then
  if [[ -z "${DOCENT_OPEN_PATH:-}" ]]; then
    die "grove launch requires DOCENT_OPEN_PATH (local-git checkout path)"
  fi
  grove_dir="$(grove_project_dir)" || die "cannot resolve repo path for grove"
  echo "launching grove worktree for ${DOCENT_BRANCH} in ${grove_dir}"
  cd "$grove_dir" || die "cannot cd to ${grove_dir}"
  exec grove "$DOCENT_BRANCH"
fi

# 2. local folder via Cursor CLI
if [[ -n "${DOCENT_OPEN_PATH:-}" && -d "${DOCENT_OPEN_PATH}" ]] && command -v cursor >/dev/null 2>&1; then
  echo "opening local path ${DOCENT_OPEN_PATH}"
  exec cursor --new-window "$DOCENT_OPEN_PATH"
fi

# 3. remote SSH window via docent-wm on the workstation
if [[ -n "${DOCENT_OPEN_PATH:-}" && -n "${DOCENT_WM_URL:-}" && -n "${DOCENT_HOST:-}" ]]; then
  name="${DOCENT_BRANCH:-}"
  if [[ -z "$name" ]]; then
    name="$(leaf_name "$DOCENT_OPEN_PATH")"
  fi
  if [[ -z "$name" && -n "${DOCENT_TICKET:-}" ]]; then
    name="$DOCENT_TICKET"
  fi
  echo "opening remote ${DOCENT_HOST}:${DOCENT_OPEN_PATH}"
  if command -v curl >/dev/null 2>&1; then
    if command -v python3 >/dev/null 2>&1; then
      payload=$(DOCENT_HOST="$DOCENT_HOST" DOCENT_OPEN_PATH="$DOCENT_OPEN_PATH" LAUNCH_NAME="$name" python3 -c '
import json, os
print(json.dumps({
    "host": os.environ["DOCENT_HOST"],
    "path": os.environ["DOCENT_OPEN_PATH"],
    "name": os.environ.get("LAUNCH_NAME", ""),
}))
')
    else
      die "python3 required to build JSON for remote open (or customize onclick.sh)"
    fi
    curl -fsS -X POST "${DOCENT_WM_URL%/}/open" \
      -H 'Content-Type: application/json' \
      -d "$payload"
    echo "remote open requested"
    exit 0
  fi
  die "curl required for remote open via docent-wm"
fi

# 4. ticket-only / no local path — user extension point
if [[ -n "${DOCENT_TICKET:-}" ]]; then
  die "no branch or open path for ${DOCENT_TICKET}; customize onclick.sh (e.g. grove in a default project)"
fi

die "nothing to launch: set DOCENT_BRANCH, DOCENT_OPEN_PATH, or customize onclick.sh"
