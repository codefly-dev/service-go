package main

import (
	"context"
	"os"
	"testing"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	golanghelpers "github.com/codefly-dev/core/runners/golang"
	"github.com/codefly-dev/core/wool"
)

// TestManifestGuardRender renders the plugin's deployment templates through the
// production manifest path (Builder.Deploy → DeployGoKubernetes) so the reusable
// plugin-manifest-guard workflow can verify the emitted bundle. The guard runs
// it twice against the same CODEFLY_MANIFEST_DESTINATION-driven inputs and
// requires byte-for-byte identical trees. It skips when the destination is unset
// so ordinary `go test ./...` runs stay usable.
func TestManifestGuardRender(t *testing.T) {
	destination := os.Getenv("CODEFLY_MANIFEST_DESTINATION")
	if destination == "" {
		t.Skip("CODEFLY_MANIFEST_DESTINATION not set")
	}
	profileName := os.Getenv("CODEFLY_MANIFEST_PROFILE")
	profileValue, ok := builderv0.KubernetesOutputProfile_value[profileName]
	if !ok || builderv0.KubernetesOutputProfile(profileValue) == builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_UNSPECIFIED {
		t.Fatalf("unknown CODEFLY_MANIFEST_PROFILE %q", profileName)
	}
	profile := builderv0.KubernetesOutputProfile(profileValue)

	ctx := context.Background()
	protoIdentity := &basev0.ServiceIdentity{Workspace: "workspace", Module: "module", Name: "example-service", Version: "1.2.3"}
	base := &services.Base{Wool: wool.Get(ctx)}
	if err := base.HeadlessLoad(ctx, protoIdentity); err != nil {
		t.Fatalf("headless load: %v", err)
	}
	base.Information = &services.Information{
		Service: resources.ToServiceWithCase(base.Identity),
		Module:  resources.ToModuleWithCase(base.Identity),
	}
	// Digest-pinned so the restricted/portable contract's image-pinning check
	// passes; the guard always renders that profile.
	base.SetDockerImage(&resources.DockerImage{
		Name:   "example/service",
		Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	builder := &services.BuilderWrapper{Base: base}
	base.Builder = builder
	manager := base.EnvironmentVariables
	manager.SetIdentity(protoIdentity)

	req := &builderv0.DeploymentRequest{
		Environment: &basev0.Environment{Name: os.Getenv("CODEFLY_MANIFEST_ENVIRONMENT")},
		Deployment: &builderv0.Deployment{Kind: &builderv0.Deployment_Kubernetes{
			Kubernetes: &builderv0.KubernetesDeployment{
				Namespace:   os.Getenv("CODEFLY_MANIFEST_NAMESPACE"),
				Destination: destination,
				Profile:     profile,
			},
		}},
	}

	response, err := golanghelpers.DeployGoKubernetes(ctx, builder, req, manager, deploymentFS)
	if err != nil {
		t.Fatalf("render manifest bundle: %v", err)
	}
	if state := response.GetState().GetState(); state != builderv0.DeploymentStatus_SUCCESS {
		t.Fatalf("deployment state = %s, want SUCCESS: %s", state, response.GetState().GetMessage())
	}
}
