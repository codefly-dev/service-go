# The Go runtime image: the container a Go service is built and run in when
# the runtime context is "container" (local runs). It is the official Go
# Alpine image plus the tools the runners reach for, pinned so the build
# reproduces the digest in ../runtime-image.json byte for byte.
#
# Move Go here (GO_IMAGE), re-lock, tag runtime-v<n>; the Runtime image
# workflow publishes ghcr.io/codefly-dev/service-go-runtime at that digest.
ARG GO_IMAGE=golang:1.27.2-alpine3.23@sha256:2ac5c2a64f1f970b5120fe21c6a5e3d9190b196a9ead95797564738ecd07a8a2
ARG SOURCE_DATE_EPOCH=0

FROM ${GO_IMAGE} AS runtime

# The image's Go is the Go that builds. A module whose go directive is newer
# than it fails here, in the open, rather than every fresh container
# downloading a toolchain first.
ENV GOTOOLCHAIN=local

RUN apk add --no-cache \
    bash=5.3.3-r1 \
    curl=8.22.0-r0 \
    git=2.52.0-r0 \
    jq=1.8.2-r0 \
    procps-ng=4.0.5-r0 \
    tzdata=2026e-r0 && \
    rm -f /var/log/apk.log

LABEL org.opencontainers.image.source="https://github.com/codefly-dev/service-go"

WORKDIR /workspace
