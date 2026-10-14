package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/resources"
	gobuilder "github.com/codefly-dev/service-go/pkg/builder"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// Create through the real Builder RPC, allocate the declared endpoint and
// run the emitted service at that address. A hand-injected endpoint would
// conceal a factory that emitted an HTTP server without declaring one.
func TestGeneratedServiceRunsAtResolvedAddress(t *testing.T) {
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, "service.codefly.yaml"), serviceConfig)
	builder := gobuilder.New(goservice.New(agent), gobuilder.BuildConfig{FactoryFS: factoryFS, BuilderFS: builderFS, DeploymentFS: deploymentFS, Requirements: requirements})
	builderClient := grpcBuilderClient(t, builder)
	loaded, err := builderClient.Load(t.Context(), &builderv0.LoadRequest{Identity: &basev0.ServiceIdentity{Name: "myservice", Module: "mymodule", Version: "0.0.1", WorkspacePath: workspace, RelativeToWorkspace: "."}, CreationMode: &builderv0.CreationMode{}})
	if err != nil || loaded.GetState().GetState() != builderv0.LoadStatus_READY {
		t.Fatalf("load: %v %v", loaded, err)
	}
	created, err := builderClient.Create(t.Context(), &builderv0.CreateRequest{})
	if err != nil || created.GetState().GetState() != builderv0.CreateStatus_CREATED {
		t.Fatalf("create: %v %v", created, err)
	}
	declared, err := resources.LoadServiceFromDir(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.GetEndpoints()) != 1 || len(declared.Endpoints) != 1 || declared.Endpoints[0].API != "http" || declared.Endpoints[0].Visibility != resources.VisibilityPrivate {
		t.Fatalf("factory must declare the generated private HTTP endpoint: %v %v", created.GetEndpoints(), declared.Endpoints)
	}
	endpoint := resources.EndpointInformationFromProto(created.Endpoints[0])
	binary := filepath.Join(t.TempDir(), "service")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join(workspace, "code")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build scaffold: %v\n%s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var environment []string
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "CODEFLY") {
			environment = append(environment, variable)
		}
	}
	environment = append(environment, resources.ModulePrefix+"=mymodule", resources.ServicePrefix+"=myservice", "CODEFLY__ENVIRONMENT=local")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, binary)
	run.Dir = workspace
	run.Env = append(environment, resources.EndpointAsEnvironmentVariableKey(endpoint)+"=http://"+address)
	output, err := os.CreateTemp(t.TempDir(), "process-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	run.Stdout, run.Stderr = output, output
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = run.Wait() }()
	client := &http.Client{Timeout: time.Second}
	for {
		response, err := client.Get("http://" + address + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("health = %d", response.StatusCode)
			}
			break
		}
		if ctx.Err() != nil {
			data, _ := os.ReadFile(output.Name())
			t.Fatalf("scaffold did not listen at resolved address: %v\n%s", err, data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// No allocated carrier is a refusal, not a hardcoded/default listener.
	missing := exec.CommandContext(ctx, binary)
	missing.Dir = run.Dir
	missing.Env = environment
	if out, err := missing.CombinedOutput(); err == nil || !strings.Contains(string(out), "cannot resolve this service's http address") {
		t.Fatalf("missing carrier must refuse: %v\n%s", err, out)
	}
}
