package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

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
