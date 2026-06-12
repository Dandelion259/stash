#!/usr/bin/env bash
#
# update-strm-fork.sh — update this Stash fork to a new upstream version while
# keeping the .strm-support commit on top, then build a runnable Docker image.
#
# Usage:
#   ./update-strm-fork.sh                 # rebase onto upstream/develop, build
#   ./update-strm-fork.sh v0.29.0         # rebase onto release tag v0.29.0, build
#   ./update-strm-fork.sh v0.29.0 norebuild   # rebase only, skip docker build
#
# What it does:
#   1. Fetches the official Stash repo (the 'upstream' remote).
#   2. Rebases your strm-support branch onto the chosen ref. Your single
#      .strm commit replays on top. If upstream edited the same lines you did,
#      git stops for a manual conflict resolution (rare — see README notes).
#   3. Builds a Docker image 'stash-strm:<ref>' using Stash's own Dockerfile,
#      which compiles both the frontend and backend inside the build — so you
#      need only Docker installed, not Go or Node.
#
set -euo pipefail

BRANCH="strm-support"
REF="${1:-upstream/develop}"
DO_BUILD="${2:-build}"
IMAGE_TAG="stash-strm:$(echo "$REF" | sed 's#.*/##')"

cd "$(dirname "$0")"

echo ">> Fetching upstream..."
git fetch upstream --tags --prune

# Resolve a bare tag/branch name to upstream/<name> if it isn't already a ref.
if ! git rev-parse --verify --quiet "$REF^{commit}" >/dev/null; then
  echo "!! '$REF' is not a known ref. Available recent tags:"
  git tag --list 'v*' --sort=-version:refname | head -10
  exit 1
fi

echo ">> Checking out $BRANCH ..."
git checkout "$BRANCH"

# Because upstream release tags and develop diverge, a plain 'git rebase <ref>'
# would try to replay unrelated upstream commits onto $REF. Instead we replay
# ONLY our own patch commits, identified by author email: walk back from the tip
# collecting the contiguous run of commits we authored; the first commit we did
# NOT author is the old base. Then rebase --onto the new $REF from that base.
PATCH_AUTHOR="231287729+Dandelion259@users.noreply.github.com"
c="$BRANCH"
n=0
while [ "$(git log -1 --format='%ae' "$c" 2>/dev/null)" = "$PATCH_AUTHOR" ]; do
  n=$((n + 1))
  c="${c}^"
done
if [ "$n" -eq 0 ]; then
  echo "!! No patch commits by $PATCH_AUTHOR found at the tip of $BRANCH."
  echo "   The tip should be your .strm commit(s). Aborting."
  exit 1
fi
OLD_BASE_PARENT=$(git rev-parse "$c")

echo ">> Replaying $n patch commit(s) onto $REF (old base $(git rev-parse --short "$OLD_BASE_PARENT")) ..."
if ! git rebase --onto "$REF" "$OLD_BASE_PARENT" "$BRANCH"; then
  echo
  echo "!! Rebase hit a conflict. Upstream changed code near your .strm patch."
  echo "   Resolve the conflicted file(s), then run:"
  echo "       git add <file> && git rebase --continue"
  echo "   (git rerere will remember the resolution for next time.)"
  echo "   Or abort with:  git rebase --abort"
  echo "   Re-run this script after the rebase completes to build."
  exit 1
fi

echo ">> Rebase clean. Your commit on top:"
git log --oneline -1

if [ "$DO_BUILD" = "norebuild" ]; then
  echo ">> Skipping docker build (norebuild)."
  exit 0
fi

GITHASH=$(git rev-parse --short HEAD)
# describe may fail on develop without tags; fall back to the ref name.
STASH_VERSION=$(git describe --tags --exclude latest_develop 2>/dev/null || echo "$REF")

echo ">> Building Docker image $IMAGE_TAG (this compiles UI + backend; takes a while)..."
docker build \
  --build-arg GITHASH="$GITHASH" \
  --build-arg STASH_VERSION="$STASH_VERSION" \
  -t "$IMAGE_TAG" \
  -f docker/build/x86_64/Dockerfile.strm .

echo
echo ">> Done. Image built: $IMAGE_TAG"
echo "   Run it (adjust volumes to match your current stash container):"
echo "       docker run -d --name stash-strm -p 9999:9999 \\"
echo "         -v /home/ubuntu/p-stack/config/stash:/root/.stash \\"
echo "         -v /path/to/your/media:/data \\"
echo "         $IMAGE_TAG"
