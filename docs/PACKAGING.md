# Go package cleanup

Cross packaging runs the Go build in a goreleaser-cross companion. After the
build, the packager gives `Shutdown` a two-minute context independent of request
cancellation. If the build succeeded and shutdown returns a wrapped
`context.DeadlineExceeded`, the packager logs a warning naming the container and
keeps the packaged result. Other shutdown errors remain fatal. When the build
failed, any shutdown error is joined to the build error so both remain visible.

The companion's Docker lifecycle is owned by core. The pinned Core also
applies its own ten-second timeout to the removal request; the outer two-minute
context does not override that timeout. A removal timeout can leave a container
behind; this change preserves the artifact and reports its container name, and
does not change core's container recovery policy.

`TestPackageCrossShutdown` runs real Go builds through core's local companion and
injects only the shutdown error to exercise these outcomes without Docker.
`TestPackageRealCGO` retains the real native/cross CGO contract and requires
`SERVICE_GO_PACKAGE_TESTS=required` and Docker.

## Graph identity in package evidence

Each executable receives its own SBOM using Core's existing inventory and
artifact-binding operations. A native target honors the CLI's prepared GOWORK;
without one it uses the pinned module graph. Cross targets use GOWORK=off for
source discovery, toolchain selection, build and inventory because their
companion mounts the source module, not host workspace siblings. The service's
with-workspace setting is not Package's graph selector. A single mixed-target
request may therefore produce different dependency graphs, each bound to its
own executable digest. The public Package regression links a workspace sibling,
checks the actual source attachment and all component references, and compares
the selected graph with dependency versions embedded in real CGO executables.

Workspace SBOM qualification requires the Core workspace-root correction. The
current released pin, Core v0.19.10, omits other workspace main modules and can
attach an executable to the wrong source. The strengthened regression is qualified
with an uncommitted local Core workspace override while that owner fix awaits a
release. This does not qualify a clean install from the current pin. Adopt the
released correction through `codefly agent deps --pin`, then rerun standalone,
generated-service and public native/cross packaging checks before release.

Core v0.19.10 includes the Linux process-identity correction. Native ownership
records use authenticated-v2; older agents/CLIs that only know authenticated-v1
do not reap those records. Use compatible CLI recovery tooling; an installation
key alone is not evidence that a locally built binary is the published release.
The ownership gate retains the launch birth and requires it before signaling,
then verifies the recovery receipt and preservation of an independent sibling.
