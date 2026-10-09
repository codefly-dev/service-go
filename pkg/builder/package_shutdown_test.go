package builder

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
	"github.com/codefly-dev/core/runners/companion"
	"github.com/codefly-dev/core/runners/dockerrun"
	"github.com/codefly-dev/core/wool"
)

// Only Shutdown is fault-injected. Init and the build use core's real local
// companion and the real Go toolchain; TestPackageRealCGO covers Docker/CGO.
type packageShutdownRunner struct {
	companion.CompanionRunner
	shutdown func(context.Context) error
}

func (r *packageShutdownRunner) Shutdown(ctx context.Context) error {
	if err := r.CompanionRunner.Shutdown(ctx); err != nil {
		return err
	}
	return r.shutdown(ctx)
}

type packageLogCapture struct {
	sync.Mutex
	warnings []string
}

func (l *packageLogCapture) Process(log *wool.Log) {
	l.Lock()
	defer l.Unlock()
	if log.Level == wool.WARN {
		l.warnings = append(l.warnings, log.String())
	}
}

func TestPackageCrossShutdown(t *testing.T) {
	for _, tc := range []struct {
		name        string
		buildFails  bool
		shutdownErr error
	}{
		{name: "successful package survives cleanup deadline", shutdownErr: context.DeadlineExceeded},
		{name: "failed package retains cleanup deadline", buildFails: true, shutdownErr: context.DeadlineExceeded},
		{name: "successful package rejects other cleanup errors", shutdownErr: errors.New("removal denied")},
		{name: "failed package retains other cleanup errors", buildFails: true, shutdownErr: errors.New("removal denied")},
		{name: "successful cleanup preserves build failure", buildFails: true},
		{name: "successful package and cleanup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := t.TempDir()
			mustWrite(t, filepath.Join(source, "go.mod"), "module example.test/package-shutdown\n\ngo 1.27.0\n", 0600)
			body := "package main\nfunc main() { println(42) }\n"
			if tc.buildFails {
				body = "package main\nfunc main() { missingPackageSymbol() }\n"
			}
			mustWrite(t, filepath.Join(source, "main.go"), body, 0600)
			destination := filepath.Join(t.TempDir(), "agent")
			logs := &packageLogCapture{}
			provider := wool.New(t.Context(), &wool.Resource{Kind: "test", Unique: t.Name()}).WithLogger(logs)
			ctx, cancel := context.WithCancel(provider.Inject(t.Context()))
			defer cancel()
			var containerName string
			shutdownCalled := false
			factory := func(ctx context.Context, opts companion.CompanionOpts) (companion.CompanionRunner, error) {
				if opts.PreferredBackend != companion.BackendDocker {
					t.Fatal("production cross packaging must request Docker")
				}
				containerName = dockerrun.ContainerName(opts.Name)
				opts.PreferredBackend = companion.BackendLocal
				runner, err := companion.NewCompanionRunner(ctx, opts)
				if err != nil {
					return nil, err
				}
				return &packageShutdownRunner{CompanionRunner: runner, shutdown: func(shutdownCtx context.Context) error {
					shutdownCalled = true
					cancel() // Cleanup must outlive cancellation of the package request.
					if shutdownCtx.Err() != nil {
						t.Fatalf("cleanup inherited request cancellation: %v", shutdownCtx.Err())
					}
					deadline, ok := shutdownCtx.Deadline()
					if remaining := time.Until(deadline); !ok || remaining < 119*time.Second || remaining > 2*time.Minute {
						t.Fatalf("cleanup deadline is not two minutes: %v (set=%v)", remaining, ok)
					}
					if tc.shutdownErr == nil {
						return nil
					}
					// Match the wrapping performed by the Docker runner.
					return wool.Get(shutdownCtx).In("Docker.Shutdown").Wrapf(tc.shutdownErr, "cannot remove container")
				}}, nil
			}
			target := &builderv0.PackageTarget{Os: runtime.GOOS, Architecture: runtime.GOARCH}
			err := packageCrossGoBinaryWithRunner(ctx, source, ".", destination, target, factory)
			if !shutdownCalled {
				t.Fatalf("build never reached cleanup: %v", err)
			}
			wantWarning := !tc.buildFails && errors.Is(tc.shutdownErr, context.DeadlineExceeded)
			wantError := tc.buildFails || (tc.shutdownErr != nil && !wantWarning)
			if (err != nil) != wantError {
				t.Fatalf("package error = %v, want error = %v", err, wantError)
			}
			if tc.buildFails && !strings.Contains(err.Error(), "undefined: missingPackageSymbol") {
				t.Fatalf("lost compiler failure: %v", err)
			}
			if wantError && tc.shutdownErr != nil && !errors.Is(err, tc.shutdownErr) {
				t.Fatalf("lost cleanup failure: %v", err)
			}
			if !tc.buildFails {
				build, readErr := buildinfo.ReadFile(destination)
				if readErr != nil || build.Path != "example.test/package-shutdown" {
					t.Fatalf("missing packaged executable: %v, info=%v", readErr, build)
				}
				output, runErr := exec.CommandContext(t.Context(), destination).CombinedOutput()
				if runErr != nil || string(output) != "42\n" {
					t.Fatalf("packaged executable: %v, output=%q", runErr, output)
				}
			}
			logs.Lock()
			defer logs.Unlock()
			if wantWarning {
				message := fmt.Sprintf("go package %s/%s succeeded; timed out shutting down container %s", target.Os, target.Architecture, containerName)
				if len(logs.warnings) != 1 || !strings.Contains(logs.warnings[0], message) || !strings.Contains(logs.warnings[0], context.DeadlineExceeded.Error()) {
					t.Fatalf("missing container/timeout warning: %v", logs.warnings)
				}
			} else if len(logs.warnings) != 0 {
				t.Fatalf("unexpected cleanup warning: %v", logs.warnings)
			}
		})
	}
}
