package code

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codev0 "github.com/codefly-dev/core/generated/go/codefly/services/code/v0"
	"github.com/codefly-dev/core/resources"

	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// newTestCode creates a Code instance pointing at a temporary Go project.
func newTestCode(t *testing.T) (*Code, string) {
	t.Helper()
	dir := t.TempDir()

	modContent := "module testmod\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modContent), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = dir
	c := New(svc)
	c.InitServer()
	return c, dir
}

func TestFix_GoImports(t *testing.T) {
	code, dir := newTestCode(t)

	src := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{File: "main.go"}},
	})
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	resp := codeResp.GetFix()
	if !resp.Success {
		t.Fatalf("Fix failed: %v", codeResp.GetFailure())
	}
	if !strings.Contains(resp.Content, `"fmt"`) {
		t.Errorf("expected goimports to add fmt import, got:\n%s", resp.Content)
	}
	if !resp.GetChanged() || !resp.GetWrote() || resp.GetBeforeSha256() == resp.GetAfterSha256() {
		t.Fatalf("missing fix mutation evidence: %+v", resp)
	}
	written, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil || string(written) != resp.GetContent() {
		t.Fatalf("Fix did not commit returned content: err=%v content=%q", err, written)
	}
}

func TestFix_GoFmt(t *testing.T) {
	code, dir := newTestCode(t)

	src := "package main\n\nimport \"fmt\"\n\nfunc main() {\nfmt.Println(   \"hello\"   )\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{File: "main.go"}},
	})
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	resp := codeResp.GetFix()
	if !resp.Success {
		t.Fatalf("Fix failed: %v", codeResp.GetFailure())
	}
	if strings.Contains(resp.Content, `"hello"   )`) {
		t.Errorf("gofmt did not normalize spacing:\n%s", resp.Content)
	}
}

func TestFix_DryRunDoesNotWrite(t *testing.T) {
	code, dir := newTestCode(t)
	src := "package main\n\nfunc main(){println(\"hello\")}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{File: "main.go", DryRun: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := codeResp.GetFix()
	if !resp.GetSuccess() || !resp.GetChanged() || resp.GetWrote() {
		t.Fatalf("dry-run evidence = %+v", resp)
	}
	written, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil || string(written) != src {
		t.Fatalf("dry-run changed file: err=%v content=%q", err, written)
	}
}

func TestApplyEditRunsSafeFixerByDefault(t *testing.T) {
	code, dir := newTestCode(t)
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{Operation: &codev0.CodeRequest_ApplyEdit{ApplyEdit: &codev0.ApplyEditRequest{
		File: "main.go", Find: "func main() {}", Replace: "func main() { fmt.Println(\"hello\") }",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	edit := codeResp.GetApplyEdit()
	if !edit.GetSuccess() || !edit.GetChanged() || !edit.GetWrote() {
		t.Fatalf("apply edit = %+v failure=%+v", edit, codeResp.GetFailure())
	}
	if !strings.Contains(edit.GetContent(), `"fmt"`) || len(edit.GetFixActions()) != 2 {
		t.Fatalf("safe fixer was not composed into ApplyEdit:\n%s\nactions=%v", edit.GetContent(), edit.GetFixActions())
	}
	written, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil || string(written) != edit.GetContent() {
		t.Fatalf("ApplyEdit did not commit once: err=%v content=%q", err, written)
	}
}

func TestFix_NoFile(t *testing.T) {
	code, _ := newTestCode(t)

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_Fix{Fix: &codev0.FixRequest{File: "nonexistent.go"}},
	})
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	resp := codeResp.GetFix()
	if resp.Success {
		t.Error("expected Fix to fail for nonexistent file")
	}
	if codeResp.GetFailure().GetCode() != basev0.FailureCode_FAILURE_CODE_NOT_FOUND || !strings.Contains(codeResp.GetFailure().GetMessage(), "not found") {
		t.Errorf("expected typed not-found failure, got: %v", codeResp.GetFailure())
	}
}

func TestReadFile(t *testing.T) {
	code, dir := newTestCode(t)

	content := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ReadFile{ReadFile: &codev0.ReadFileRequest{Path: "main.go"}},
	})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	resp := codeResp.GetReadFile()
	if !resp.Exists {
		t.Fatal("expected file to exist")
	}
	if resp.Content != content {
		t.Errorf("content mismatch: got %q, want %q", resp.Content, content)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	code, _ := newTestCode(t)

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ReadFile{ReadFile: &codev0.ReadFileRequest{Path: "nope.go"}},
	})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	resp := codeResp.GetReadFile()
	if resp.Exists {
		t.Error("expected file to not exist")
	}
}

func TestWriteFile(t *testing.T) {
	code, dir := newTestCode(t)

	content := "package sub\n"
	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_WriteFile{WriteFile: &codev0.WriteFileRequest{
			Path: "sub/lib.go", Content: content,
		}},
	})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	resp := codeResp.GetWriteFile()
	if !resp.Success {
		t.Fatalf("WriteFile failed: %v", codeResp.GetFailure())
	}

	got, err := os.ReadFile(filepath.Join(dir, "sub", "lib.go"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != content {
		t.Errorf("written content mismatch: got %q, want %q", string(got), content)
	}
}

func TestListFiles(t *testing.T) {
	code, dir := newTestCode(t)

	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "lib.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644)

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ListFiles{ListFiles: &codev0.ListFilesRequest{
			Recursive: true, Extensions: []string{".go"},
		}},
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	resp := codeResp.GetListFiles()

	paths := make(map[string]bool)
	for _, f := range resp.Files {
		paths[f.Path] = true
	}
	if !paths["main.go"] {
		t.Error("expected main.go in listing")
	}
	if !paths["pkg/lib.go"] {
		t.Error("expected pkg/lib.go in listing")
	}
	if paths["README.md"] {
		t.Error("README.md should be filtered out with .go extension filter")
	}
}

func TestListFiles_NonRecursive(t *testing.T) {
	code, dir := newTestCode(t)

	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "lib.go"), []byte("package pkg\n"), 0o644)

	codeResp, err := code.Execute(context.Background(), &codev0.CodeRequest{
		Operation: &codev0.CodeRequest_ListFiles{ListFiles: &codev0.ListFilesRequest{Recursive: false}},
	})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	for _, f := range codeResp.GetListFiles().Files {
		if strings.Contains(f.Path, "pkg/lib.go") {
			t.Error("non-recursive listing should not include files in subdirectories")
		}
	}
}
