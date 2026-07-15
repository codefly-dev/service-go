package runtime_test

import (
	"testing"

	"github.com/codefly-dev/core/agents/helpers/code"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"

	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// TestRuntimeEmbedsService verifies the embedding chain:
//
//	runtime.Runtime → *service.Service → *services.Base
//
// Specializations rely on this chain to inherit Wool, Logger, Location,
// Identity, etc. via method promotion. If embedding is replaced with a
// named field this test breaks loudly.
func TestRuntimeEmbedsService(t *testing.T) {
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	rt := goruntime.New(svc)

	if rt == nil {
		t.Fatal("New returned nil")
	}
	if rt.Service != svc {
		t.Error("embedded Service is not the same pointer passed to New")
	}
	// Promoted fields from *services.Base must be reachable on *Runtime.
	// If these compile, the chain is intact.
	_ = rt.Base
	_ = rt.Settings
	_ = rt.Runtime
}

// TestRuntimeImageIsExported ensures the default runtime image is exported
// so specializations can override or reference it.
func TestRuntimeImageIsExported(t *testing.T) {
	if goruntime.RuntimeImage == nil {
		t.Fatal("RuntimeImage is nil")
	}
	if goruntime.RuntimeImage.Name == "" {
		t.Error("RuntimeImage.Name is empty")
	}
}

func TestEventHandlerRequestsCorrectLifecycleStage(t *testing.T) {
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	rt := goruntime.New(svc)

	if err := rt.EventHandler(code.Change{Path: "code/main.go", IsRelative: true}); err != nil {
		t.Fatalf("go change: %v", err)
	}
	if got := rt.Runtime.DesiredState.GetStage(); got != runtimev0.DesiredState_START {
		t.Fatalf("go change stage = %s, want START", got)
	}

	if err := rt.EventHandler(code.Change{Path: "service.codefly.yaml", IsRelative: true}); err != nil {
		t.Fatalf("service config change: %v", err)
	}
	if got := rt.Runtime.DesiredState.GetStage(); got != runtimev0.DesiredState_LOAD {
		t.Fatalf("service config change stage = %s, want LOAD", got)
	}
}
