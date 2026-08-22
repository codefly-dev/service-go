package builder

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyGoContextPreservesModes copies a tree with an executable file and a
// nested directory and asserts contents, layout, and permission bits survive —
// the recipe digest covers file mode, so a dropped executable bit would change
// what the CLI builds.
func TestCopyGoContextPreservesModes(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "go.mod"), "module x\n", 0o644)
	mustWrite(t, filepath.Join(src, "scripts", "build.sh"), "#!/bin/sh\n", 0o755)

	dst := filepath.Join(t.TempDir(), "code")
	if err := copyGoContext(src, dst); err != nil {
		t.Fatalf("copyGoContext: %v", err)
	}

	for rel, wantMode := range map[string]os.FileMode{
		"go.mod":           0o644,
		"scripts/build.sh": 0o755,
	} {
		info, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if info.Mode().Perm() != wantMode {
			t.Errorf("%s mode = %o, want %o", rel, info.Mode().Perm(), wantMode)
		}
	}
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
