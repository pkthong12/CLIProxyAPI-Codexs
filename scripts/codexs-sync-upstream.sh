#!/usr/bin/env bash

set -euo pipefail

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-upstream}"
BASE_BRANCH="${BASE_BRANCH:-codexs/main}"
UPSTREAM_REF="${1:?Usage: scripts/codexs-sync-upstream.sh <upstream-tag-or-branch>}"

if [[ -n "$(git status --short)" ]]; then
  printf '%s\n' 'Working tree must be clean before syncing upstream.' >&2
  exit 1
fi

git fetch --tags "$UPSTREAM_REMOTE"
git switch "$BASE_BRANCH"
git pull --ff-only origin "$BASE_BRANCH"

UPGRADE_BRANCH="upgrade/${UPSTREAM_REF//\//-}"
if git show-ref --verify --quiet "refs/heads/$UPGRADE_BRANCH"; then
  printf 'Branch already exists: %s\n' "$UPGRADE_BRANCH" >&2
  exit 1
fi

git switch -c "$UPGRADE_BRANCH"

if git rev-parse --verify --quiet "refs/tags/$UPSTREAM_REF" >/dev/null; then
  MERGE_TARGET="$UPSTREAM_REF"
elif git rev-parse --verify --quiet "refs/remotes/$UPSTREAM_REMOTE/$UPSTREAM_REF" >/dev/null; then
  MERGE_TARGET="$UPSTREAM_REMOTE/$UPSTREAM_REF"
else
  printf 'Upstream tag or branch was not found: %s\n' "$UPSTREAM_REF" >&2
  exit 1
fi

git merge --no-ff "$MERGE_TARGET" -m "chore(upstream): sync $UPSTREAM_REF"

printf '%s\n' 'Upstream merge created.'
printf '%s\n' 'Next: run tests, push the upgrade branch, open a PR, then run the VPS canary script.'
