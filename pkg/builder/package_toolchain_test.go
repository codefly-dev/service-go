package builder

import "testing"

func TestCrossCGOToolchainsCoverProductionLoaderTargets(t *testing.T) {
	for _, target := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"} {
		toolchain, found := crossCGOToolchainFor(target)
		if !found {
			t.Fatalf("missing CGO toolchain for %s", target)
		}
		if toolchain.cc == "" || toolchain.cxx == "" {
			t.Fatalf("incomplete CGO toolchain for %s: %+v", target, toolchain)
		}
	}
	if _, found := crossCGOToolchainFor("plan9/amd64"); found {
		t.Fatal("unsupported CGO target was accepted")
	}
}
