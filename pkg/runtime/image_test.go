package runtime_test

import (
	"strings"
	"testing"

	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
)

// TestRuntimeImageIsThePinnedCanonicalReference: the default runtime image is
// the lock's ghcr.io image by digest, so the fleet's canonical registry serves
// it and a tag moving under it cannot change what a container runs.
func TestRuntimeImageIsThePinnedCanonicalReference(t *testing.T) {
	image := goruntime.RuntimeImage
	if image.Name != "ghcr.io/codefly-dev/service-go-runtime" {
		t.Fatalf("runtime image name = %q, want the ghcr.io image the Runtime image workflow publishes", image.Name)
	}
	if !strings.HasPrefix(image.Digest, "sha256:") || strings.Contains(image.Digest, "PENDING") {
		t.Fatalf("runtime image digest = %q, want the manifest-index digest of the lock", image.Digest)
	}
	if !strings.Contains(image.FullName(), "@sha256:") {
		t.Fatalf("runtime image reference %q does not pin the digest", image.FullName())
	}
	if len(goruntime.RuntimeImagePlatforms) != 2 {
		t.Fatalf("runtime image platforms = %v, want linux/amd64 and linux/arm64", goruntime.RuntimeImagePlatforms)
	}
}

func TestParseRuntimeImageLockRefusesAnUnpinnedOrMalformedLock(t *testing.T) {
	for name, content := range map[string]string{
		"no digest":          `{"name":"ghcr.io/x/y","tag":"t","platforms":["linux/amd64"]}`,
		"tag only digest":    `{"name":"ghcr.io/x/y","tag":"t","digest":"latest","platforms":["linux/amd64"]}`,
		"no platforms":       `{"name":"ghcr.io/x/y","tag":"t","digest":"sha256:` + strings.Repeat("a", 64) + `"}`,
		"duplicate platform": `{"name":"ghcr.io/x/y","tag":"t","digest":"sha256:` + strings.Repeat("a", 64) + `","platforms":["linux/amd64","linux/amd64"]}`,
		"bad platform":       `{"name":"ghcr.io/x/y","tag":"t","digest":"sha256:` + strings.Repeat("a", 64) + `","platforms":["amd64"]}`,
	} {
		if _, err := goruntime.ParseRuntimeImageLock([]byte(content)); err == nil {
			t.Errorf("%s: lock accepted, want a refusal", name)
		}
	}
}
