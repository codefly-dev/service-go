package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/stretchr/testify/require"
)

// TestServiceRecipeDeclaresItsGoModuleDownload pins the declaration the CLI
// fetches from: the recipe names the module root inside its own context and the
// build context the modules arrive in, so the plan carries the module-downloads
// contract and still verifies. Without the declaration the CLI prefetches
// nothing and the build has to reach the network for a private module with no
// credential — which is how a render of this agent's services failed.
func TestServiceRecipeDeclaresItsGoModuleDownload(t *testing.T) {
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
	require.NoError(t, err)
	plan := resp.GetResult().GetDockerBuildPlan()
	require.NotNil(t, plan, "state %v message %q", resp.GetState().GetState(), resp.GetState().GetMessage())

	require.NoError(t, services.VerifyDockerBuildPlan(out, plan))
	require.Equal(t, services.DockerBuildRecipeGoModulesContractVersion, plan.GetContractVersion())

	downloads := plan.GetRecipes()[0].GetGoModuleDownloads()
	require.Len(t, downloads, 1)
	require.Equal(t, "code", downloads[0].GetModuleRoot(),
		"the declared root must be where the recipe copies the module, or the caller fetches the wrong tree")
	require.Equal(t, "gomodproxy", downloads[0].GetProxyContext())

	// The root the declaration names has to be a real module in the emitted
	// recipe, not a path that merely reads well.
	_, err = os.Stat(filepath.Join(out, downloads[0].GetModuleRoot(), "go.mod"))
	require.NoError(t, err, "the declared module root carries no go.mod")
}

// TestTemplateReadsTheDeclaredProxy pins the template side of the same
// contract: a stage named after the declared context, declared before the
// builder stage, read as the only module source when the caller supplied one,
// and never named by the runtime stage.
func TestTemplateReadsTheDeclaredProxy(t *testing.T) {
	template, err := builderFS.ReadFile("templates/builder/Dockerfile.tmpl")
	require.NoError(t, err)
	source := string(template)

	require.Contains(t, source, "FROM scratch AS gomodproxy")
	require.Less(t, strings.Index(source, "FROM scratch AS gomodproxy"), strings.Index(source, "AS builder"),
		"the proxy stage must be declared before the stage that mounts it")

	builderStage, runtimeStage, found := strings.Cut(source, "# Final stage")
	require.True(t, found)
	require.Contains(t, builderStage, "--mount=type=bind,from=gomodproxy,target=/gomodproxy")
	require.Contains(t, builderStage, "GOPROXY=file:///gomodproxy")
	require.Contains(t, builderStage, "GOSUMDB=off")
	require.Contains(t, builderStage, "GOPRIVATE=")
	require.Contains(t, builderStage, "go mod verify",
		"go.sum alone verifies a module read from the proxy, so the recipe must verify it")
	require.NotContains(t, runtimeStage, "gomodproxy")
}
