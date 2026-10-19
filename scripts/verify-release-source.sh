#!/usr/bin/env bash
set -euo pipefail

tag="${1:?release tag is required}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
tag_commit="$(git rev-parse --verify "refs/tags/$tag^{commit}")"
test "$(git rev-parse --verify HEAD)" = "$tag_commit"
test -z "$(git status --porcelain --untracked-files=all)"

# Fetch failure must abort even when a stale origin/main reference exists.
git fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main
git rev-parse --verify 'refs/remotes/origin/main^{commit}' >/dev/null
git merge-base --is-ancestor "$tag_commit" refs/remotes/origin/main
