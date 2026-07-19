// Package builder implements the generic Go Builder gRPC service.
// Specializations embed *Builder to inherit Load / Init / Sync / Create /
// Build / Deploy. Because //go:embed cannot reach outside the .go file's
// directory, the caller (binary main.go) provides the three template FS
// trees (factory, builder, deployment) at construction time.
package builder

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/codefly-dev/core/agents/communicate"
	"github.com/codefly-dev/core/agents/services"
	"github.com/codefly-dev/core/agents/services/audit"
	"github.com/codefly-dev/core/agents/services/sbom"
	"github.com/codefly-dev/core/builders"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	golanghelpers "github.com/codefly-dev/core/runners/golang"
	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"

	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// Setting names for communicate prompts.
const (
	HotReload                 = golanghelpers.SettingHotReload
	DebugSymbols              = golanghelpers.SettingDebugSymbols
	RaceConditionDetectionRun = golanghelpers.SettingRaceConditionDetectionRun
)

// BuildConfig provides the embedded template trees plus the file
// requirements descriptor. Specializations construct this struct with
// their own //go:embed directives in their main.go.
type BuildConfig struct {
	FactoryFS     embed.FS // templates/factory — service scaffolding
	BuilderFS     embed.FS // templates/builder — Dockerfile generation
	DeploymentFS  embed.FS // templates/deployment — k8s manifests
	Requirements  *builders.Dependencies
	GoVersion     string
	AlpineVersion string
}

// Builder is the generic Go builder server. Embedded by specializations.
type Builder struct {
	services.BuilderServer
	*goservice.Service

	cfg           BuildConfig
	cacheLocation string
	answers       map[string]*agentv0.Answer
}

// New builds a generic Go Builder. Caller provides template FS + deps.
func New(svc *goservice.Service, cfg BuildConfig) *Builder {
	return &Builder{Service: svc, cfg: cfg}
}

func (s *Builder) Load(ctx context.Context, req *builderv0.LoadRequest) (*builderv0.LoadResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if err := s.Builder.Load(ctx, req.Identity, s.Settings); err != nil {
		return nil, err
	}
	if err := s.Settings.Validate(); err != nil {
		return s.Builder.LoadErrorf(err, "invalid Go settings")
	}

	s.Service.SourceLocation = s.Local("%s", s.Settings.GoSourceDir())
	s.cacheLocation = s.Local(".cache")
	if s.cfg.Requirements != nil {
		s.cfg.Requirements.Localize(s.Location)
	}

	if req.CreationMode != nil {
		s.Builder.CreationMode = req.CreationMode
		gs, err := templates.ApplyTemplateFrom(ctx, shared.Embed(s.cfg.FactoryFS), "templates/factory/GETTING_STARTED.md", s.Information)
		if err != nil {
			return s.Builder.LoadError(err)
		}
		s.Builder.GettingStarted = gs
		return s.Builder.LoadResponse()
	}

	s.Endpoints, _ = s.Base.Service.LoadEndpoints(ctx)
	return s.Builder.LoadResponse()
}

func (s *Builder) Init(ctx context.Context, req *builderv0.InitRequest) (*builderv0.InitResponse, error) {
	defer s.Wool.Catch()
	s.Builder.LogInitRequest(req)
	ctx = s.Wool.Inject(ctx)
	s.DependencyEndpoints = req.DependenciesEndpoints
	return s.Builder.InitResponse()
}

func (s *Builder) Update(ctx context.Context, _ *builderv0.UpdateRequest) (*builderv0.UpdateResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	return &builderv0.UpdateResponse{}, nil
}

// Sync is a no-op on the generic layer — go has no protos to regenerate.
// Specializations (go-grpc) override.
func (s *Builder) Sync(ctx context.Context, _ *builderv0.SyncRequest) (*builderv0.SyncResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	return s.Builder.SyncResponse()
}

// Build produces a Docker image via the shared go builder helper.
func (s *Builder) Build(ctx context.Context, req *builderv0.BuildRequest) (*builderv0.BuildResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	return golanghelpers.BuildGoDocker(ctx, s.Base.Builder, req, s.Location,
		s.cfg.Requirements, s.cfg.BuilderFS, s.cfg.GoVersion, s.cfg.AlpineVersion)
}

// Audit is inherited by every Go specialization. Scanner ownership belongs in
// this language base so a new Go plugin gets a typed, fail-closed security RPC
// without reimplementing process invocation or result mapping.
func (s *Builder) Audit(ctx context.Context, req *builderv0.AuditRequest) (*builderv0.AuditResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	result, err := audit.Golang(ctx, s.Service.SourceLocation, req.GetIncludeOutdated())
	if err != nil {
		return s.Builder.AuditError(err)
	}
	return s.Builder.AuditResponse(req, result.Findings, result.Outdated, result.Tool, result.Language)
}

// SBOM is inherited by every Go specialization and inventories the exact
// GOWORK-disabled module graph selected by go.mod/go.sum.
func (s *Builder) SBOM(ctx context.Context, _ *builderv0.SBOMRequest) (*builderv0.SBOMResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	result, err := sbom.Golang(ctx, s.Service.SourceLocation)
	if err != nil {
		return s.Builder.SBOMError(err)
	}
	return s.Builder.SBOMResponse(result.Bom, result.Tool, result.Language, result.SHA256)
}

// Package emits portable Go binaries and release-bound CycloneDX evidence.
// The operation is plugin-owned and local-first; agent build, CI, and future
// editor/Mind consumers all call this same typed RPC.
func (s *Builder) Package(ctx context.Context, req *builderv0.PackageRequest) (*builderv0.PackageResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	if req == nil {
		return s.Builder.PackageError(fmt.Errorf("package request is required"))
	}
	outputDirectory := filepath.Clean(req.GetOutputDirectory())
	if !filepath.IsAbs(outputDirectory) {
		return s.Builder.PackageError(fmt.Errorf("package output_directory must be absolute"))
	}
	artifactName := strings.TrimSpace(req.GetArtifactName())
	if artifactName == "" && s.Identity != nil {
		artifactName = s.Identity.Name
	}
	if artifactName == "" || artifactName == "." || filepath.Base(artifactName) != artifactName || strings.ContainsAny(artifactName, "/\\\x00") {
		return s.Builder.PackageError(fmt.Errorf("invalid package artifact_name %q", artifactName))
	}
	targets, err := normalizePackageTargets(req.GetTargets())
	if err != nil {
		return s.Builder.PackageError(err)
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return s.Builder.PackageError(fmt.Errorf("create package output: %w", err))
	}

	artifacts := make([]*builderv0.PackageArtifact, 0, len(targets)*2)
	for _, target := range targets {
		destinationDirectory := filepath.Join(outputDirectory, target.GetOs()+"-"+target.GetArchitecture())
		if err := os.MkdirAll(destinationDirectory, 0o755); err != nil {
			return s.Builder.PackageError(fmt.Errorf("create target output: %w", err))
		}
		destination := filepath.Join(destinationDirectory, artifactName)
		if err := packageGoBinary(ctx, s.Service.SourceLocation, destination, target); err != nil {
			return s.Builder.PackageError(err)
		}
		digest, err := packageFileSHA256(destination)
		if err != nil {
			return s.Builder.PackageError(err)
		}
		artifacts = append(artifacts, &builderv0.PackageArtifact{
			Kind:      builderv0.PackageArtifact_EXECUTABLE,
			Path:      destination,
			Target:    target,
			Sha256:    digest,
			MediaType: "application/vnd.codefly.executable",
		})
	}

	if req.GetIncludeSbom() {
		source, err := sbom.GolangWithOptions(ctx, s.Service.SourceLocation, sbom.GolangOptions{
			UseWorkspace: s.Settings.WithWorkspace,
		})
		if err != nil {
			return s.Builder.PackageError(fmt.Errorf("generate package SBOM: %w", err))
		}
		for _, executable := range append([]*builderv0.PackageArtifact(nil), artifacts...) {
			subject := req.GetSubject()
			publisher, name, version := "", artifactName, ""
			if subject != nil {
				publisher, name, version = subject.GetPublisher(), subject.GetName(), subject.GetVersion()
			}
			if name == "" {
				name = artifactName
			}
			release, err := sbom.AttachArtifact(source, sbom.Artifact{
				Publisher: publisher,
				Name:      name,
				Version:   version,
				Target:    executable.GetTarget().GetOs() + "/" + executable.GetTarget().GetArchitecture(),
				SHA256:    executable.GetSha256(),
			})
			if err != nil {
				return s.Builder.PackageError(fmt.Errorf("attach package artifact to SBOM: %w", err))
			}
			payload, err := sbom.MarshalCycloneDXJSON(release.Bom)
			if err != nil {
				return s.Builder.PackageError(fmt.Errorf("encode package SBOM: %w", err))
			}
			destination := executable.GetPath() + ".cdx.json"
			if err := writePackageFile(destination, append(payload, '\n'), 0o644); err != nil {
				return s.Builder.PackageError(fmt.Errorf("write package SBOM: %w", err))
			}
			digest, err := packageFileSHA256(destination)
			if err != nil {
				return s.Builder.PackageError(err)
			}
			artifacts = append(artifacts, &builderv0.PackageArtifact{
				Kind:      builderv0.PackageArtifact_SBOM,
				Path:      destination,
				Target:    executable.GetTarget(),
				Sha256:    digest,
				MediaType: "application/vnd.cyclonedx+json",
			})
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		left := artifacts[i].GetTarget().GetOs() + "/" + artifacts[i].GetTarget().GetArchitecture() + "/" + artifacts[i].GetPath()
		right := artifacts[j].GetTarget().GetOs() + "/" + artifacts[j].GetTarget().GetArchitecture() + "/" + artifacts[j].GetPath()
		return left < right
	})
	return s.Builder.PackageResponse(artifacts)
}

func normalizePackageTargets(requested []*builderv0.PackageTarget) ([]*builderv0.PackageTarget, error) {
	if len(requested) == 0 {
		requested = []*builderv0.PackageTarget{{Os: runtime.GOOS, Architecture: runtime.GOARCH}}
	}
	byIdentity := make(map[string]*builderv0.PackageTarget, len(requested))
	for _, target := range requested {
		if target == nil || !validPackageTargetComponent(target.GetOs()) || !validPackageTargetComponent(target.GetArchitecture()) {
			return nil, fmt.Errorf("invalid package target %v", target)
		}
		identity := target.GetOs() + "/" + target.GetArchitecture()
		byIdentity[identity] = &builderv0.PackageTarget{Os: target.GetOs(), Architecture: target.GetArchitecture()}
	}
	identities := make([]string, 0, len(byIdentity))
	for identity := range byIdentity {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	targets := make([]*builderv0.PackageTarget, 0, len(identities))
	for _, identity := range identities {
		targets = append(targets, byIdentity[identity])
	}
	return targets, nil
}

func validPackageTargetComponent(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func packageGoBinary(ctx context.Context, source, destination string, target *builderv0.PackageTarget) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".codefly-go-package-*")
	if err != nil {
		return fmt.Errorf("prepare package output: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	command := exec.CommandContext(ctx, "go", "build", "-o", temporaryPath, ".")
	command.Dir = source
	command.Env = append(os.Environ(),
		"GOOS="+target.GetOs(),
		"GOARCH="+target.GetArchitecture(),
	)
	if target.GetOs() != runtime.GOOS || target.GetArchitecture() != runtime.GOARCH {
		command.Env = append(command.Env, "CGO_ENABLED=0")
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go package %s/%s: %w\n%s", target.GetOs(), target.GetArchitecture(), err, strings.TrimSpace(string(output)))
	}
	if err := os.Chmod(temporaryPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return nil
}

func packageFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func writePackageFile(destination string, payload []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".codefly-package-evidence-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

// Deploy renders k8s manifests and applies them.
func (s *Builder) Deploy(ctx context.Context, req *builderv0.DeploymentRequest) (*builderv0.DeploymentResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)
	return golanghelpers.DeployGoKubernetes(ctx, s.Base.Builder, req, s.EnvironmentVariables, s.cfg.DeploymentFS)
}

// Options are the default communicate questions for `codefly add service`.
func (s *Builder) Options() []*agentv0.Question {
	return []*agentv0.Question{
		communicate.NewConfirm(&agentv0.Message{Name: HotReload, Message: "Code hot-reload?", Description: "Restart service when code changes"}, true),
		communicate.NewConfirm(&agentv0.Message{Name: DebugSymbols, Message: "Start with debug symbols?", Description: "Build with debug symbols for stack debugging"}, false),
		communicate.NewConfirm(&agentv0.Message{Name: RaceConditionDetectionRun, Message: "Start with race condition detection?", Description: "Build with -race"}, false),
	}
}

// CreateConfiguration is the template context passed to factory templates.
type CreateConfiguration struct {
	*services.Information
	Envs []string
}

func (s *Builder) Create(ctx context.Context, req *builderv0.CreateRequest) (*builderv0.CreateResponse, error) {
	defer s.Wool.Catch()
	ctx = s.Wool.Inject(ctx)

	if s.Builder.CreationMode != nil && s.Builder.CreationMode.Communicate && s.answers != nil {
		var err error
		s.Settings.HotReload, err = communicate.Confirm(s.answers, HotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.DebugSymbols, err = communicate.Confirm(s.answers, DebugSymbols)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.RaceConditionDetectionRun, err = communicate.Confirm(s.answers, RaceConditionDetectionRun)
		if err != nil {
			return s.Builder.CreateError(err)
		}
	} else {
		options := s.Options()
		var err error
		s.Settings.HotReload, err = communicate.GetDefaultConfirm(options, HotReload)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.DebugSymbols, err = communicate.GetDefaultConfirm(options, DebugSymbols)
		if err != nil {
			return s.Builder.CreateError(err)
		}
		s.Settings.RaceConditionDetectionRun, err = communicate.GetDefaultConfirm(options, RaceConditionDetectionRun)
		if err != nil {
			return s.Builder.CreateError(err)
		}
	}

	create := CreateConfiguration{Information: s.Information, Envs: []string{}}
	ignore := shared.NewIgnore("go.work*", "service.generation.codefly.yaml")

	if err := s.Templates(ctx, create, services.WithFactory(s.cfg.FactoryFS).WithPathSelect(ignore)); err != nil {
		return s.Builder.CreateError(err)
	}
	return s.Builder.CreateResponse(ctx, s.Settings)
}

func (s *Builder) Communicate(stream builderv0.Builder_CommunicateServer) error {
	asker := communicate.NewQuestionAsker(stream)
	answers, err := asker.RunSequence(s.Options())
	if err != nil {
		return err
	}
	s.answers = answers
	return nil
}
