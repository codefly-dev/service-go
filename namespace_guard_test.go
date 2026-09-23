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
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/wool"
)

// A restricted (promotable) render is committed to a GitOps repository and
// reconciled by Argo CD into a cell whose AppProject forbids cluster-scoped
// resources — `clusterResourceWhitelist: []`. A Namespace in that tree makes
// every sync of the Application fail outright:
//
//	resource :Namespace is not permitted in project <project>
//
// The namespace is provisioned outside the render; the rendered Application
// does not create it either (`CreateNamespace=false`). So the base namespace
// manifest, and its entry in the base kustomization, are both guarded by
// `{{- if not .Restricted }}` — the convention every other official service
// agent follows, and the one this agent already follows for the overlay Secret.
//
// The shared helper agenttesting.AssertKustomizeTemplates cannot catch this:
// core's manifest validator treats Namespace as ownership-checked rather than
// cluster-scoped, so an emitted Namespace whose name matches the target and
// which carries the managed-by label passes static conformance. The invariant
// has to be asserted here.
func TestRestrictedRenderOmitsTheNamespace(t *testing.T) {
	for _, profile := range restrictedProfiles() {
		t.Run(profile.String(), func(t *testing.T) {
			destination := renderDeploymentTree(t, profile)

			if _, err := os.Stat(filepath.Join(destination, "base", "namespace.yaml")); !os.IsNotExist(err) {
				t.Errorf("a restricted render must omit base/namespace.yaml, not leave a stub (stat error: %v)", err)
			}
			kustomization := readRenderedFile(t, destination, "base", "kustomization.yaml")
			if strings.Contains(kustomization, "namespace.yaml") {
				t.Errorf("a restricted base kustomization must not list namespace.yaml:\n%s", kustomization)
			}
		})
	}
}

// The guard narrows the restricted profile only: a local apply still creates
// the namespace it deploys into, so the ephemeral render must keep emitting it
// and keep listing it. Without this the guard could be "satisfied" by deleting
// the template.
func TestEphemeralRenderStillEmitsTheNamespace(t *testing.T) {
	destination := renderDeploymentTree(t, builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_EPHEMERAL_LOCAL_APPLY_V1)

	namespace := readRenderedFile(t, destination, "base", "namespace.yaml")
	for _, want := range []string{"kind: Namespace", "codefly-test", "app.kubernetes.io/managed-by: codefly"} {
		if !strings.Contains(namespace, want) {
			t.Errorf("ephemeral base/namespace.yaml does not contain %q:\n%s", want, namespace)
		}
	}
	kustomization := readRenderedFile(t, destination, "base", "kustomization.yaml")
	if !strings.Contains(kustomization, "- namespace.yaml") {
		t.Errorf("ephemeral base kustomization must list namespace.yaml:\n%s", kustomization)
	}
	// The guard must not cost the other resources their entry.
	for _, want := range []string{"- deployment.yaml", "- service.yaml"} {
		if !strings.Contains(kustomization, want) {
			t.Errorf("base kustomization lost %q:\n%s", want, kustomization)
		}
	}
}

// restrictedProfiles is the set core treats as restricted: the transport-neutral
// profile and, for the migration window, its deprecated predecessor, which must
// render identically.
func restrictedProfiles() []builderv0.KubernetesOutputProfile {
	return []builderv0.KubernetesOutputProfile{
		builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
		builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_PROMOTABLE_GITOPS_V1, //nolint:staticcheck // migration compatibility
	}
}

// renderDeploymentTree renders the embedded deployment templates for one output
// profile through the production path and returns the destination root, so both
// the base and the overlays can be asserted. A restricted render additionally
// requires a digest-pinned image, as core's static conformance does.
func renderDeploymentTree(t *testing.T, profile builderv0.KubernetesOutputProfile) string {
	t.Helper()
	ctx := context.Background()
	identity := &resources.ServiceIdentity{
		Workspace: "workspace",
		Module:    "module",
		Name:      "example-service",
		Version:   "1.2.3",
	}
	base := &services.Base{
		Wool:        wool.Get(ctx),
		Identity:    identity,
		Information: &services.Information{Service: resources.ToServiceWithCase(identity), Module: resources.ToModuleWithCase(identity)},
	}
	if services.IsRestrictedOutputProfile(profile) {
		base.SetDockerImage(&resources.DockerImage{
			Name:   "example/service",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})
	} else {
		base.SetDockerImage(resources.NewDockerImage("example/service:1.2.3"))
	}
	builder := &services.BuilderWrapper{Base: base}
	base.Builder = builder

	destination := t.TempDir()
	deployment := &builderv0.KubernetesDeployment{
		Namespace:   "codefly-test",
		Destination: destination,
		Profile:     profile,
	}
	params := services.DeploymentParameters{
		ConfigMap: services.EnvironmentMap{"CODEFLY_TEST_VALUE": "value"},
		SecretMap: services.EnvironmentMap{"CODEFLY_TEST_SECRET": "value"},
	}
	if err := builder.KustomizeDeploy(ctx, &basev0.Environment{Name: "test"}, deployment, deploymentFS, params); err != nil {
		t.Fatalf("render %s kustomize templates: %v", profile, err)
	}
	return destination
}

func readRenderedFile(t *testing.T, destination string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{destination}, parts...)...)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return string(content)
}
