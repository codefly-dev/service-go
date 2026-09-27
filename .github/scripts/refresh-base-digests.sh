#!/usr/bin/env bash
# refresh-base-digests — re-resolve the base image digests pinned in the
# builder template.
#
# This is the break-glass path, not the routine one. The routine path is
# Dependabot: .github/dependabot.yml points its docker ecosystem at
# /templates/builder, Dependabot's Docker updater matches this file by
# /dockerfile|containerfile/i, and it rewrites `tag@sha256:…` as one unit every
# week. Reach for this script when you need the pin moved now — a published
# base was withdrawn, a CVE landed mid-week, or you are reviewing whether the
# pin has gone stale — not on a schedule.
#
#   refresh-base-digests.sh           rewrite the template in place
#   refresh-base-digests.sh --check   report drift and exit 1, changing nothing
#
# --check is deliberately not wired into CI. The pin's whole purpose is that
# the recipe does not follow upstream; a job that fails the moment upstream
# publishes would turn every base rebuild into a red build and train people to
# ignore it.
#
# The digest written is whatever the tag resolves to, which for the official
# images is an OCI image index covering every architecture. Do not substitute a
# single-platform manifest digest: pkg/builder declares linux/amd64 and
# linux/arm64, and a per-platform pin satisfies one of them at most.
set -euo pipefail

CHECK=0
TEMPLATE=""
for argument in "$@"; do
  case "$argument" in
    --check) CHECK=1 ;;
    -*) echo "unknown option: $argument" >&2; exit 2 ;;
    *) TEMPLATE="$argument" ;;
  esac
done
ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
TEMPLATE="${TEMPLATE:-$ROOT/templates/builder/Dockerfile.tmpl}"

command -v docker >/dev/null || { echo "docker is required to resolve digests" >&2; exit 2; }

drift=0
while IFS= read -r reference; do
  image="${reference%@*}"
  pinned="${reference#*@}"
  resolved="$(docker buildx imagetools inspect "$image" --format '{{.Manifest.Digest}}')"
  if [[ "$resolved" != sha256:* ]]; then
    echo "$image: could not resolve a digest (got ${resolved:-empty})" >&2
    exit 2
  fi
  if [[ "$resolved" == "$pinned" ]]; then
    echo "$image is current ($pinned)"
    continue
  fi
  drift=1
  if (( CHECK )); then
    echo "$image drifted: pinned $pinned, now $resolved"
    continue
  fi
  # The tag is part of the match, so a bare `alpine:3.24` elsewhere in the file
  # is left alone and only the pinned reference moves.
  perl -pi -e "s/\Q$image\E\@\Q$pinned\E/$image\@$resolved/g" "$TEMPLATE"
  echo "$image: $pinned -> $resolved"
done < <(grep -oE '^FROM (--platform=\S+ )?[^ ]+@sha256:[0-9a-f]{64}' "$TEMPLATE" | awk '{print $NF}')

if (( CHECK && drift )); then
  exit 1
fi
