---
name: release-go-agent
description: Release the codefly-dev/service-go agent binary with `codefly publish`, and satisfy the three contracts a release has to hold — the embedded agent.codefly.yaml version matching the tag, the CGO cross toolchain matching go.mod's Go version, and the archive name core's downloader resolves. Use when asked to release, tag, publish, or bump the version of this agent, when a published version installs but misbehaves and you suspect a version or archive mismatch, or when bumping the Go version in go.mod, which moves two independent goreleaser-cross pins.
---

# Releasing the Go agent

**`codefly publish patch` is the release.** It owns every step — the version
bump, landing it on main, the tag, and the GitHub release — and the steps are
not separable by hand. Run it from a clean, up-to-date clone of `main`:

```bash
codefly publish patch          # or minor / major / beta
codefly publish patch --dry-run   # shows the bump and tag, changes nothing
```

**Never bump `agent.codefly.yaml` by hand, and never push the tag yourself.**
The version and the tag are one fact, and `publish` is what keeps them one: a
hand bump that is tagged separately is how they drift, and the drift is
invisible until a user reports a version nobody can locate.

**Never build or upload artifacts by hand** either. The cross image supplies the
darwin and linux C compilers that CGO needs; a local `goreleaser release` or a
hand-uploaded binary is not the same artifact and will not link tree-sitter.

## What publish does here

The bump lands through a **release pull request** rather than a push to main, so
main can require its checks of everyone, admins included — a release commit
pushed straight to main carries no check results. Tags are outside branch
protection, so the tag push is direct.

Pre-flight, any failure aborting with no side effects: working tree clean, on
`main`, in sync with `origin/main`, and the tag does not already exist locally or
remotely. Pushes are never `--force`.

Because this repo's `agent.codefly.yaml` declares `release.owner: workflow` with
`release.workflow: releaser.yml`, publish additionally runs release-grade agent
CI against the bumped version and aborts untouched if it fails, then creates the
GitHub release, uploads the loader archives and SBOMs, and verifies every archive
resolves through the install URL resolver. `releaser.yml` builds every platform,
so releasing needs no darwin/arm64 host.

**If it stops between the PR merging and the tag push**, the bump is on main
without a release. **Re-run `codefly publish patch`** — it recognises the
untagged release commit and finishes it rather than bumping again.

## The walk

1. Confirm `main` is green and the core pin in `go.mod` is the one you intend to
   ship. Agent releases are usually paired with a core bump; publish does not
   check that for you.
2. From a clean clone of `main`, `codefly publish patch`.
3. Check the `releaser.yml` run and that the release carries all four archives.

`v0.0.71` was released this way: release PR #147, tag `v0.0.71` at commit
`d24bb6e`, [releaser run 39076588197](https://github.com/codefly-dev/service-go/actions/runs/39076588197).

## Three contracts a release has to hold

### The embedded version must equal the tag

`agent.codefly.yaml` is embedded into the binary (`main.go`) and is what the
agent reports about itself. Tag `v0.0.51` ships `version: 0.0.51`.

**Nothing in CI checks this** — which is why `codefly publish` owns both halves
and you do not. It bumps the manifest and tags the commit carrying that bump, so
the pair cannot drift. A hand bump, a hand tag, or re-tagging an existing
version all reintroduce exactly the mismatch nothing would catch: it releases
successfully and surfaces later as an agent reporting a version nobody can
locate.

To confirm after a release:

```bash
git show v0.0.71:agent.codefly.yaml | grep '^version:'   # 0.0.71
```

### The archive name is a contract with core's downloader

`.goreleaser.yaml` builds `service-{name}_{version}_{os}_{arch}.tar.gz`, with
the binary `service-{name}` inside. Core resolves that exact shape in
`agents/manager/downloader_url.go`. Changing `name_template`, the archive
format, or the binary name breaks installation for every user — and breaks
nothing in CI, because CI never installs the release it just built.

### CGO targets must match the Go version

Releases are CGO builds for `darwin/{amd64,arm64}` and `linux/{amd64,arm64}`,
each with its own `CC`/`CXX` in `.goreleaser.yaml`. When you change the Go
version in `go.mod`, **two independent pins move with it**:

| Pin | Used for |
| --- | --- |
| `ghcr.io/goreleaser/goreleaser-cross` in `.github/workflows/releaser.yml` | building *this agent's* release binaries |
| `ghcr.io/goreleaser/goreleaser-cross` in `pkg/builder/builder.go` | packaging *user services* at runtime |

They are set independently and neither one's drift is caught by a build. Both
are on `v1.27.3`, matching `go.mod`. Check the tag is published before moving
them — never pin one that is not. The
packaging suite covers the second:

```bash
SERVICE_GO_PACKAGE_TESTS=required \
  go test -race -count=1 -timeout 15m ./pkg/builder -run TestPackageRealCGO
```

Run it before tagging a release that moved the Go version. Dropping a target
from `.goreleaser.yaml` to make a release pass removes a platform users install
on; fix the toolchain instead.

## After the tag

Check the `releaser.yml` run and that the release carries all four archives
(`darwin/{amd64,arm64}`, `linux/{amd64,arm64}`) plus checksums and SBOMs:

```bash
gh release view vX.Y.Z --repo codefly-dev/service-go --json assets -q '.assets[].name'
```

If the job failed after publishing a partial release, delete the release and the
tag and cut the next patch — do not re-tag the same version, since consumers may
already have resolved it. That is a different situation from publish stopping
before the tag, which is finished by re-running publish.
