# Go package cleanup

Cross packaging runs the Go build in a goreleaser-cross companion. After the
build, the packager gives `Shutdown` a two-minute context independent of request
cancellation. If the build succeeded and shutdown returns a wrapped
`context.DeadlineExceeded`, the packager logs a warning naming the container and
keeps the packaged result. Other shutdown errors remain fatal. When the build
failed, any shutdown error is joined to the build error so both remain visible.

The companion's Docker lifecycle is owned by core. The pinned core v0.17.0 also
applies its own ten-second timeout to the removal request; the outer two-minute
context does not override that timeout. A removal timeout can leave a container
behind; this change preserves the artifact and reports its container name, and
does not change core's container recovery policy.

`TestPackageCrossShutdown` runs real Go builds through core's local companion and
injects only the shutdown error to exercise these outcomes without Docker.
`TestPackageRealCGO` retains the real native/cross CGO contract and requires
`SERVICE_GO_PACKAGE_TESTS=required` and Docker.
