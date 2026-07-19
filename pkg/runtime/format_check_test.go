package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckGoFormattingIsReadOnlyAndScoped(t *testing.T) {
	dir := t.TempDir()
	source := "package main\n\nfunc main(){ println(\"ok\") }\n"
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := checkGoFormatting(dir, "main.go")
	if err == nil || !strings.Contains(output, "main.go: needs Fix") {
		t.Fatalf("format check = output:%q err:%v", output, err)
	}
	written, readErr := os.ReadFile(path)
	if readErr != nil || string(written) != source {
		t.Fatalf("read-only lint changed source: err=%v content=%q", readErr, written)
	}
}
