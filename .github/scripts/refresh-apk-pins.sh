#!/usr/bin/env bash
# refresh-apk-pins — re-resolve the Alpine package versions pinned in the Go
# runtime image, asked of the base image that image is actually built from.
#
# Why this exists, and why it is NOT the same call as refresh-base-digests.sh:
# a pinned base DIGEST does not follow upstream, so upstream publishing a new
# one is harmless and a job that failed on it would only train people to ignore
# red. A pinned apk VERSION is the opposite: Alpine's index serves only the
# current -rN of each package, so the day a patch lands the pin stops resolving
# and the image cannot be built at all. Drift here is not cosmetic, it is a
# countdown to a red main — and nothing was watching for it, because
# Dependabot's docker ecosystem points only at /templates/builder and its
# updater rewrites `FROM …@sha256` references, not apk pins. That is the gap
# this closes.
#
# The versions stay pinned. They have to: runtime-image.json locks this image
# by digest and ci.yml rebuilds it --no-cache and fails unless the digest
# reproduces, so the package set is part of the asserted bytes. Unpinning would
# not remove the failure, it would move it from this image's build (which names
# the package that moved) to the digest comparison (which can only say one
# sha256 is not another).
#
#   refresh-apk-pins.sh           rewrite the Dockerfile in place
#   refresh-apk-pins.sh --check   report drift and exit 1, changing nothing
set -euo pipefail

CHECK=0
DOCKERFILE=""
for argument in "$@"; do
  case "$argument" in
    --check) CHECK=1 ;;
    -*) echo "unknown option: $argument" >&2; exit 2 ;;
    *) DOCKERFILE="$argument" ;;
  esac
done
if [[ -z "$DOCKERFILE" ]]; then
  ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
  DOCKERFILE="$ROOT/pkg/runtime/runtime-image/Dockerfile"
fi

command -v docker >/dev/null || { echo "docker is required to resolve package versions" >&2; exit 2; }

# The base the pins are resolved against is the one this image is built from,
# read from the file rather than passed in: resolving against a different
# Alpine would hand back versions this build cannot install.
base="$(sed -n 's/^ARG GO_IMAGE=\(.*\)$/\1/p' "$DOCKERFILE" | head -1)"
[[ -n "$base" ]] || { echo "$DOCKERFILE declares no ARG GO_IMAGE" >&2; exit 2; }

# Try the configured HTTPS mirror explicitly at the unchanged pinned digest.
# Direct registry references cannot express an HTTP mirror's transport policy,
# so leave those to Docker. If the direct pull fails, Docker also retains its
# normal ordered mirror/origin fallback through the original reference.
mirror="$(docker info --format '{{range .RegistryConfig.Mirrors}}{{println .}}{{end}}' | head -1)"
if [[ "$mirror" == https://* && "$base" == golang:*@sha256:* ]]; then
  mirror="${mirror#https://}"
  mirrored="${mirror%/}/library/$base"
  if docker pull "$mirrored" >&2; then
    base="$mirrored"
  fi
fi

# Every `name=version` token inside the apk add instruction. A RUN spans
# continuation lines, so the instruction is rejoined before it is read. perl
# does the joining because BSD sed does not understand \f and `mapfile` is
# bash 4 — this has to run on a developer's mac as well as on the runner.
pinned=()
while IFS= read -r pin; do
  pinned+=("$pin")
done < <(
  perl -0pe 's/\\\n\s*/ /g' "$DOCKERFILE" |
    grep -E '^[[:space:]]*RUN .*apk add' |
    grep -oE '[a-zA-Z0-9_.+-]+=[a-zA-Z0-9_.+-]+'
)
(( ${#pinned[@]} )) || { echo "$DOCKERFILE pins no apk package versions" >&2; exit 2; }

names=()
for pin in "${pinned[@]}"; do names+=("${pin%%=*}"); done

# One container for the whole set: `apk policy` lists each package's available
# versions newest first, and the newest is the only one the index still serves.
resolved="$(docker run --rm --entrypoint sh "$base" -c "
  apk update >/dev/null 2>&1 || exit 1
  for name in ${names[*]}; do
    version=\$(apk policy \"\$name\" 2>/dev/null | sed -n '2s/^[[:space:]]*\(.*\):\$/\1/p')
    printf '%s %s\n' \"\$name\" \"\$version\"
  done
")"

drift=0
for pin in "${pinned[@]}"; do
  name="${pin%%=*}"
  was="${pin#*=}"
  now="$(awk -v n="$name" '$1 == n {print $2}' <<<"$resolved")"
  if [[ -z "$now" ]]; then
    echo "$name: $base serves no version of this package" >&2
    exit 2
  fi
  if [[ "$now" == "$was" ]]; then
    echo "$name is current ($was)"
    continue
  fi
  drift=1
  if (( CHECK )); then
    echo "$name drifted: pinned $was, index now serves $now — this image can no longer be built"
    continue
  fi
  # The package name is part of the match, so a version that happens to appear
  # elsewhere in the file is left alone and only this pin moves.
  perl -pi -e "s/\b\Q$name=$was\E\b/$name=$now/g" "$DOCKERFILE"
  echo "$name: $was -> $now"
done

if (( drift )); then
  if (( CHECK )); then
    exit 1
  fi
  echo
  echo "The image changed, so its locked digest has to move with it:"
  echo "  rebuild pkg/runtime/runtime-image, tag runtime-v<n> so the Runtime"
  echo "  image workflow publishes it, then write that digest into"
  echo "  pkg/runtime/runtime-image.json. Publish before pinning, never after."
fi
