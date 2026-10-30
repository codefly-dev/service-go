package main

import (
	"debug/buildinfo"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise the public Package RPC, real binaries and Core's attached evidence.
// The workspace selects a released dependency different from the pinned graph.
func TestPackageEvidenceFollowsEachTargetGraph(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "code")
	write(t, filepath.Join(root, "service.codefly.yaml"), serviceConfig)
	write(t, filepath.Join(source, "go.mod"), "module example.test/package-evidence\n\ngo 1.27.0\n\nrequire rsc.io/quote v1.5.1\n")
	write(t, filepath.Join(source, "main.go"), "package main\n/* static int answer(void) { return 42; } */\nimport \"C\"\nimport (\"fmt\"; \"rsc.io/quote\")\nfunc main() { if C.answer()!=42 { panic(\"C compiler\") }; fmt.Println(quote.Hello()) }\n")
	prepare := exec.Command("go", "mod", "tidy")
	prepare.Dir = source
	prepare.Env = append(os.Environ(), "GOWORK=off")
	if out, err := prepare.CombinedOutput(); err != nil {
		t.Fatalf("prepare pinned graph: %v %s", err, out)
	}
	work := filepath.Join(root, "go.work")
	write(t, work, "go 1.27.0\nuse ./code\nreplace rsc.io/quote v1.5.1 => rsc.io/quote v1.5.2\n")
	for _, workspace := range []bool{false, true} {
		name := "pinned"
		if workspace {
			name = "prepared-workspace"
		}
		t.Run(name, func(t *testing.T) {
			mode := "off"
			if workspace {
				mode = work
			}
			t.Setenv("GOWORK", mode)
			b, ctx := loadBuilderAt(t, root)
			b.Settings.WithWorkspace = !workspace // not Package's graph selector
			targets := []*builderv0.PackageTarget{{Os: runtime.GOOS, Architecture: runtime.GOARCH}}
			if os.Getenv("SERVICE_GO_PACKAGE_TESTS") == "required" && (runtime.GOOS != "linux" || runtime.GOARCH != "amd64") {
				targets = append(targets, &builderv0.PackageTarget{Os: "linux", Architecture: "amd64"})
			}
			response, err := grpcBuilderClient(t, b).Package(ctx, &builderv0.PackageRequest{OutputDirectory: t.TempDir(), ArtifactName: "evidence", Targets: targets, IncludeSbom: true})
			if err != nil || response.GetState().GetState() != builderv0.PackageStatus_SUCCESS {
				t.Fatalf("Package: %v %v", response, err)
			}
			if len(response.Artifacts) != 2*len(targets) {
				t.Fatalf("artifacts: %v", response.Artifacts)
			}
			for _, artifact := range response.Artifacts {
				if artifact.Kind != builderv0.PackageArtifact_EXECUTABLE {
					continue
				}
				want := "v1.5.1"
				if workspace && artifact.Target.Os == runtime.GOOS && artifact.Target.Architecture == runtime.GOARCH {
					want = "v1.5.2"
				}
				info, err := buildinfo.ReadFile(artifact.Path)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, dep := range info.Deps {
					if dep.Path == "rsc.io/quote" {
						version := dep.Version
						if dep.Replace != nil {
							version = dep.Replace.Version
						}
						if version != want {
							t.Fatalf("binary dependency %s want %s", version, want)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("binary omitted fixture dependency")
				}
				payload, err := os.ReadFile(artifact.Path + ".cdx.json")
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(payload), "pkg:golang/rsc.io/quote@"+want) || !strings.Contains(string(payload), artifact.Sha256) {
					t.Fatalf("SBOM did not bind selected dependency and executable: %s", payload)
				}
			}
		})
	}
}
