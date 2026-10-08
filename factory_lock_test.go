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

// The scaffolded service must take its address from the COMPOSITION. A
// hardcoded port is wrong the moment two services want it and ignores what the
// composition assigned, so the service listens where nothing is looking. This
// agent shipped `Addr: ":8080"` and nothing caught it, because no test ran the
// service it generates.
func TestGeneratedServiceResolvesItsAddress(t *testing.T) {
	main, err := os.ReadFile("base/code/main.go")
	if err != nil {
		t.Fatalf("read base main.go: %v", err)
	}
	if !bytes.Contains(main, []byte("ResolveNetworkInstance()")) {
		t.Fatal("the generated service must resolve its address through the composition")
	}
	if bytes.Contains(main, []byte(`Addr: ":8080"`)) {
		t.Fatal("the generated service hardcodes a port instead of resolving one")
	}
}
