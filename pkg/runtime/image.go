package runtime

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/codefly-dev/core/resources"
	"github.com/codefly-dev/core/shared"
)

// runtime-image.json is the lock for the Go runtime image: the name, tag and
// manifest-index digest the Runtime image workflow publishes from
// runtime-image/Dockerfile, and the platforms behind that index. Move Go in
// the Dockerfile, rebuild reproducibly, write the digest here, tag
// runtime-v<n>; CI proves the lock reproduces and is anonymously pullable.
//
//go:embed runtime-image.json
var runtimeImageLockJSON []byte

// RuntimeImage is the default runtime Docker image. Specializations can
// override by reassigning before Init if their layer needs a different base.
var RuntimeImage = shared.Must(ParseRuntimeImageLock(runtimeImageLockJSON)).DockerImage

// RuntimeImagePlatforms is what ships behind RuntimeImage's digest: the digest
// addresses a manifest index, so this list is what says how many images it is.
var RuntimeImagePlatforms = shared.Must(ParseRuntimeImageLock(runtimeImageLockJSON)).Platforms

type runtimeImageLock struct {
	Name      string   `json:"name"`
	Tag       string   `json:"tag"`
	Digest    string   `json:"digest"`
	Platforms []string `json:"platforms"`
}

// RuntimeImageLock is a pinned image together with the platforms it ships.
type RuntimeImageLock struct {
	*resources.DockerImage
	Platforms []string
}

// ParseRuntimeImageLock reads a runtime-image.json: a non-empty name and tag,
// a sha256 manifest digest, and at least one os/arch platform with no repeats.
func ParseRuntimeImageLock(content []byte) (*RuntimeImageLock, error) {
	var lock runtimeImageLock
	if err := json.Unmarshal(content, &lock); err != nil {
		return nil, fmt.Errorf("parse runtime image lock: %w", err)
	}
	if lock.Name == "" {
		return nil, fmt.Errorf("runtime image name is required")
	}
	if lock.Tag == "" {
		return nil, fmt.Errorf("runtime image tag is required")
	}
	algorithm, encoded, found := strings.Cut(lock.Digest, ":")
	decoded, err := hex.DecodeString(encoded)
	if !found || algorithm != "sha256" || err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("runtime image digest must be a sha256 digest")
	}
	if len(lock.Platforms) == 0 {
		return nil, fmt.Errorf("runtime image platforms are required")
	}
	seen := map[string]bool{}
	for _, platform := range lock.Platforms {
		osName, architecture, ok := strings.Cut(platform, "/")
		if !ok || osName == "" || architecture == "" {
			return nil, fmt.Errorf("runtime image platform %q must be in os/arch form", platform)
		}
		if seen[platform] {
			return nil, fmt.Errorf("runtime image platform %q is duplicated", platform)
		}
		seen[platform] = true
	}
	return &RuntimeImageLock{
		DockerImage: &resources.DockerImage{Name: lock.Name, Tag: lock.Tag, Digest: lock.Digest},
		Platforms:   lock.Platforms,
	}, nil
}
