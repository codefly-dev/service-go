package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
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
	if err := packageGoBinary(t.Context(), source, destination, target); err != nil {
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
			err := packageGoBinary(t.Context(), source, destination, target)
			if err == nil || !strings.Contains(err.Error(), "example.test/sibling") {
				t.Fatalf("pinned graph must refuse the workspace-only sibling, got: %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("refused package was installed: %v", err)
			}
		})
	}
}
