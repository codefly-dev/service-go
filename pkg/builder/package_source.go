package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
)

// hostGoEnvironment is the environment every host-side `go` invocation of a
// package build runs with: the agent's own, plus GOWORK settled by goWorkMode.
func hostGoEnvironment(extra ...string) []string {
	return append(append(os.Environ(), "GOWORK="+goWorkMode()), extra...)
}

// goWorkMode is the module-graph selection a package build honours. The CLI
// prepares it: for a local build it binds the go.work it detected for the
// source checkout, so the agent links the owner checkouts a developer composed
// (codefly-dev/cli cmd/agents/build.go, compileAgent) and an unreleased core
// can be proven without a publication; for standalone CI and release packaging
// it sets GOWORK=off, so the graph is exactly go.mod/go.sum. When nothing was
// prepared — the agent invoked outside the CLI — off is the only graph that is
// reproducible, and the one the cross build below always uses.
//
// Before this, every host-side invocation forced GOWORK=off after inheriting
// the environment, which silently discarded the CLI's selection: a local build
// against a go.work pointing at a core checkout still linked the pinned release.
func goWorkMode() string {
	if mode := strings.TrimSpace(os.Getenv("GOWORK")); mode != "" {
		return mode
	}
	return "off"
}

// Use Go's module/toolchain selection on the source, just as the native build
// does. The cross image supplies C compilers, not the module's Go version.
func resolvePackageGoToolchain(ctx context.Context, source string) (string, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	command.Dir = source
	command.Env = hostGoEnvironment()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Go package toolchain: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	selected := strings.TrimSpace(string(output))
	if !version.IsValid(selected) {
		return "", fmt.Errorf("Go package toolchain %q cannot be selected for a cross build", selected)
	}
	return selected, nil
}

// Resolve the declared package with Go itself. Cross builds must mount the
// enclosing module, not just a nested main directory with no go.mod or imports.
func resolvePackageSource(ctx context.Context, source string, target *builderv0.PackageTarget) (root, entry string, err error) {
	command := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-json", ".")
	command.Dir = source
	command.Env = hostGoEnvironment("CGO_ENABLED=1", "GOOS="+target.GetOs(), "GOARCH="+target.GetArchitecture())
	var stderr bytes.Buffer
	command.Stderr = &stderr
	payload, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("resolve Go package source: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var pkg struct {
		Name   string
		Dir    string
		Module *struct{ Dir string }
	}
	if err := json.Unmarshal(payload, &pkg); err != nil {
		return "", "", fmt.Errorf("decode Go package source: %w", err)
	}
	if pkg.Name != "main" {
		return "", "", fmt.Errorf("Go executable packaging requires package main, got %q; select the main package source directory", pkg.Name)
	}
	if pkg.Module == nil || !filepath.IsAbs(pkg.Module.Dir) || !filepath.IsAbs(pkg.Dir) {
		return "", "", fmt.Errorf("Go executable packaging requires a module-owned source directory")
	}
	root, err = filepath.EvalSymlinks(pkg.Module.Dir)
	if err != nil {
		return "", "", err
	}
	directory, err := filepath.EvalSymlinks(pkg.Dir)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || !filepath.IsLocal(relative) {
		return "", "", fmt.Errorf("Go main package must stay inside its module root")
	}
	if relative == "." {
		return root, ".", nil
	}
	return root, "./" + filepath.ToSlash(relative), nil
}
