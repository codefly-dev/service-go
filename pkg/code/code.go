// Package code implements the generic Go Code gRPC service.
// Specializations can embed *Code and register additional Execute
// overrides for framework-specific handlers.
//
// Current capabilities:
//   - File / git operations from embedded *corecode.GoCodeServer
//   - Fix (goimports + gofmt)
//   - ApplyEdit with safe fixing by default
//   - AddDependency / RemoveDependency (go get / go mod edit -droprequire)
package code

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"

	corecode "github.com/codefly-dev/core/code"
	"github.com/codefly-dev/core/failures"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
	"golang.org/x/tools/imports"

	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// runTool wraps a short-lived toolchain command in the plugin's active
// RunnerEnvironment (native / docker / nix) and returns captured output.
// When ActiveEnv is nil (Runtime.Init hasn't run), resolves a standalone
// env from the plugin's declared RuntimeContext via ResolveStandalone-
// Environment — so Code ops stay mode-consistent even pre-Init. Only
// Container mode silently falls through to native because the Docker
// image is plugin-specific and not known at the Code layer.
func (c *Code) runTool(ctx context.Context, dir, cmd string, args ...string) ([]byte, error) {
	env := c.Service.ActiveEnv
	if env == nil {
		var rctx *basev0.RuntimeContext
		if c.Service.Base != nil && c.Service.Base.Runtime != nil {
			rctx = c.Service.Base.Runtime.RuntimeContext
		}
		env = runners.ResolveStandaloneEnvironment(ctx, dir, rctx)
	}
	proc, err := env.NewProcess(cmd, args...)
	if err != nil {
		return nil, err
	}
	proc.WithDir(dir)
	var buf bytes.Buffer
	proc.WithOutput(&buf)
	runErr := proc.Run(ctx)
	return buf.Bytes(), runErr
}

// Code is the generic Go Code server. It embeds GoCodeServer from core
// (file ops, git, AST analysis, ApplyEdit) and adds Go-specific handlers
// via Override — goimports/gofmt Fix and go-get / go-mod-tidy deps.
type Code struct {
	*corecode.GoCodeServer
	Service *goservice.Service

	initialized bool
}

// New builds a generic Go Code server bound to the shared Service.
func New(svc *goservice.Service) *Code {
	return &Code{
		Service:      svc,
		GoCodeServer: corecode.NewGoCodeServer(".", nil),
	}
}

// InitServer creates the GoCodeServer once SourceDir is resolved.
// Exported so specializations that re-point SourceLocation can force a
// re-init without waiting for lazy init.
func (c *Code) InitServer() {
	c.GoCodeServer = corecode.NewGoCodeServer(c.SourceDir(), nil)
	c.registerOverrides()
	c.initialized = true
}

// EnsureInit lazily swaps in a GoCodeServer pointed at the resolved
// source directory the first time an RPC lands.
func (c *Code) EnsureInit() {
	if !c.initialized {
		c.InitServer()
	}
}

// SourceDir returns the directory to operate on. Resolution:
// Service.SourceLocation → $CODEFLY_AGENT_WORKDIR → <Location>/code.
func (c *Code) SourceDir() string {
	if c.Service.SourceLocation != "" {
		return c.Service.SourceLocation
	}
	if wd := os.Getenv("CODEFLY_AGENT_WORKDIR"); wd != "" {
		return wd
	}
	return c.Service.Location + "/code"
}

// registerOverrides wires agent-specific handlers on top of GoCodeServer.
// GoCodeServer already provides get_project_info and list_dependencies.
// We add a goimports/gofmt source fixer and dependency mutations. The core
// server composes that fixer into both Fix and ApplyEdit and owns the VFS write.
func (c *Code) registerOverrides() {
	c.SetSourceFixer(c.fixGo)
	c.Override("add_dependency", c.handleAddDependency)
	c.Override("remove_dependency", c.handleRemoveDependency)
	// get_call_graph: served via Tooling gRPC service (not through Execute).
}

func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.EnsureInit()
	return c.GoCodeServer.Execute(ctx, req)
}

// fixGo uses the same library that backs goimports, with the real source path
// as context. This avoids temporary /tmp files losing module/package context
// and removes a host-binary dependency from the edit loop.
func (c *Code) fixGo(_ context.Context, input corecode.FixInput) (corecode.FixResult, error) {
	filename := input.Path
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(c.SourceDir(), filename)
	}
	withImports, err := imports.Process(filename, input.Content, &imports.Options{
		Comments:   true,
		Fragment:   false,
		FormatOnly: false,
	})
	if err != nil {
		return corecode.FixResult{}, fmt.Errorf("goimports: %w", err)
	}
	formatted, err := format.Source(withImports)
	if err != nil {
		return corecode.FixResult{}, fmt.Errorf("gofmt: %w", err)
	}
	return corecode.FixResult{Content: formatted, Actions: []string{"goimports", "gofmt"}}, nil
}

// --- Go-specific: Dependency management ---

func (c *Code) handleAddDependency(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetAddDependency()
	pkg := r.PackageName
	if r.Version != "" {
		pkg += "@" + r.Version
	}
	out, err := c.runTool(ctx, c.SourceDir(), "go", "get", pkg)
	if err != nil {
		return failedResponse(&codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{Success: false}}},
			basev0.FailureCode_FAILURE_CODE_PROCESS_FAILED, "code.add-dependency", fmt.Sprintf("go get: %s", string(out))), nil
	}
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{Success: true, InstalledVersion: r.Version}}}, nil
}

func (c *Code) handleRemoveDependency(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetRemoveDependency()
	if out, err := c.runTool(ctx, c.SourceDir(), "go", "mod", "edit", "-droprequire", r.PackageName); err != nil {
		return failedResponse(&codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{Success: false}}},
			basev0.FailureCode_FAILURE_CODE_PROCESS_FAILED, "code.remove-dependency", fmt.Sprintf("go mod edit: %s", string(out))), nil
	}
	_, _ = c.runTool(ctx, c.SourceDir(), "go", "mod", "tidy")
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{Success: true}}}, nil
}

// --- Helpers ---

func failedResponse(response *codev0.CodeResponse, code basev0.FailureCode, operation, message string) *codev0.CodeResponse {
	response.Failure = failures.New(code, operation, message)
	return response
}
