package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"

	gobuilder "github.com/codefly-dev/service-go/pkg/builder"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

const serviceConfig = `kind: service
name: myservice
module: mymodule
agent:
  kind: runtime::service
  name: go
  version: 0.0.1
  publisher: codefly.ai
`

// loadedBuilder writes a minimal Go service into a fresh workspace and returns
// a Builder that has been through the Load RPC, ready to drive Build.
func loadedBuilder(t *testing.T) (*gobuilder.Builder, context.Context) {
	t.Helper()
	ws := t.TempDir()
	write(t, filepath.Join(ws, "service.codefly.yaml"), serviceConfig)
	write(t, filepath.Join(ws, "code", "go.mod"), "module myservice\n\ngo 1.26\n")
	write(t, filepath.Join(ws, "code", "main.go"), "package main\n\nfunc main() {}\n")

	svc := goservice.New(agent)
	b := gobuilder.New(svc, gobuilder.BuildConfig{
		FactoryFS:     factoryFS,
		BuilderFS:     builderFS,
		DeploymentFS:  deploymentFS,
		Requirements:  requirements,
		GoVersion:     GoVersion,
		AlpineVersion: AlpineVersion,
	})

	ctx := context.Background()
	identity := &basev0.ServiceIdentity{
		Name: "myservice", Module: "mymodule", Version: "0.0.1",
		WorkspacePath: ws, RelativeToWorkspace: ".",
	}
	if _, err := b.Load(ctx, &builderv0.LoadRequest{Identity: identity}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b, ctx
}

// TestBuildEmitsRecipePlan drives the Build RPC with an output directory and
// asserts the agent renders a self-contained recipe there and returns a
// DockerBuildPlan that re-verifies against the on-disk tree.
func TestBuildEmitsRecipePlan(t *testing.T) {
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
	if resp.GetState().GetState() != builderv0.BuildStatus_SUCCESS {
		t.Fatalf("build state = %v, message=%q", resp.GetState().GetState(), resp.GetState().GetMessage())
	}

	plan := resp.GetResult().GetDockerBuildPlan()
	if plan == nil {
		t.Fatalf("expected a DockerBuildPlan, got %T", resp.GetResult().GetKind())
	}
	if err := services.VerifyDockerBuildPlan(out, plan); err != nil {
		t.Fatalf("plan does not verify against its tree: %v", err)
	}

	if len(plan.GetRecipes()) != 1 {
		t.Fatalf("expected 1 recipe, got %d", len(plan.GetRecipes()))
	}
	recipe := plan.GetRecipes()[0]
	if recipe.GetDockerfile() != "builder/Dockerfile" {
		t.Errorf("dockerfile = %q", recipe.GetDockerfile())
	}
	if recipe.GetContext() != "." {
		t.Errorf("context = %q", recipe.GetContext())
	}
	if got := recipe.GetPlatforms(); len(got) != 2 || got[0] != "linux/amd64" || got[1] != "linux/arm64" {
		t.Errorf("platforms = %v", got)
	}
	if !strings.Contains(recipe.GetImage(), "myservice") {
		t.Errorf("image = %q, want it to reference the service", recipe.GetImage())
	}

	for _, rel := range []string{"builder/Dockerfile", "builder/dockerignore", "code/go.mod", "code/main.go"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("expected %s in recipe tree: %v", rel, err)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(out, "builder", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	// The recipe declares two architectures, so the Dockerfile must build for
	// the target platform rather than a hardcoded GOARCH.
	if !strings.Contains(string(dockerfile), "TARGETARCH") {
		t.Errorf("rendered Dockerfile is not multi-arch aware:\n%s", dockerfile)
	}
}

// TestBuildRecipeSkipsSymlinks proves a symlink in the source tree is left out
// of the recipe context — the recipe inventory rejects symlinks, so copying one
// through would break plan generation.
func TestBuildRecipeSkipsSymlinks(t *testing.T) {
	b, ctx := loadedBuilder(t)
	src := b.SourceLocation
	if err := os.Symlink("go.mod", filepath.Join(src, "link.mod")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
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
	if _, err := os.Lstat(filepath.Join(out, "code", "link.mod")); !os.IsNotExist(err) {
		t.Errorf("symlink leaked into recipe tree: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
