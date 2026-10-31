package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

// Exercise the public Package RPC, real binaries and Core's attached evidence.
// The workspace links a sibling main module instead of its pinned release.
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
	write(t, filepath.Join(root, "sibling/go.mod"), "module rsc.io/quote\n\ngo 1.27.0\n")
	write(t, filepath.Join(root, "sibling/quote.go"), "package quote\nfunc Hello() string { return \"workspace sibling\" }\n")
	write(t, work, "go 1.27.0\nuse (\n./code\n./sibling\n)\n")
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
				isNative := artifact.Target.Os == runtime.GOOS && artifact.Target.Architecture == runtime.GOARCH
				if workspace && isNative {
					want = "(devel)"
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
				if isNative {
					out, err := exec.CommandContext(ctx, artifact.Path).CombinedOutput()
					require.NoError(t, err, "%s", out)
					if workspace {
						require.Equal(t, "workspace sibling\n", string(out))
					} else {
						require.NotEqual(t, "workspace sibling\n", string(out))
					}
				}
				payload, err := os.ReadFile(artifact.Path + ".cdx.json")
				if err != nil {
					t.Fatal(err)
				}
				assertPackageEvidence(t, artifact, payload, workspace && isNative)
			}
		})
	}
}

func assertPackageEvidence(t *testing.T, artifact *builderv0.PackageArtifact, payload []byte, workspace bool) {
	t.Helper()
	type component struct {
		Ref    string `json:"bom-ref"`
		Hashes []struct{ Alg, Content string }
	}
	var document struct {
		Metadata     struct{ Component component }
		Components   []component
		Dependencies []struct {
			Ref       string
			DependsOn []string
		}
	}
	require.NoError(t, json.Unmarshal(payload, &document))
	root := document.Metadata.Component
	bytes, err := os.ReadFile(artifact.Path)
	require.NoError(t, err)
	digest := fmt.Sprintf("%x", sha256.Sum256(bytes))
	require.Equal(t, digest, artifact.Sha256)
	require.Len(t, root.Hashes, 1)
	require.Equal(t, "SHA-256", root.Hashes[0].Alg)
	require.Equal(t, digest, root.Hashes[0].Content)
	refs := map[string]bool{root.Ref: true}
	for _, component := range document.Components {
		require.NotEmpty(t, component.Ref)
		require.False(t, refs[component.Ref], "duplicate component %s", component.Ref)
		refs[component.Ref] = true
	}
	edges := map[string][]string{}
	for _, dependency := range document.Dependencies {
		require.True(t, refs[dependency.Ref], "unknown graph source %s", dependency.Ref)
		for _, ref := range dependency.DependsOn {
			require.True(t, refs[ref], "unknown graph target %s", ref)
		}
		edges[dependency.Ref] = dependency.DependsOn
	}
	const source = "pkg:golang/example.test/package-evidence"
	quote := "pkg:golang/rsc.io/quote"
	if !workspace {
		quote += "@v1.5.1"
	}
	require.Contains(t, refs, source)
	require.Contains(t, refs, quote)
	require.Equal(t, []string{source}, edges[root.Ref], "executable must attach its actual source")
	require.Contains(t, edges[source], quote)
	const sampler = "pkg:golang/rsc.io/sampler@v1.3.0"
	if workspace {
		require.NotContains(t, refs, quote+"@v1.5.1")
		require.NotContains(t, edges[quote], sampler, "local sibling has no released-version requirements")
	} else {
		require.NotContains(t, refs, strings.TrimSuffix(quote, "@v1.5.1"))
		require.Contains(t, edges[quote], sampler)
	}
}
