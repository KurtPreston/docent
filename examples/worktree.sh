#!/usr/bin/env bash
# Example docent per-worktree setup hook.
#
# Copy to ~/.config/docent/worktree.sh (or point docentd.yaml's worktreeHook
# elsewhere), make it executable, and edit. docent runs it once in every working
# directory it creates, right after the checkout and before any agent starts, and
# treats a failure as a warning: the tree is still used, the error is reported.
#
# Nothing here is required. This is the place where "a fresh checkout of this
# repository needs X before it is usable" lives, because docent cannot know X.
#
# Environment (unset when not applicable):
#   DOCENT_WORKTREE_DIR     the directory just created (also the working directory)
#   DOCENT_BRANCH           the branch checked out in it
#   DOCENT_REPO             host-relative repository, e.g. Chip/salsa
#   DOCENT_PROJECT_DIR      the root the directory was created under
#   DOCENT_BASE_REF         the ref a brand-new branch was based on
#   DOCENT_REFERENCE_DIR    an existing checkout of the same repository, to copy from
#   DOCENT_WORKTREE_OWNED   1 in docent's own tree, 0 in one the developer shares

set -euo pipefail

log() { echo "worktree.sh: $*"; }

# 1. Untracked files a checkout needs.
#
# The point of DOCENT_REFERENCE_DIR: docent's own clone is a clone, so anything
# git does not track -- .env files, local settings, credentials symlinks -- is
# simply absent from it. Copy them from the developer's checkout when there is
# one. Keep this list explicit; do not sweep in everything ignored.
if [[ -n "${DOCENT_REFERENCE_DIR:-}" && -d "${DOCENT_REFERENCE_DIR}" ]]; then
  for f in .env .env.local; do
    if [[ -f "${DOCENT_REFERENCE_DIR}/${f}" && ! -e "${DOCENT_WORKTREE_DIR}/${f}" ]]; then
      log "copying ${f}"
      cp "${DOCENT_REFERENCE_DIR}/${f}" "${DOCENT_WORKTREE_DIR}/${f}"
    fi
  done
fi

# 2. Dependencies.
#
# Only worth doing in docent's own tree if the agent will build or test. It is
# the slow step, and the hook has a 15 minute budget for all of this.
if [[ -f package.json ]] && command -v yarn >/dev/null 2>&1; then
  log "yarn install"
  yarn install --frozen-lockfile
fi

# 3. Anything else the repository needs: a generated config, a database, a
# git config local to this tree. Per-repository branches go here:
#
#   case "${DOCENT_REPO:-}" in
#     Chip/salsa) ./scripts/bootstrap-worktree.sh ;;
#   esac

log "ready: ${DOCENT_BRANCH:-?} in ${DOCENT_WORKTREE_DIR}"
