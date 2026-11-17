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
	"slices"
	"strings"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"golang.org/x/mod/modfile"
)

// hostGoEnvironment is the environment every host-side `go` invocation of a
// package build runs with: the agent's own, plus the GOWORK packageGoWork
// settled.
func hostGoEnvironment(goWork string, extra ...string) []string {
	return append(append(os.Environ(), "GOWORK="+goWork), extra...)
}

// goWorkMode is the module-graph selection the caller prepared. The CLI
// prepares it: for a local build it binds the go.work it detected for the
// source checkout, so the agent links the owner checkouts a developer composed
// (codefly-dev/cli cmd/agents/build.go, compileAgent) and an unreleased core
// can be proven without a publication; for standalone CI and release packaging
// it sets GOWORK=off, so the graph is exactly go.mod/go.sum. Nothing prepared
// reads as off; packageGoWork decides what an unprepared build means.
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
func resolvePackageGoToolchain(ctx context.Context, source, goWork string) (string, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	command.Dir = source
	command.Env = hostGoEnvironment(goWork)
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
func resolvePackageSource(ctx context.Context, source string, target *builderv0.PackageTarget, goWork string) (root, entry string, err error) {
	command := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-json", ".")
	command.Dir = source
	command.Env = hostGoEnvironment(goWork, "CGO_ENABLED=1", "GOOS="+target.GetOs(), "GOARCH="+target.GetArchitecture())
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

// packageGoWork settles the module graph one Package call builds, once, for
// every target and both build paths. What the caller prepared wins — a go.work
// path, or the explicit off of standalone CI. With nothing prepared a service
// that does not declare with-workspace is the pinned graph. One that does is
// the go.work Go itself discovers from the source: the graph its SBOM in this
// same call describes and its runtime builds. A declaration no go.work backs
// is refused, because the pinned graph in its place is another program.
func packageGoWork(ctx context.Context, source string, withWorkspace bool) (string, error) {
	if prepared := goWorkMode(); prepared != "off" || os.Getenv("GOWORK") != "" || !withWorkspace {
		return prepared, nil
	}
	command := exec.CommandContext(ctx, "go", "env", "GOWORK")
	command.Dir = source
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Go workspace: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	discovered := strings.TrimSpace(string(output))
	if discovered == "" || discovered == "off" {
		return "", fmt.Errorf("the service declares with-workspace but no go.work governs %s: refusing to package the pinned module graph in its place (prepare GOWORK, or set GOWORK=off to select the pinned graph)", source)
	}
	return discovered, nil
}

// packageWorkspaceMounts lists what a workspace build reads outside the
// packaged module: the go.work, its checksums, and every module Go resolves
// from a directory rather than the module cache — workspace members and the
// local replacements they or the go.work declare. A member the checkout does
// not hold is refused here, on the host, naming the go.work; nothing is built
// from go.mod's pins instead.
func packageWorkspaceMounts(source, goWork string) ([]string, error) {
	payload, err := os.ReadFile(goWork)
	if err != nil {
		return nil, fmt.Errorf("load Go workspace: %w", err)
	}
	work, err := modfile.ParseWork(goWork, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("load Go workspace: %w", err)
	}
	mounts := []string{goWork}
	if sums := goWork + ".sum"; fileExists(sums) {
		mounts = append(mounts, sums)
	}
	add := func(base, path string) string {
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		path = filepath.Clean(path)
		// The packaged module is the toolchain's source already, and so is
		// anything nested in it.
		relative, err := filepath.Rel(source, path)
		if (err != nil || !filepath.IsLocal(relative)) && !slices.Contains(mounts, path) {
			mounts = append(mounts, path)
		}
		return path
	}
	replacements := func(base string, replaces []*modfile.Replace) {
		for _, replace := range replaces {
			if replace.New.Version == "" {
				add(base, replace.New.Path)
			}
		}
	}
	replacements(filepath.Dir(goWork), work.Replace)
	for _, use := range work.Use {
		member := add(filepath.Dir(goWork), use.Path)
		manifest := filepath.Join(member, "go.mod")
		payload, err := os.ReadFile(manifest)
		if err != nil {
			return nil, fmt.Errorf("load Go workspace %s: %w", goWork, err)
		}
		module, err := modfile.ParseLax(manifest, payload, nil)
		if err != nil {
			return nil, fmt.Errorf("load Go workspace %s: %w", goWork, err)
		}
		replacements(member, module.Replace)
	}
	return mounts, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
