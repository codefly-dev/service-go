package main

import (
	"context"
	"io/fs"
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

// TestRestrictedProfileShipsNoNamespace pins the deployment templates to the
// convention every other official agent follows: a restricted (GitOps) render
// is applied into a namespace the cell already provisions, under an AppProject
// that permits no cluster-scoped resource, so it must neither ship a Namespace
// nor reference one from its kustomization. A Namespace in that tree makes the
// cell refuse the whole Application. The ephemeral local-apply profile still
// creates its own namespace.
func TestRestrictedProfileShipsNoNamespace(t *testing.T) {
	restricted := renderProfile(t, builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1)
	if found := namespaceDocuments(t, restricted); len(found) > 0 {
		t.Errorf("restricted render ships a Namespace in %v", found)
	}
	kustomization, err := os.ReadFile(filepath.Join(restricted, "base", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(kustomization), "namespace.yaml") {
		t.Errorf("restricted base kustomization still lists namespace.yaml:\n%s", kustomization)
	}

	ephemeral := renderProfile(t, builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_EPHEMERAL_LOCAL_APPLY_V1)
	if found := namespaceDocuments(t, ephemeral); len(found) != 1 {
		t.Errorf("ephemeral render must create exactly its own namespace, found %v", found)
	}
}

func renderProfile(t *testing.T, profile builderv0.KubernetesOutputProfile) string {
	t.Helper()
	ctx := context.Background()
	identity := &resources.ServiceIdentity{Workspace: "workspace", Module: "module", Name: "example-service", Version: "1.2.3"}
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
	deployment := &builderv0.KubernetesDeployment{Namespace: "codefly-test", Destination: destination, Profile: profile}
	params := services.DeploymentParameters{ConfigMap: services.EnvironmentMap{"CODEFLY_TEST_VALUE": "value"}}
	if err := builder.KustomizeDeploy(ctx, &basev0.Environment{Name: "test"}, deployment, deploymentFS, params); err != nil {
		t.Fatalf("render %s: %v", profile, err)
	}
	return destination
}

// namespaceDocuments returns every rendered file that declares a Namespace.
func namespaceDocuments(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "kind: Namespace") {
			found = append(found, strings.TrimPrefix(path, root))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestRenderedPodRunsAsNumericUser pins a numeric uid on every rendered
// profile. The image's USER is a name, and the kubelet refuses a runAsNonRoot
// container it cannot verify ("image has non-numeric user (appuser), cannot
// verify user is non-root"), so without a numeric runAsUser no pod starts.
func TestRenderedPodRunsAsNumericUser(t *testing.T) {
	for _, profile := range []builderv0.KubernetesOutputProfile{
		builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_RESTRICTED_PORTABLE_V1,
		builderv0.KubernetesOutputProfile_KUBERNETES_OUTPUT_PROFILE_EPHEMERAL_LOCAL_APPLY_V1,
	} {
		rendered, err := os.ReadFile(filepath.Join(renderProfile(t, profile), "base", "deployment.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(rendered), "runAsUser: 65534"); got != 2 {
			t.Errorf("%s: want runAsUser 65534 on the pod and the container, found %d:\n%s", profile, got, rendered)
		}
	}
}
