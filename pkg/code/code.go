// Package code implements the generic Go Code gRPC service.
// Specializations can embed *Code and register additional Execute
// overrides for framework-specific handlers.
//
// Current capabilities:
//   - File / git operations from embedded *corecode.GoCodeServer
//   - Fix (goimports + gofmt)
//   - ApplyEdit with auto-fix
//   - AddDependency / RemoveDependency (go get / go mod edit -droprequire)
package code

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	corecode "github.com/codefly-dev/core/code"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/codefly-dev/core/wool"

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
// We add goimports/gofmt fix, auto-fix apply_edit, and dependency mutations.
func (c *Code) registerOverrides() {
	c.Override("fix", c.handleFix)
	c.Override("apply_edit", c.handleApplyEdit)
	c.Override("add_dependency", c.handleAddDependency)
	c.Override("remove_dependency", c.handleRemoveDependency)
	// get_call_graph: served via Tooling gRPC service (not through Execute).
}

// --- Lazy init wrappers ---

func (c *Code) GetProjectInfo(ctx context.Context, req *codev0.GetProjectInfoRequest) (*codev0.GetProjectInfoResponse, error) {
	c.EnsureInit()
	return c.GoCodeServer.GetProjectInfo(ctx, req)
}

func (c *Code) Execute(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	c.EnsureInit()
	return c.GoCodeServer.Execute(ctx, req)
}

// --- Go-specific: Fix (goimports + gofmt) ---

func (c *Code) handleFix(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetFix()
	absPath := filepath.Join(c.SourceDir(), r.File)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fixResp(false, "", fmt.Sprintf("file not found: %s", r.File), nil), nil
	}

	tmpFile, err := os.CreateTemp("", "mind-fix-*.go")
	if err != nil {
		return fixResp(false, "", fmt.Sprintf("create temp: %v", err), nil), nil
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fixResp(false, "", fmt.Sprintf("write temp: %v", err), nil), nil
	}
	tmpFile.Close()

	tmpDir := filepath.Dir(tmpPath)
	var actions []string
	if out, err := c.runTool(ctx, tmpDir, "goimports", "-w", tmpPath); err != nil {
		wool.Get(ctx).In("Code.Fix").Warn("goimports failed", wool.Field("error", string(out)))
	} else {
		actions = append(actions, "goimports")
	}
	if out, err := c.runTool(ctx, tmpDir, "gofmt", "-w", tmpPath); err != nil {
		wool.Get(ctx).In("Code.Fix").Warn("gofmt failed", wool.Field("error", string(out)))
	} else {
		actions = append(actions, "gofmt")
	}
	result, err := os.ReadFile(tmpPath)
	if err != nil {
		// Don't report success with empty content — the caller would
		// otherwise overwrite the user's file with nothing.
		return fixResp(false, "", fmt.Sprintf("cannot read formatted result: %v", err), actions), nil
	}
	return fixResp(true, string(result), "", actions), nil
}

// --- Go-specific: ApplyEdit with auto-fix ---

func (c *Code) handleApplyEdit(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetApplyEdit()
	absPath := filepath.Join(c.SourceDir(), r.File)
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return applyEditResp(false, "", "", fmt.Sprintf("file not found: %s", r.File), nil), nil
		}
		return nil, fmt.Errorf("reading %s: %w", r.File, err)
	}

	result := corecode.SmartEdit(string(data), r.Find, r.Replace)
	if !result.OK {
		return applyEditResp(false, "", "", "FIND block does not match any content in the file", nil), nil
	}

	edited := result.Content
	var fixActions []string
	if r.AutoFix {
		tmpFile, tmpErr := os.CreateTemp("", "mind-edit-*.go")
		if tmpErr == nil {
			tmpPath := tmpFile.Name()
			defer os.Remove(tmpPath)
			if _, werr := tmpFile.Write([]byte(edited)); werr != nil {
				_ = tmpFile.Close()
				// A partial/empty temp file would make the formatters below
				// silently operate on wrong content — fail loudly instead.
				return applyEditResp(false, "", "", fmt.Sprintf("cannot write temp file: %v", werr), nil), nil
			}
			if cerr := tmpFile.Close(); cerr != nil {
				return applyEditResp(false, "", "", fmt.Sprintf("cannot close temp file: %v", cerr), nil), nil
			}

			tmpDir := filepath.Dir(tmpPath)
			if out, fixErr := c.runTool(ctx, tmpDir, "goimports", "-w", tmpPath); fixErr != nil {
				wool.Get(ctx).In("Code.ApplyEdit").Warn("goimports failed", wool.Field("error", string(out)))
			} else {
				fixActions = append(fixActions, "goimports")
			}
			if out, fixErr := c.runTool(ctx, tmpDir, "gofmt", "-w", tmpPath); fixErr != nil {
				wool.Get(ctx).In("Code.ApplyEdit").Warn("gofmt failed", wool.Field("error", string(out)))
			} else {
				fixActions = append(fixActions, "gofmt")
			}
			if fixed, readErr := os.ReadFile(tmpPath); readErr == nil {
				edited = string(fixed)
			}
		}
	}
	return applyEditResp(true, edited, result.Strategy, "", fixActions), nil
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
		return &codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{
			Success: false, Error: fmt.Sprintf("go get: %s", string(out)),
		}}}, nil
	}
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_AddDependency{AddDependency: &codev0.AddDependencyResponse{Success: true, InstalledVersion: r.Version}}}, nil
}

func (c *Code) handleRemoveDependency(ctx context.Context, req *codev0.CodeRequest) (*codev0.CodeResponse, error) {
	r := req.GetRemoveDependency()
	if out, err := c.runTool(ctx, c.SourceDir(), "go", "mod", "edit", "-droprequire", r.PackageName); err != nil {
		return &codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{
			Success: false, Error: fmt.Sprintf("go mod edit: %s", string(out)),
		}}}, nil
	}
	_, _ = c.runTool(ctx, c.SourceDir(), "go", "mod", "tidy")
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_RemoveDependency{RemoveDependency: &codev0.RemoveDependencyResponse{Success: true}}}, nil
}

// --- Helpers ---

func fixResp(success bool, content, errMsg string, actions []string) *codev0.CodeResponse {
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_Fix{Fix: &codev0.FixResponse{
		Success: success, Content: content, Error: errMsg, Actions: actions,
	}}}
}

func applyEditResp(success bool, content, strategy, errMsg string, fixActions []string) *codev0.CodeResponse {
	return &codev0.CodeResponse{Result: &codev0.CodeResponse_ApplyEdit{ApplyEdit: &codev0.ApplyEditResponse{
		Success: success, Content: content, Strategy: strategy, Error: errMsg, FixActions: fixActions,
	}}}
}
