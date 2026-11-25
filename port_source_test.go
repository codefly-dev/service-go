package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	agenttesting "github.com/codefly-dev/core/agents/testing"
	builderv1 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v1"
	"github.com/codefly-dev/core/standards"
	gobuilder "github.com/codefly-dev/service-go/pkg/builder"
)

// TestPortsComeFromTheStandardPortSource holds that no template of this agent
// writes a port: the Dockerfile's EXPOSE, the Service, the containerPort and
// the probes all render standards.Port for the service's HTTP API, so they
// cannot disagree with each other or with what core tells a consumer to dial.
func TestPortsComeFromTheStandardPortSource(t *testing.T) {
	want := standards.Port(standards.HTTP)
	if got := gobuilder.NewDeploymentParameters().HTTPPort; got != want {
		t.Fatalf("deployment HTTP port = %d, want standards.Port(%s) = %d", got, standards.HTTP, want)
	}

	literal := regexp.MustCompile(`(?m)^\s*(-\s*)?(EXPOSE|port:|targetPort:|containerPort:)\s*\d+`)
	for _, source := range []struct {
		name string
		read func(string) ([]byte, error)
	}{
		{"templates/builder/Dockerfile.tmpl", builderFS.ReadFile},
		{"templates/deployment/kustomize/base/deployment.yaml.tmpl", deploymentFS.ReadFile},
		{"templates/deployment/kustomize/base/service.yaml.tmpl", deploymentFS.ReadFile},
	} {
		content, err := source.read(source.name)
		if err != nil {
			t.Fatalf("read %s: %v", source.name, err)
		}
		if found := literal.Find(content); found != nil {
			t.Errorf("%s hardcodes a port: %q", source.name, found)
		}
	}

	destination := agenttesting.AssertKustomizeTemplates(t, deploymentFS, gobuilder.NewDeploymentParameters())
	service, err := os.ReadFile(filepath.Join(destination, "base", "service.yaml"))
	if err != nil {
		t.Fatalf("read rendered service: %v", err)
	}
	if expected := fmt.Sprintf("      port: %d\n      targetPort: %d\n", want, want); !regexp.MustCompile(regexp.QuoteMeta(expected)).Match(service) {
		t.Errorf("rendered Service does not publish the standard port %d:\n%s", want, service)
	}
	deployment, err := os.ReadFile(filepath.Join(destination, "base", "deployment.yaml"))
	if err != nil {
		t.Fatalf("read rendered deployment: %v", err)
	}
	if got := len(regexp.MustCompile(fmt.Sprintf(`(?m)^\s*(containerPort|port): %d$`, want)).FindAll(deployment, -1)); got != 4 {
		t.Errorf("rendered Deployment names the standard port %d times, want 4 (containerPort and three probes):\n%s", got, deployment)
	}

	b, ctx := loadedBuilder(t)
	out := filepath.Join(t.TempDir(), "recipe")
	if _, err := b.Build(ctx, &builderv1.BuildRequest{
		OutputDirectory: out,
		BuildContext: &builderv1.BuildContext{
			Kind: &builderv1.BuildContext_DockerBuildContext{
				DockerBuildContext: &builderv1.DockerBuildContext{DockerRepository: "registry.example.com"},
			},
		},
	}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(out, "builder", "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	if !regexp.MustCompile(fmt.Sprintf(`(?m)^EXPOSE %d$`, want)).Match(dockerfile) {
		t.Errorf("rendered Dockerfile does not expose the standard port %d:\n%s", want, dockerfile)
	}
}
