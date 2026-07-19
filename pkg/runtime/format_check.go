package runtime

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/imports"
)

// checkGoFormatting performs the read-only half of the Go source fixer. Lint
// reports files whose in-memory goimports/gofmt result differs; callers use the
// Code/Tooling Fix RPC to apply that result.
func checkGoFormatting(sourceDir, target string) (string, error) {
	root, err := goFormatTarget(sourceDir, target)
	if err != nil {
		return err.Error(), err
	}
	var findings []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".cache", "vendor", "node_modules":
				if path != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		fixed, fixErr := imports.Process(path, content, &imports.Options{Comments: true})
		rel, _ := filepath.Rel(sourceDir, path)
		if fixErr != nil {
			findings = append(findings, fmt.Sprintf("%s: goimports analysis failed: %v", filepath.ToSlash(rel), fixErr))
			return nil
		}
		if !bytes.Equal(content, fixed) {
			findings = append(findings, fmt.Sprintf("%s: needs Fix (goimports/gofmt)", filepath.ToSlash(rel)))
		}
		return nil
	})
	if walkErr != nil {
		return strings.Join(findings, "\n"), walkErr
	}
	output := strings.Join(findings, "\n")
	if len(findings) > 0 {
		return output, fmt.Errorf("%d Go source file(s) need safe fixes", len(findings))
	}
	return "", nil
}

func goFormatTarget(sourceDir, target string) (string, error) {
	if target == "" || target == "." || target == "./..." {
		return sourceDir, nil
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(target, "./"), "/...")
	if trimmed == "" {
		return sourceDir, nil
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("absolute lint target is not allowed: %q", target)
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("lint target escapes source root: %q", target)
	}
	resolved := filepath.Join(sourceDir, cleaned)
	if _, err := os.Stat(resolved); err != nil {
		// A Go import path is meaningful to go vet but not mappable to a local
		// formatting scope. Leave formatting to the package-aware Fix call.
		if !strings.HasPrefix(target, ".") && filepath.Ext(target) != ".go" {
			return sourceDir, nil
		}
		return "", err
	}
	return resolved, nil
}
