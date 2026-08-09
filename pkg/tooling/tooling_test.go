package tooling_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	toolingv0 "github.com/codefly-dev/core/generated/go/codefly/services/tooling/v0"
	"github.com/codefly-dev/core/resources"

	gocode "github.com/codefly-dev/service-go/pkg/code"
	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
	goservice "github.com/codefly-dev/service-go/pkg/service"
	gotooling "github.com/codefly-dev/service-go/pkg/tooling"
)

// TestToolingWiring verifies Tooling holds the Code and Runtime pointers
// the caller supplied. Specializations compose by passing the same pair.
func TestToolingWiring(t *testing.T) {
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	c := gocode.New(svc)
	rt := goruntime.New(svc)

	tl := gotooling.New(c, rt)
	if tl == nil {
		t.Fatal("New returned nil")
	}
	if tl.Code != c {
		t.Error("Tooling.Code is not the Code passed to New")
	}
	if tl.Runtime != rt {
		t.Error("Tooling.Runtime is not the Runtime passed to New")
	}
}

func TestToolingPropagatesCanonicalCodeFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/tooling\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	c := gocode.New(svc)
	c.InitServer()
	response, err := gotooling.New(c, goruntime.New(svc)).Fix(context.Background(), &toolingv0.FixRequest{File: "missing.go"})
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if response.GetSuccess() || response.GetFailure().GetCode() != basev0.FailureCode_FAILURE_CODE_NOT_FOUND {
		t.Fatalf("tooling response = %+v, want propagated not-found failure", response)
	}
	if response.GetFailure().GetOperation() != "code.fix" {
		t.Fatalf("failure operation = %q, want original code.fix", response.GetFailure().GetOperation())
	}
}

func TestToolingForwardsTypedProjectEvidence(t *testing.T) {
	dir := t.TempDir()
	goMod := "module example.test/tooling\n\ngo 1.25\n\nrequire github.com/google/uuid v1.6.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"github.com/google/uuid\"\n\nvar id = uuid.New()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	code := gocode.New(svc)
	code.InitServer()

	project, err := gotooling.New(code, goruntime.New(svc)).GetProjectInfo(context.Background(), &toolingv0.GetProjectInfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if project.GetFailure() != nil || len(project.GetDependencies()) != 1 || project.GetDependencies()[0].GetName() != "github.com/google/uuid" {
		t.Fatalf("dependencies=%+v failure=%+v", project.GetDependencies(), project.GetFailure())
	}
	if len(project.GetSourceFiles()) != 1 || len(project.GetSourceFiles()[0].GetImports()) != 1 || project.GetSourceFiles()[0].GetImports()[0] != "github.com/google/uuid" {
		t.Fatalf("source evidence=%+v", project.GetSourceFiles())
	}
	semantic, err := gotooling.New(code, goruntime.New(svc)).GetSemanticIndex(context.Background(), &toolingv0.GetSemanticIndexRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if semantic.GetFailure() != nil || semantic.GetIndex().GetState() != basev0.SemanticIndexState_SEMANTIC_INDEX_STATE_COMPLETE || len(semantic.GetIndex().GetSymbols()) == 0 {
		t.Fatalf("semantic index = %+v", semantic)
	}
}
