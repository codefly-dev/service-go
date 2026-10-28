---
name: verify-changes
description: Run the full local gate walk for codefly-dev/service-go before opening a PR, including the two suites that skip themselves silently — the real-CGO packaging test (needs Docker) and the plugin manifest guard, whose commands otherwise exist only inside CI and a reusable workflow in another repo. Use when you have changed anything under pkg/ or templates/ and want to know whether CI will be green, when `go test ./...` passed but you touched packaging, cross-compilation, the Dockerfile template, or the kustomize templates, or when a check is red and you need to reproduce it locally.
---

# Verifying a change here

`go test ./...` is the floor, not the walk. Two suites in this repo **skip
themselves when their prerequisite is missing** and report `ok`, so a green run
proves nothing about the surfaces they cover. The manifest guard's commands live
inside a reusable workflow in `codefly-dev/.github` and appear nowhere in this
repo.

Run what your change touches. Say in the PR which of these you ran.

## 1. The three CI steps

From `.github/workflows/ci.yml`. The Go version comes from `go.mod`.

```bash
go build ./...
go test ./...
go vet ./...
```

This also runs the root policy tests — workflow pins, Dependabot grouping, the
`AGENTS.md` budget. A failure there is about repo config, not your code.

## 2. Real-CGO packaging — if you touched `pkg/builder` packaging or `go.mod`'s Go version

Needs Docker running. Covers the native path and the cross-compile path through
the `goreleaser-cross` companion, with a fixture that genuinely uses cgo — a
pure-Go fixture misses both failures this guards.

```bash
SERVICE_GO_PACKAGE_TESTS=required \
  go test -race -count=1 -timeout 15m ./pkg/builder -run TestPackageRealCGO
```

Without the env var it prints `ok` having run nothing.

If it fails on a cross target, check that the `goreleaser-cross` image version
matches the Go version in `go.mod`. Bumping Go without moving the image is the
failure this test exists for. **Do not** set `CGO_ENABLED=0` to get past it:
that ships an agent whose tree-sitter parser cannot work.

## 3. Manifest guard — if you touched `templates/deployment/` or `Builder.Deploy`

CI runs this through `codefly-dev/.github/.github/workflows/plugin-manifest-guard.yml`,
which renders the bundle **twice** and requires byte-for-byte identical trees, then
scans both for ownership concepts a manifest producer must not have (Git, GitHub,
pull requests, Argo CD / Flux, repository or revision binding).

Reproduce the determinism half locally:

```bash
out="$(mktemp -d)"
for run in run-1 run-2; do
  CODEFLY_MANIFEST_DESTINATION="$out/$run" \
  CODEFLY_MANIFEST_ENVIRONMENT=manifest-guard \
  CODEFLY_MANIFEST_NAMESPACE=codefly-manifest-guard \
  CODEFLY_MANIFEST_PROFILE=KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1 \
    go test ./... -run '^TestManifestGuardRender$' -count=1
done
diff -r "$out/run-1" "$out/run-2" && echo "deterministic"
```

With `CODEFLY_MANIFEST_DESTINATION` unset the test skips, which is why ordinary
`go test ./...` runs stay usable.

A diff between the two runs means the render is non-deterministic — map
iteration order, a timestamp, or a generated name. Fix the ordering; do not
sort the output afterwards to hide it.

## 4. Nix runtime integration — if you touched `pkg/runtime` environment creation

These skip unless they can locate `core/runners/base/testdata` relative to the
package, which needs a sibling `codefly-dev/core` checkout — having `nix`
installed is not sufficient. Confirm which happened:

```bash
go test -v -count=1 ./pkg/runtime -run Nix 2>&1 | grep -E '^--- (PASS|SKIP|FAIL)'
```

If they report `SKIP`, you have not tested that path. Say so rather than
implying coverage.

## 5. Native ownership adoption — when updating Core's runner dependency

Use `codefly agent deps --pin <published-version>` for the agent, base fixtures,
and generated factory locks. Build through `codefly agent build --native-only`
into your own `CODEFLY_HOME`. Isolate the OS `HOME` too: Core's process registry
is under `HOME/.codefly/runs`, independently of the plugin path. Preserve the
ordinary Go toolchain/cache settings when isolating the home.

With the actual built agent installed, run:

```bash
SERVICE_GO_NATIVE_AGENT_HOME=/absolute/path/to/own/codefly-home \
SERVICE_GO_QUALIFICATION_CLI=/absolute/path/to/qualified/codefly \
  go test -v -count=1 -timeout 5m . -run '^TestNativeAgentOwnershipLifecycle$'
```

The gate logs binary and SBOM hashes, loads the real agent through Core's
authenticated manager, and drives Load/Init/Start/Stop over gRPC. It checks
ordinary stop and restart, kills only its disposable agent owner's recorded
birth, then uses the CLI's public preview/expect recovery against the actual
authenticated-v2 workload record. A separately launched sibling must retain
its exact birth and live owner throughout. Every registry operation uses a
temporary OS home. This proves native agent ownership and CLI recovery; it is
not full CLI composition, Docker, Nix, Linux, or published-release qualification.

## What "green" means

CI covers steps 1–3. Step 4 does not run in CI at all, so a change to the nix
environment path is unverified unless you arranged the checkout and ran it. A
PR that says "tests pass" while every gated suite skipped is the thing the
fleet standard calls a hack.
Step 5 is opt-in and must be reported separately, with the exact agent and CLI
artifacts used. Prior dependency/build results retain their original attribution.
