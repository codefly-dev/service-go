package runtime_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/codefly-dev/core/agents/helpers/code"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	golanghelpers "github.com/codefly-dev/core/runners/golang"
	selectioncontract "github.com/codefly-dev/core/runners/testselection"

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

func TestRuntimeHonorsTypedSelectionWithStructuredResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/selection\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testSource := `package selection

import "testing"

func TestSelectedFailure(t *testing.T) { t.Fatal("selected") }
func TestUnselectedPass(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(dir, "selection_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	rt := goruntime.New(svc)
	req := &runtimev0.TestRequest{
		Selection: &runtimev0.TestSelection{Scope: &runtimev0.TestSelection_TestCase{TestCase: &runtimev0.TestCaseSelection{
			Package:       ".",
			Path:          "selection_test.go",
			QualifiedName: []string{"TestSelectedFailure"},
		}}},
		SelectionId: "go-selected-case",
	}
	resp, err := rt.Test(context.Background(), req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if err := selectioncontract.VerifyAcknowledgement(req, resp); err != nil {
		t.Fatalf("selection acknowledgement: %v", err)
	}
	if resp.GetResult().GetState() != runtimev0.TestRunResult_FAILED || resp.GetCounts().GetTotal() != 1 || resp.GetCounts().GetFailed() != 1 {
		t.Fatalf("selected result = %s counts=%+v, want only one failing case", resp.GetResult().GetState(), resp.GetCounts())
	}
}

func TestRuntimePropagatesFailFastToNativeRunner(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/failfast\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testSource := `package failfast

import "testing"

func TestFirstFailure(t *testing.T) { t.Fatal("first") }
func TestSecondFailure(t *testing.T) { t.Fatal("second") }
`
	if err := os.WriteFile(filepath.Join(dir, "failfast_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	rt := goruntime.New(svc)
	resp, err := rt.Test(context.Background(), &runtimev0.TestRequest{
		Formula:  &runtimev0.TestFormula{Command: []string{"go", "test", "-json", "./..."}},
		FailFast: true,
	})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.GetCounts().GetFailed() != 1 || resp.GetCounts().GetTotal() != 1 {
		t.Fatalf("fail-fast counts = %+v, want only the first failing test", resp.GetCounts())
	}
}

func TestRuntimeTestExecutesEveryInvocation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/cacheproof\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testSource := fmt.Sprintf(`package cacheproof

import (
	"net/http"
	"testing"
)

func TestInvocationReachesCounter(t *testing.T) {
	response, err := http.Get(%q)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}
`, server.URL)
	if err := os.WriteFile(filepath.Join(dir, "cacheproof_test.go"), []byte(testSource), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	rt := goruntime.New(svc)
	request := &runtimev0.TestRequest{
		Formula: &runtimev0.TestFormula{Command: []string{"go", "test", "-json", "./..."}},
	}
	for attempt := 1; attempt <= 2; attempt++ {
		response, err := rt.Test(context.Background(), request)
		if err != nil {
			t.Fatalf("Test attempt %d: %v", attempt, err)
		}
		if response.GetResult().GetState() != runtimev0.TestRunResult_PASSED || response.GetCounts().GetPassed() != 1 {
			t.Fatalf("attempt %d state = %s counts=%+v", attempt, response.GetResult().GetState(), response.GetCounts())
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("formula test binary invocation count = %d, want 2; Runtime.Test reused a successful Go cache result", got)
	}

	runner, err := golanghelpers.NewNativeGoRunner(context.Background(), dir, ".")
	if err != nil {
		t.Fatalf("new native runner: %v", err)
	}
	runner.WithWorkspace(false)
	rt.RunnerEnvironment = runner
	for attempt := 1; attempt <= 2; attempt++ {
		response, err := rt.Test(context.Background(), &runtimev0.TestRequest{})
		if err != nil {
			t.Fatalf("default Test attempt %d: %v", attempt, err)
		}
		if response.GetResult().GetState() != runtimev0.TestRunResult_PASSED || response.GetCounts().GetPassed() != 1 {
			t.Fatalf("default attempt %d state = %s counts=%+v", attempt, response.GetResult().GetState(), response.GetCounts())
		}
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("combined test binary invocation count = %d, want 4; Runtime.Test reused a successful Go cache result", got)
	}
}
