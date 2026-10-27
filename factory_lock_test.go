package main

import (
	"bytes"
	"os"
	"testing"
)

// base/ is a LOCK, not a sample: `templates/factory/code/**` must render
// byte-for-byte to `base/code/**`, so a service created today compiles against
// the same dependencies the fixture proves.
//
// The templates are GENERATED from the fixture — `codefly agent generate
// --service .`, driven by base/service.generation.codefly.yaml — so the fix for
// a drifted template is to edit base/code and regenerate, never to hand-edit
// the .tmpl. This test is what makes the two impossible to diverge quietly;
// Dependabot does not watch base/code.
func TestFactoryTemplatesRenderToBase(t *testing.T) {
	for _, f := range []struct{ base, template string }{
		{"base/GETTING_STARTED.md", "templates/factory/GETTING_STARTED.md.tmpl"},
		{"base/code/main.go", "templates/factory/code/main.go.tmpl"},
		{"base/code/go.mod", "templates/factory/code/go.mod.tmpl"},
		{"base/code/go.sum", "templates/factory/code/go.sum.tmpl"},
	} {
		t.Run(f.base, func(t *testing.T) {
			base, err := os.ReadFile(f.base)
			if err != nil {
				t.Fatalf("read %s: %v", f.base, err)
			}
			tmpl, err := factoryFS.ReadFile(f.template)
			if err != nil {
				t.Fatalf("read %s: %v", f.template, err)
			}
			// The one substitution the generator makes, undone.
			rendered := bytes.ReplaceAll(tmpl, []byte("{{ .Service.Name.DNSCase }}"), []byte("codefly-base"))
			if !bytes.Equal(base, rendered) {
				t.Fatalf("%s drifted from %s: edit base/code and re-run `codefly agent generate --service .`", f.template, f.base)
			}
		})
	}
}
