package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/resources"
	golanghelpers "github.com/codefly-dev/core/runners/golang"

	goruntime "github.com/codefly-dev/service-go/pkg/runtime"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// A native Start reports what it started (runtime-started-artifact/v1), and
// cli#993 approves exactly that build: it digests the reported executable as
// its SHA-256 and refuses a workload whose agent reports nothing. So these
// tests assert the three things the consumer depends on — an artifact is
// reported, the file it names is the program that actually ran, and a failed
// start reports none — against a real native build and a real process.

// startedService is a Go service whose running process identifies itself: it
// writes its compiled-in token into `ran.token` beside its source, then blocks
// so the runtime supervises a live process rather than a corpse. The token is
// a constant, so it is a property of the executable's bytes, which is what
// makes it usable as evidence about which build ran.
func startedService(token string) string {
	return fmt.Sprintf(`package main

import (
	"os"
	"time"
)

const token = %q

func main() {
	if err := os.WriteFile("ran.token", []byte(token), 0o644); err != nil {
		panic(err)
	}
	time.Sleep(10 * time.Minute)
}
`, token)
}

// nativeRuntime is a Runtime on a real native Go runner over root, with the
// runtime context resolved the way Init resolves it.
func nativeRuntime(t *testing.T, ctx context.Context, root string) *goruntime.Runtime {
	t.Helper()
	runner, err := golanghelpers.NewNativeGoRunner(ctx, root, ".")
	if err != nil {
		t.Fatalf("new native runner: %v", err)
	}
	runner.WithWorkspace(false)
	runner.WithLocalCacheDir(t.TempDir())
	if err := runner.Init(ctx); err != nil {
		t.Fatalf("runner init: %v", err)
	}
	t.Cleanup(func() { _ = runner.Shutdown(context.Background()) })

	svc := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"})
	svc.SourceLocation = root
	rt := goruntime.New(svc)
	rt.RunnerEnvironment = runner
	if err := rt.SetRuntimeContext(ctx, resources.NewRuntimeContextNative()); err != nil {
		t.Fatalf("set runtime context: %v", err)
	}
	t.Cleanup(func() { _, _ = rt.Stop(context.Background(), &runtimev0.StopRequest{}) })
	return rt
}

func writeService(t *testing.T, root, module, token string) {
	t.Helper()
	write(t, root, "go.mod", "module "+module+"\n\ngo 1.21\n")
	write(t, root, "main.go", startedService(token))
}

func write(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ranToken waits for the started process to write its token, and returns it.
// A started process is asynchronous, so the evidence has to be waited for
// rather than read once.
func ranToken(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "ran.token")
	deadline := time.Now().Add(30 * time.Second)
	for {
		body, err := os.ReadFile(path)
		if err == nil && len(body) > 0 {
			return strings.TrimSpace(string(body))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the started process never wrote %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tokenOf runs the executable at path in a directory of its own and returns the
// token it writes — the independent witness of which build that file holds.
func tokenOf(t *testing.T, ctx context.Context, path string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, path)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("running the reported executable: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return ranToken(t, dir)
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the reported executable: %v", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func startedArtifact(t *testing.T, resp *runtimev0.StartResponse) *runtimev0.StartedArtifact {
	t.Helper()
	if state := resp.GetStatus().GetState(); state != runtimev0.StartStatus_STARTED {
		t.Fatalf("start state = %s (%s), want STARTED", state, resp.GetStatus().GetMessage())
	}
	return resp.GetStatus().GetStartedArtifact()
}

// A native start reports an artifact, and the executable it names is the
// program that ran: the file at that path holds the same build as the process
// the runtime supervises. The digest is taken because that is what the
// consumer approves the build by.
func TestNativeStartReportsTheExecutableThatRan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	writeService(t, root, "example.com/started", "build-one")
	rt := nativeRuntime(t, ctx, root)

	resp, err := rt.Start(ctx, &runtimev0.StartRequest{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	artifact := startedArtifact(t, resp)
	if artifact == nil {
		t.Fatal("a native start reported no artifact: cli#993 refuses to deliver such a workload")
	}
	executable := artifact.GetExecutable()
	if !filepath.IsAbs(executable) {
		t.Fatalf("reported executable %q is not absolute; the contract reports absolute paths and the consumer resolves none", executable)
	}
	if _, err := os.Stat(executable); err != nil {
		t.Fatalf("reported executable is not a file the host can open: %v", err)
	}
	if sources := artifact.GetSources(); len(sources) != 0 {
		t.Errorf("sources = %v, want empty: a compiled runtime's executable IS the build", sources)
	}

	// The process that the runtime started wrote this token; the reported file,
	// run on its own, writes the same one. That is the whole claim.
	if ran, reported := ranToken(t, root), tokenOf(t, ctx, executable); ran != reported {
		t.Fatalf("the reported executable holds %q but the process that ran wrote %q", reported, ran)
	} else if ran != "build-one" {
		t.Fatalf("the started process wrote %q, want the compiled-in build-one", ran)
	}
	if sha256Of(t, executable) == "" {
		t.Error("the reported executable has no digest; the consumer approves the build by it")
	}
}

// A restart reports again, and reports the new build. Core consumes the report
// on each StartResponse, so an agent that reported only at its first start
// would have the previous process's build approved for the one now running.
func TestNativeRestartReportsTheRebuiltExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	writeService(t, root, "example.com/restarted", "build-one")
	rt := nativeRuntime(t, ctx, root)

	first, err := rt.Start(ctx, &runtimev0.StartRequest{})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	firstArtifact := startedArtifact(t, first)
	if firstArtifact == nil {
		t.Fatal("the first start reported no artifact")
	}
	if ran := ranToken(t, root); ran != "build-one" {
		t.Fatalf("first process wrote %q, want build-one", ran)
	}
	firstDigest := sha256Of(t, firstArtifact.GetExecutable())

	// Change what the service is, and restart it.
	if err := os.Remove(filepath.Join(root, "ran.token")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "main.go", startedService("build-two"))

	second, err := rt.Start(ctx, &runtimev0.StartRequest{})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	secondArtifact := startedArtifact(t, second)
	if secondArtifact == nil {
		t.Fatal("the restart reported no artifact: core consumed the first report, so silence here is a refusal downstream")
	}
	if ran := ranToken(t, root); ran != "build-two" {
		t.Fatalf("restarted process wrote %q, want build-two", ran)
	}
	if got := tokenOf(t, ctx, secondArtifact.GetExecutable()); got != "build-two" {
		t.Fatalf("the restart reported an executable holding %q, but build-two is what ran", got)
	}
	if secondDigest := sha256Of(t, secondArtifact.GetExecutable()); secondDigest == firstDigest {
		t.Error("the restart reported the first build's digest; the consumer would approve a build that is no longer running")
	}
}

// A start whose build failed reports nothing. There is no process, so there is
// no artifact — and emphatically not the previous build's executable, which is
// still on disk.
func TestFailedNativeStartReportsNoArtifact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	writeService(t, root, "example.com/broken", "build-one")
	rt := nativeRuntime(t, ctx, root)

	first, err := rt.Start(ctx, &runtimev0.StartRequest{})
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	firstArtifact := startedArtifact(t, first)
	if firstArtifact == nil {
		t.Fatal("the first start reported no artifact")
	}
	previous := firstArtifact.GetExecutable()

	write(t, root, "main.go", "package main\n\nfunc main() { this is not go }\n")

	resp, err := rt.Start(ctx, &runtimev0.StartRequest{})
	if err != nil {
		t.Fatalf("Start returned a transport error, want a failed StartStatus: %v", err)
	}
	if state := resp.GetStatus().GetState(); state != runtimev0.StartStatus_ERROR {
		t.Fatalf("start state = %s, want ERROR on a build that does not compile", state)
	}
	if artifact := resp.GetStatus().GetStartedArtifact(); artifact != nil {
		t.Fatalf("a failed start reported %v; nothing started, so nothing may be approved", artifact)
	}
	if _, err := os.Stat(previous); err != nil {
		t.Fatalf("the previous executable should still be on disk, simply not this attempt's answer: %v", err)
	}
}
