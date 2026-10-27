package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
)

// netrcMount is the BuildKit mount the CLI builds against: the secret is named
// "netrc", it lands where git and the go tool look for credentials, and it is
// optional so a build with no private module needs no secret at all.
const netrcMount = "--mount=type=secret,id=netrc,target=/root/.netrc,required=false"

// proxyMount is the build context the recipe declares its module download
// against: the caller fetches the module graph on the host and supplies it
// here, so the download needs no credential at all.
const proxyMount = "--mount=type=bind,from=gomodproxy,target=/gomodproxy"

// TestDockerfileFetchesPrivateModulesThroughAnOptionalSecret holds the contract
// the CLI builds against for private Go modules: every dependency download runs
// with the "netrc" BuildKit secret mounted at /root/.netrc, where both git and
// the go tool read credentials, and GOPRIVATE is a build argument declared
// before that step so the toolchain sees the host's value. The mount is
// optional so a build without private modules — and any consumer rebuilding the
// vendored recipe without a secret — renders and builds the same Dockerfile.
// The credential must never reach an image: no ENV, no COPY, and nothing in the
// runtime stage may name it. Without this, `go mod download` in the builder
// stage has no way to authenticate and a service that imports a private module
// fails with "could not read Username for 'https://github.com'".
func TestDockerfileFetchesPrivateModulesThroughAnOptionalSecret(t *testing.T) {
	// Judge the recipe the agent actually emits, not the template alone: a
	// rendering that dropped the mount would still leave the source correct.
	b, ctx := loadedBuilder(t)
	out := filepath.Join(t.TempDir(), "recipe")
	resp, err := b.Build(ctx, &builderv0.BuildRequest{
		OutputDirectory: out,
		BuildContext: &builderv0.BuildContext{
			Kind: &builderv0.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv0.DockerBuildContext{DockerRepository: "registry.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if resp.GetResult().GetDockerBuildPlan() == nil {
		t.Fatalf("expected a plan, got state %v message %q",
			resp.GetState().GetState(), resp.GetState().GetMessage())
	}
	contents, err := os.ReadFile(filepath.Join(out, "builder", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	rendered := string(contents)

	builderStage, runtimeStage, found := strings.Cut(rendered, "# Final stage")
	if !found {
		t.Fatalf("runtime stage marker is missing:\n%s", rendered)
	}

	// GOPRIVATE is declared as a build argument before the download so the RUN
	// inherits it as an environment variable; it must not be an ENV, which
	// would persist a host setting into the stage configuration.
	if !strings.Contains(builderStage, "\nARG GOPRIVATE\n") {
		t.Errorf("builder stage does not declare ARG GOPRIVATE:\n%s", builderStage)
	}
	if strings.Contains(rendered, "ENV GOPRIVATE") {
		t.Error("GOPRIVATE is an ENV; it must stay a build argument")
	}
	if arg, download := strings.Index(builderStage, "ARG GOPRIVATE"), strings.Index(builderStage, "go mod download"); arg < 0 || download < 0 || arg > download {
		t.Errorf("ARG GOPRIVATE (%d) must precede go mod download (%d)", arg, download)
	}

	// Every dependency download mounts the secret. A RUN spans continuation
	// lines, so judge whole instructions rather than raw lines.
	downloads := 0
	for _, instruction := range strings.Split(strings.ReplaceAll(builderStage, "\\\n", " "), "\n") {
		if !strings.Contains(instruction, "go mod download") {
			continue
		}
		downloads++
		// The declared download reads the caller's prefetched modules first
		// (the gomodproxy bind), and keeps the optional netrc secret for a
		// caller that supplies no proxy and still passes a credential. Both
		// mounts belong to the same RUN, in that order.
		if mounts := strings.Join(strings.Fields(instruction), " "); !strings.HasPrefix(mounts, "RUN "+proxyMount+" "+netrcMount+" ") {
			t.Errorf("dependency download must read the declared proxy and keep the optional netrc secret: %q", instruction)
		}
	}
	if downloads == 0 {
		t.Error("rendered recipe downloads no dependencies")
	}

	// The credential is a mount, never image content. A RUN spans continuation
	// lines, so a mount may be the RUN's own line or one of its continuations;
	// what must never happen is the credential appearing as ENV, COPY or ARG.
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(line, "netrc") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		mount := strings.HasPrefix(trimmed, "RUN ") || strings.HasPrefix(trimmed, "--mount=")
		if !mount && !strings.HasPrefix(trimmed, "#") {
			t.Errorf("netrc may only appear on a RUN mount or a comment: %q", line)
		}
	}
	if strings.Contains(runtimeStage, "netrc") {
		t.Errorf("runtime stage names the credential:\n%s", runtimeStage)
	}
	if strings.Contains(runtimeStage, "GOPRIVATE") {
		t.Errorf("runtime stage names GOPRIVATE:\n%s", runtimeStage)
	}
}

// renderedRecipe drives Build the way the CLI does and returns the Dockerfile
// the agent actually emitted. The template alone is not the contract: a
// rendering that dropped a line would still leave the source looking right.
func renderedRecipe(t *testing.T) string {
	t.Helper()
	b, ctx := loadedBuilder(t)
	out := filepath.Join(t.TempDir(), "recipe")
	resp, err := b.Build(ctx, &builderv0.BuildRequest{
		OutputDirectory: out,
		BuildContext: &builderv0.BuildContext{
			Kind: &builderv0.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv0.DockerBuildContext{DockerRepository: "registry.example.com"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if resp.GetResult().GetDockerBuildPlan() == nil {
		t.Fatalf("expected a plan, got state %v message %q",
			resp.GetState().GetState(), resp.GetState().GetMessage())
	}
	contents, err := os.ReadFile(filepath.Join(out, "builder", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	return string(contents)
}

// TestRuntimeStageInstallsNoOSPackages holds the half of the input set that is
// genuinely reproducible rather than merely recorded. Every FROM in the recipe
// is pinned by digest, so the package set of a stage that installs nothing is
// fixed by that digest alone. The runtime stage qualifies: alpine already ships
// ca-certificates-bundle, which owns /etc/ssl/certs/ca-certificates.crt — the
// first file crypto/x509 reads on Linux — so a static binary needs no apk at
// all. Reintroducing one would put an input back into the shipped image that
// resolves against whatever the Alpine index happens to offer that day, which
// is exactly the drift the digest pin exists to close.
func TestRuntimeStageInstallsNoOSPackages(t *testing.T) {
	rendered := renderedRecipe(t)
	_, runtimeStage, found := strings.Cut(rendered, "# Final stage")
	if !found {
		t.Fatalf("runtime stage marker is missing:\n%s", rendered)
	}
	for _, instruction := range strings.Split(strings.ReplaceAll(runtimeStage, "\\\n", " "), "\n") {
		if !strings.HasPrefix(instruction, "RUN ") {
			continue
		}
		if strings.Contains(instruction, "apk add") || strings.Contains(instruction, "apk upgrade") {
			t.Errorf("runtime stage installs OS packages, so its input set is no longer fixed by the base digest: %q", instruction)
		}
	}
}

// TestBuilderStagePackagesAreRecordedAsEvidence covers the half that cannot be
// pinned. The builder stage needs git, which the golang base does not carry,
// and Alpine's index serves only the current -rN of each package: `git=2.51.0-r0`
// stops resolving the day 3.24 ships a patch, so a version pin would make every
// rebuild of an already-published recipe fail. The resolved set is recorded
// instead and carried into the final image, so the OS-package inputs that
// produced an artifact are readable from that artifact. Without this, an
// `apk add` could be added back with nothing recording what it pulled in.
func TestBuilderStagePackagesAreRecordedAsEvidence(t *testing.T) {
	rendered := renderedRecipe(t)
	builderStage, runtimeStage, found := strings.Cut(rendered, "# Final stage")
	if !found {
		t.Fatalf("runtime stage marker is missing:\n%s", rendered)
	}
	if !strings.Contains(builderStage, "apk info -v") {
		t.Errorf("builder stage installs packages without recording the resolved set:\n%s", builderStage)
	}
	// The record has to reach the artifact. Evidence that dies with the
	// discarded builder stage answers nobody's question about a published image.
	const evidence = "/etc/codefly/build-inputs/"
	if !strings.Contains(runtimeStage, "COPY --from=builder") || !strings.Contains(runtimeStage, evidence) {
		t.Errorf("builder-stage evidence never reaches the final image at %s:\n%s", evidence, runtimeStage)
	}
	for _, record := range []string{"/apk-builder.txt", "/go-build-info.txt", "apk-runtime.txt"} {
		if !strings.Contains(rendered, record) {
			t.Errorf("recipe records no %s:\n%s", record, rendered)
		}
	}
}

// builderGoBase matches the builder stage's Go base: the exact tag and the
// image-index digest it is pinned to, as one reference.
var builderGoBase = regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\S+\s+)?(golang:(\S+)@sha256:[0-9a-f]{64})\b`)

// runtimeImageGoBase matches the Go base the runtime image is built from, as
// pkg/runtime/runtime-image/Dockerfile declares it.
var runtimeImageGoBase = regexp.MustCompile(`(?m)^ARG GO_IMAGE=(\S+)`)

// TestBuilderStageBuildsWithTheRuntimeImageGoBase holds the one Go base this
// repo builds a service with, in both places it is written down.
//
// The official golang image sets GOTOOLCHAIN=local, and the declared module
// download makes the caller's prefetched modules the only module source, so
// the Go in the builder base is the only Go available: a service whose go.mod
// requires a newer patch is refused with "go.mod requires go >= x.y.z
// (running x.y.z-1; GOTOOLCHAIN=local)" and the image never builds. That makes
// the base a contract with every service the agent builds, not an
// implementation detail.
//
// Two things are required of it. The tag must name the patch, so which Go a
// digest holds is readable without pulling it — a floating
// `golang:1.27-alpine` pin sat on 1.27.1 while the fleet's modules required
// `go 1.27.3`, and the drift was invisible until a hosted render failed. And
// the reference must be the SAME reference pkg/runtime/runtime-image/Dockerfile
// builds the runtime container from, because a module that builds under
// `codefly run` has to build in the deployed recipe too.
//
// The whole reference is compared, not the Go patch alone. Tag and digest are
// one fact here: two references agreeing on `1.27.3` while differing on the
// Alpine variant or on the bytes behind the digest are two different images,
// and a guard that held only the patch would let them drift apart in every
// other field — which is how this pin went stale in the first place.
func TestBuilderStageBuildsWithTheRuntimeImageGoBase(t *testing.T) {
	rendered := renderedRecipe(t)
	builderStage, _, found := strings.Cut(rendered, "# Final stage")
	if !found {
		t.Fatalf("runtime stage marker is missing:\n%s", rendered)
	}
	pin := builderGoBase.FindStringSubmatch(builderStage)
	if pin == nil {
		t.Fatalf("builder stage pins no golang base by tag and digest:\n%s", builderStage)
	}
	reference, tag := pin[1], pin[2]

	release, _, qualified := strings.Cut(tag, "-")
	if !qualified {
		t.Fatalf("golang tag %q names no base variant", tag)
	}
	if strings.Count(release, ".") != 2 {
		t.Errorf("golang tag %q names Go %q, a floating minor: the tag must name the patch release, "+
			"or which Go the digest holds is readable only from a failed build", tag, release)
	}

	// The runtime image's own Dockerfile, not runtime-image.json: the lock
	// addresses codefly's derived image (ghcr.io/codefly-dev/service-go-runtime),
	// one level up. The Go base both files must agree on is this ARG.
	const runtimeDockerfile = "pkg/runtime/runtime-image/Dockerfile"
	content, err := os.ReadFile(filepath.FromSlash(runtimeDockerfile))
	if err != nil {
		t.Fatalf("read %s: %v", runtimeDockerfile, err)
	}
	declared := runtimeImageGoBase.FindStringSubmatch(string(content))
	if declared == nil {
		t.Fatalf("%s declares no ARG GO_IMAGE", runtimeDockerfile)
	}
	if reference != declared[1] {
		t.Errorf("builder stage builds from %s but %s builds from %s: a service must build the same way "+
			"in a container run and in the recipe, so both move together",
			reference, runtimeDockerfile, declared[1])
	}
}

// TestTheGoReleaseIsOneFactAcrossTheRepo closes the axis the reference
// comparison above does not reach. Four places name the Go this repo builds
// with, and nothing made them one fact: `go.mod`'s directive (which selects
// the toolchain the agent itself is built by, and which both goreleaser-cross
// pins follow), the builder base and the runtime image's `ARG GO_IMAGE`
// (compared to each other above), and two smoke assertions that grep the
// runtime container's `go version` for a literal.
//
// They are aligned today, at 1.27.3, which is the cheapest moment to assert
// it. Without this, bumping `go.mod` alone — the normal way a Go bump starts,
// and how GO-2026-6809 was fixed — moves the two cross pins and silently
// leaves the images and both literals behind, which reads as fixed and is not.
func TestTheGoReleaseIsOneFactAcrossTheRepo(t *testing.T) {
	directive := goDirective(t)

	rendered := renderedRecipe(t)
	builderStage, _, _ := strings.Cut(rendered, "# Final stage")
	pin := builderGoBase.FindStringSubmatch(builderStage)
	if pin == nil {
		t.Fatalf("builder stage pins no golang base by tag and digest:\n%s", builderStage)
	}
	if release, _, _ := strings.Cut(pin[2], "-"); release != directive {
		t.Errorf("go.mod requires Go %s but the builder base is golang:%s: a service the agent builds "+
			"must not be compiled by an older Go than this repo itself requires", directive, pin[2])
	}

	// The smoke assertions prove the published runtime container really runs
	// the Go it claims. A literal that drifts stops proving anything while
	// still passing, because it greps for what the image happens to be.
	for _, workflow := range []string{"ci.yml", "runtime-image.yml"} {
		path := filepath.Join(".github", "workflows", workflow)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want := "go" + directive
		assertion := regexp.MustCompile(`(?m)^[\t ]*docker run[^\n]+ go version[\t ]*\|[\t ]*grep -F ['"]` + regexp.QuoteMeta(want) + `['"][\t ]*$`)
		if !assertion.Match(content) {
			t.Errorf("%s asserts no %q: its go version smoke check names a different release than go.mod requires",
				path, want)
		}
	}
}

// goDirective is the Go release this module requires, from the one place that
// declares it. CI reads the same file with go-version-file, so the directive is
// never written down twice.
func goDirective(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	directive := regexp.MustCompile(`(?m)^go (\S+)$`).FindStringSubmatch(string(content))
	if directive == nil {
		t.Fatal("go.mod declares no go directive")
	}
	return directive[1]
}
