package builder

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/runners/companion"
	"github.com/codefly-dev/core/runners/dockerrun"
	"github.com/codefly-dev/core/runners/recoveryscope"
)

// A workspace whose main module needs a sibling that only go.work can reach:
// go.mod requires it with no replace and no go.sum entry, so GOWORK=off cannot
// resolve it and the go.work's `use` is the only way it links.
func writeGoWorkFixture(t *testing.T) (source, goWork string) {
	t.Helper()
	// Go compares the module containing the cwd with go.work's `use` paths as
	// strings; a TempDir under a symlink (macOS /var → /private/var) would make
	// the same directory spell two ways and fail "not one of the workspace
	// modules" for a reason that has nothing to do with the build.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(root, "agent")
	sibling := filepath.Join(root, "sibling")
	for _, dir := range []string{source, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(sibling, "go.mod"), "module example.test/sibling\n\ngo 1.27.0\n", 0600)
	mustWrite(t, filepath.Join(sibling, "sibling.go"), "package sibling\nconst Value = \"from-workspace\"\n", 0600)
	mustWrite(t, filepath.Join(source, "go.mod"), "module example.test/agent\n\ngo 1.27.0\n\nrequire example.test/sibling v0.0.0\n", 0600)
	mustWrite(t, filepath.Join(source, "main.go"), "package main\nimport (\"fmt\"; \"example.test/sibling\")\nfunc main() { fmt.Println(sibling.Value) }\n", 0600)
	goWork = filepath.Join(root, "go.work")
	mustWrite(t, goWork, "go 1.27.0\n\nuse (\n\t./agent\n\t./sibling\n)\n", 0600)
	return source, goWork
}

func TestGoWorkModeDefaultsToOffWhenNothingWasPrepared(t *testing.T) {
	t.Setenv("GOWORK", "")
	if got := goWorkMode(); got != "off" {
		t.Fatalf("unprepared build must use the pinned graph, got GOWORK=%q", got)
	}
	t.Setenv("GOWORK", "off")
	if got := goWorkMode(); got != "off" {
		t.Fatalf("standalone CI's GOWORK=off must be kept, got %q", got)
	}
	t.Setenv("GOWORK", "  /tmp/some/go.work ")
	if got := goWorkMode(); got != "/tmp/some/go.work" {
		t.Fatalf("a prepared go.work must be honoured, got %q", got)
	}
}

// The CLI binds the go.work it detected for a local build; the agent must link
// what that workspace selects, not the pinned graph.
func TestPackageHonoursThePreparedGoWork(t *testing.T) {
	source, goWork := writeGoWorkFixture(t)
	t.Setenv("GOWORK", goWork)
	destination := filepath.Join(t.TempDir(), "agent")
	target := &builderv0.PackageTarget{Os: runtime.GOOS, Architecture: runtime.GOARCH}
	if err := packageGoBinary(t.Context(), source, destination, target, goWorkMode()); err != nil {
		t.Fatalf("a build the CLI prepared with a go.work must link the workspace: %v", err)
	}
	output, err := exec.CommandContext(t.Context(), destination).CombinedOutput()
	if err != nil || string(output) != "from-workspace\n" {
		t.Fatalf("executable must carry the workspace's sibling: %v, output=%q", err, output)
	}
}

// With nothing prepared — or standalone CI's explicit off — the same source is
// refused: the graph is go.mod/go.sum alone, and the sibling is not in it.
func TestPackageWithoutAPreparedGoWorkIsThePinnedGraph(t *testing.T) {
	source, _ := writeGoWorkFixture(t)
	target := &builderv0.PackageTarget{Os: runtime.GOOS, Architecture: runtime.GOARCH}
	for name, mode := range map[string]string{"unset": "", "off": "off"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GOWORK", mode)
			destination := filepath.Join(t.TempDir(), "agent")
			err := packageGoBinary(t.Context(), source, destination, target, goWorkMode())
			if err == nil || !strings.Contains(err.Error(), "example.test/sibling") {
				t.Fatalf("pinned graph must refuse the workspace-only sibling, got: %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("refused package was installed: %v", err)
			}
		})
	}
}

// A service that declares with-workspace and was handed no GOWORK is packaged
// from the go.work Go itself finds for it — the graph its SBOM, in the same
// Package call, already describes. When no go.work governs the source the
// declaration cannot be honoured, and packaging the pinned graph in its place
// would ship a different program under the same name: refuse, naming both.
func TestPackageGoWorkSelection(t *testing.T) {
	source, goWork := writeGoWorkFixture(t)
	standalone, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(standalone, "go.mod"), "module example.test/standalone\n\ngo 1.27.0\n", 0600)
	for _, tc := range []struct {
		name, prepared, source string
		withWorkspace          bool
		want, wantErr          string
	}{
		{name: "prepared go.work", prepared: "/tmp/some/go.work", source: source, want: "/tmp/some/go.work"},
		{name: "prepared off overrides the declaration", prepared: "off", source: source, withWorkspace: true, want: "off"},
		{name: "nothing prepared, nothing declared", source: source, want: "off"},
		{name: "declared and discovered", source: source, withWorkspace: true, want: goWork},
		{name: "declared but no go.work governs the source", source: standalone, withWorkspace: true, wantErr: "with-workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOWORK", tc.prepared)
			got, err := packageGoWork(t.Context(), tc.source, tc.withWorkspace)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), tc.source) {
					t.Fatalf("an unhonourable workspace must be refused naming %q and %s, got %q, %v", tc.wantErr, tc.source, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("GOWORK = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// packageMountRecorder notes what the cross build asks its toolchain to see.
// Everything else is core's real companion.
type packageMountRecorder struct {
	companion.CompanionRunner
	mounts map[string]string
}

func (r *packageMountRecorder) WithMount(hostPath, targetPath string) {
	r.mounts[hostPath] = targetPath
	r.CompanionRunner.WithMount(hostPath, targetPath)
}

// The cross build runs in a toolchain container. A workspace it cannot see is
// a workspace it cannot honour, so the go.work and every member outside the
// packaged module are made visible at their own paths and GOWORK selects them
// — the container artifact carries the same sibling as the native one.
func TestPackageCrossHonoursThePreparedGoWork(t *testing.T) {
	source, goWork := writeGoWorkFixture(t)
	destination := filepath.Join(t.TempDir(), "agent")
	recorder := &packageMountRecorder{mounts: map[string]string{}}
	factory := func(ctx context.Context, opts companion.CompanionOpts) (companion.CompanionRunner, error) {
		// The host is the toolchain here; TestPackageRealCrossGoWork is Docker.
		opts.PreferredBackend = companion.BackendLocal
		runner, err := companion.NewCompanionRunner(ctx, opts)
		recorder.CompanionRunner = runner
		return recorder, err
	}
	target := &builderv0.PackageTarget{Os: runtime.GOOS, Architecture: runtime.GOARCH}
	if err := packageCrossGoBinaryWithRunner(t.Context(), source, ".", destination, target, goWork, factory); err != nil {
		t.Fatalf("a cross build prepared with a go.work must link the workspace: %v", err)
	}
	output, err := exec.CommandContext(t.Context(), destination).CombinedOutput()
	if err != nil || string(output) != "from-workspace\n" {
		t.Fatalf("executable must carry the workspace's sibling: %v, output=%q", err, output)
	}
	for _, path := range []string{goWork, filepath.Join(filepath.Dir(source), "sibling")} {
		if recorder.mounts[path] != path {
			t.Errorf("%s is not visible to the toolchain at its own path: mounts %v", path, recorder.mounts)
		}
	}
	if _, twice := recorder.mounts[source]; twice {
		t.Errorf("the packaged module is the toolchain's source already: mounts %v", recorder.mounts)
	}
}

// A member the workspace names but the checkout does not hold cannot be
// linked. Say so; never package what go.mod pins in its place.
func TestPackageCrossRefusesAWorkspaceItCannotLoad(t *testing.T) {
	source, goWork := writeGoWorkFixture(t)
	if err := os.RemoveAll(filepath.Join(filepath.Dir(source), "sibling")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "agent")
	started := false
	factory := func(ctx context.Context, opts companion.CompanionOpts) (companion.CompanionRunner, error) {
		started = true
		opts.PreferredBackend = companion.BackendLocal
		return companion.NewCompanionRunner(ctx, opts)
	}
	target := &builderv0.PackageTarget{Os: runtime.GOOS, Architecture: runtime.GOARCH}
	err := packageCrossGoBinaryWithRunner(t.Context(), source, ".", destination, target, goWork, factory)
	if err == nil || !strings.Contains(err.Error(), "sibling") || !strings.Contains(err.Error(), goWork) {
		t.Fatalf("an unloadable workspace must be refused naming it and its member, got: %v", err)
	}
	if started {
		t.Fatal("a toolchain was started for a workspace that cannot be loaded")
	}
}

// The mounts are the part only Docker can prove: the toolchain container sees
// nothing of the host but what it is given.
func TestPackageRealCrossGoWork(t *testing.T) {
	if os.Getenv("SERVICE_GO_PACKAGE_TESTS") != "required" {
		t.Skip("set SERVICE_GO_PACKAGE_TESTS=required for real native/cross packaging")
	}
	t.Setenv("GOTOOLCHAIN", "auto")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput(); err != nil {
		t.Fatalf("packaging requires Docker: %v: %s", err, output)
	}
	source, goWork := writeGoWorkFixture(t)
	root := filepath.Dir(source)
	scope, err := dockerrun.NewContainerRecoveryScope(root, root, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(recoveryscope.EnvironmentVariable, os.Getenv(recoveryscope.EnvironmentVariable))
	if err := dockerrun.SetContainerRecoveryScope(scope); err != nil {
		t.Fatal(err)
	}
	target := &builderv0.PackageTarget{Os: "linux", Architecture: "amd64"}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		target = &builderv0.PackageTarget{Os: "linux", Architecture: "arm64"}
	}
	destination := filepath.Join(root, "out", "agent")
	if err := os.Mkdir(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := packageGoBinary(ctx, source, destination, target, goWork); err != nil {
		t.Fatalf("a cross build prepared with a go.work must link the workspace: %v", err)
	}
	payload, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("from-workspace")) {
		t.Fatal("cross executable does not carry the workspace's sibling")
	}
	// The same source, pinned: the sibling is not in go.mod's graph.
	err = packageGoBinary(ctx, source, filepath.Join(root, "out", "pinned"), target, "off")
	if err == nil || !strings.Contains(err.Error(), "example.test/sibling") {
		t.Fatalf("pinned graph must refuse the workspace-only sibling, got: %v", err)
	}
}
