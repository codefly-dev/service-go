package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/languages"
	"github.com/codefly-dev/core/resources"
	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// TestStartedProcessReceivesEachConfigurationVariableOnce holds that the
// service's configuration reaches its process exactly once. A started process
// receives the runner environment's entries and then Start's own, so the set
// is easy to deliver twice: Init used to put it on the runner environment and
// Start to hand it again. A core that counts every entry against the 512 KiB
// process-environment limit (before v0.19.12) then refused a service whose
// configuration was only half that size (#162).
func TestStartedProcessReceivesEachConfigurationVariableOnce(t *testing.T) {
	if !languages.HasGoRuntime(nil) {
		t.Skip("native go toolchain not available")
	}
	ctx := t.Context()
	ws := t.TempDir()
	write(t, filepath.Join(ws, "service.codefly.yaml"), serviceConfig)
	write(t, filepath.Join(ws, "code", "go.mod"), "module myservice\n\ngo 1.26\n")
	write(t, filepath.Join(ws, "code", "main.go"), "package main\n\nfunc main() {}\n")
	environment, err := resources.LocalEnvironment().Proto()
	if err != nil {
		t.Fatal(err)
	}

	rt := goruntime.New(goservice.New(agent))
	t.Cleanup(func() { _, _ = rt.Stop(t.Context(), &runtimev0.StopRequest{}) })
	loaded, err := rt.Load(ctx, &runtimev0.LoadRequest{
		Identity:    &basev0.ServiceIdentity{Name: "myservice", Module: "mymodule", Version: "0.0.1", WorkspacePath: ws, RelativeToWorkspace: "."},
		Environment: environment, DisableCatch: true,
	})
	if err != nil || loaded.GetStatus().GetState() != runtimev0.LoadStatus_READY {
		t.Fatalf("load: %v %v", loaded, err)
	}

	runtimeContext := resources.NewRuntimeContextNative()
	workspaceConfiguration := &basev0.Configuration{
		Origin:         resources.ConfigurationWorkspace,
		RuntimeContext: runtimeContext,
		Infos: []*basev0.ConfigurationInformation{{
			Name:                "robin",
			ConfigurationValues: []*basev0.ConfigurationValue{{Key: "chat", Value: "bound"}},
		}},
	}
	// No runner is bound beforehand, so Init creates it exactly as in production.
	init, err := rt.Init(ctx, &runtimev0.InitRequest{
		RuntimeContext:          runtimeContext,
		WorkspaceConfigurations: []*basev0.Configuration{workspaceConfiguration},
	})
	if err != nil || init.GetStatus().GetState() != runtimev0.InitStatus_READY {
		t.Fatalf("init: %v %v", init, err)
	}

	// What Start hands the process: the configuration, each key once.
	started, err := rt.EnvironmentVariables.All()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	configured := 0
	for _, env := range started {
		seen[env.Key]++
		if strings.HasPrefix(env.Key, resources.WorkspaceConfigurationPrefix+"__") {
			configured++
		}
	}
	for key, count := range seen {
		if count != 1 {
			t.Errorf("Start hands %s %d times", key, count)
		}
	}
	if configured != 1 {
		t.Fatalf("Start hands %d workspace configuration variables, want 1", configured)
	}

	// What the process inherits beneath that set: a process from the runner
	// environment, with nothing of its own, reports the environment it was given.
	dump := filepath.Join(ws, "environ")
	proc, err := rt.RunnerEnvironment.Env().NewProcess("sh", "-c", `env > "$0"`, dump)
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Run(ctx); err != nil {
		t.Fatal(err)
	}
	inherited, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(inherited), "\n") {
		if strings.HasPrefix(line, "CODEFLY__") {
			t.Errorf("the runner environment carries %q, so the process would receive it twice", line)
		}
	}
}
