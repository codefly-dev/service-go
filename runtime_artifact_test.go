package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codefly-dev/core/agents/services"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/standards"
	gobuilder "github.com/codefly-dev/service-go/pkg/builder"
	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
	goservice "github.com/codefly-dev/service-go/pkg/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Exercise the wire contract against an actual compiled process, including a
// replacement and a failed hot-reload build while the old binary still exists.
func TestRuntimeReportsEachStartedExecutableOverGRPC(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, "service.codefly.yaml"), serviceConfig+`endpoints:
  - name: http
    api: http
    visibility: public
    exposure: none
spec:
  hot-reload: true
`)
	write(t, filepath.Join(ws, "code", "go.mod"), "module artifactprobe\n\ngo 1.26\n")
	client := grpcRuntimeClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	environment, err := resources.LocalEnvironment().Proto()
	if err != nil {
		t.Fatal(err)
	}
	load, err := client.Load(ctx, &runtimev0.LoadRequest{
		Identity:    &basev0.ServiceIdentity{Name: "myservice", Module: "mymodule", Version: "0.0.1", WorkspacePath: ws, RelativeToWorkspace: "."},
		Environment: environment, DisableCatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.ValidateRuntimeLoadResponse(load); err != nil {
		t.Fatal(err)
	}
	if len(load.GetEndpoints()) != 1 || load.GetEndpoints()[0].GetName() != "http" {
		t.Fatalf("declared endpoint lost: %v", load.GetEndpoints())
	}
	// Write source before Init so the native Go runner sees a buildable module.
	marker := filepath.Join(ws, "started.json")
	source := func(generation int) string {
		return fmt.Sprintf(`package main
import ("encoding/json"; "os"; "time")
func main() {
 executable, err := os.Executable(); if err != nil { panic(err) }
 data, _ := json.Marshal(struct { Executable string; Generation int }{executable, %d})
 if err := os.WriteFile(%q, data, 0600); err != nil { panic(err) }
 for { time.Sleep(time.Second) }
}
`, generation, marker)
	}
	write(t, filepath.Join(ws, "code", "main.go"), source(1))
	init, err := client.Init(ctx, &runtimev0.InitRequest{RuntimeContext: resources.NewRuntimeContextNative()})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.ValidateRuntimeInitResponse(init); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = client.Stop(context.Background(), &runtimev0.StopRequest{}) }()
	var previousGeneration uint64
	for generation := 1; generation <= 3; generation++ {
		if generation == 2 {
			write(t, filepath.Join(ws, "code", "main.go"), "package main\nfunc main() { invalid Go }\n")
			failed, err := client.Start(ctx, &runtimev0.StartRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if failed.GetStatus().GetStartedArtifact() != nil {
				t.Fatal("failed compilation reported the previous executable")
			}
			if failed.GetStatus().GetState() != runtimev0.StartStatus_STARTED {
				t.Fatalf("hot reload must wait for recovery: %v", failed.GetStatus())
			}
		}
		write(t, filepath.Join(ws, "code", "main.go"), source(generation))
		started, err := client.Start(ctx, &runtimev0.StartRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if err := services.ValidateRuntimeStartResponse(started); err != nil {
			t.Fatal(err)
		}
		artifact, ok := services.StartedArtifact(started.GetStatus())
		if !ok {
			t.Fatal("started process omitted runtime-started-artifact/v1")
		}
		if !filepath.IsAbs(artifact.Executable) || len(artifact.Sources) != 0 {
			t.Fatalf("compiled executable report = %v", artifact)
		}
		info, err := os.Stat(artifact.Executable)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatalf("reported path is not executable: %v %v", info, err)
		}
		if started.GetStatus().GetGeneration() <= previousGeneration {
			t.Fatal("replacement did not advance generation")
		}
		previousGeneration = started.GetStatus().GetGeneration()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var actual struct {
				Executable string
				Generation int
			}
			data, _ := os.ReadFile(marker)
			if json.Unmarshal(data, &actual) == nil && actual.Generation == generation {
				reported, err := filepath.EvalSymlinks(artifact.Executable)
				if err != nil {
					t.Fatal(err)
				}
				executed, err := filepath.EvalSymlinks(actual.Executable)
				if err != nil {
					t.Fatal(err)
				}
				if reported != executed {
					t.Fatalf("reported %q, process executed %q", reported, executed)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("generation %d never ran", generation)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func grpcRuntimeClient(t *testing.T) runtimev0.RuntimeClient {
	t.Helper()
	rt := goruntime.New(goservice.New(agent))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	runtimev0.RegisterRuntimeServer(server, rt)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := runtimev0.NewRuntimeClient(conn)
	return client
}

func TestRuntimeRefusesInvalidEndpointContract(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, "service.codefly.yaml"), serviceConfig+`endpoints:
  - name: rest
    api: rest
    visibility: private
`)
	write(t, filepath.Join(ws, "code", "go.mod"), "module endpointprobe\n\ngo 1.26\n")
	write(t, filepath.Join(ws, standards.OpenAPIPath), "not a valid OpenAPI document")
	environment, err := resources.LocalEnvironment().Proto()
	if err != nil {
		t.Fatal(err)
	}
	response, err := grpcRuntimeClient(t).Load(t.Context(), &runtimev0.LoadRequest{
		Identity:    &basev0.ServiceIdentity{Name: "myservice", Module: "mymodule", Version: "0.0.1", WorkspacePath: ws, RelativeToWorkspace: "."},
		Environment: environment, DisableCatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetStatus().GetState() != runtimev0.LoadStatus_ERROR || !strings.Contains(response.GetStatus().GetMessage(), "loading declared endpoints") {
		t.Fatalf("invalid REST contract silently omitted: %v", response)
	}
	b := gobuilder.New(goservice.New(agent), gobuilder.BuildConfig{})
	built, err := grpcBuilderClient(t, b).Load(t.Context(), &builderv0.LoadRequest{Identity: &basev0.ServiceIdentity{Name: "myservice", Module: "mymodule", Version: "0.0.1", WorkspacePath: ws, RelativeToWorkspace: "."}})
	if err != nil {
		t.Fatal(err)
	}
	if built.GetState().GetState() != builderv0.LoadStatus_ERROR || !strings.Contains(built.GetState().GetMessage(), "loading declared endpoints") {
		t.Fatalf("builder omitted invalid endpoint: %v", built)
	}
}
