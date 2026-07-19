package service_test

import (
	"testing"

	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	goservice "github.com/codefly-dev/service-go/pkg/service"
)

func TestValidationCapabilitiesAreLocalFirstPluginOperations(t *testing.T) {
	validation := goservice.ValidationCapabilities()
	if validation == nil {
		t.Fatal("validation capabilities are absent")
	}
	for name, operation := range map[string]*agentv0.ValidationOperationCapability{
		"lint":           validation.GetLint(),
		"compile":        validation.GetCompile(),
		"audit":          validation.GetAudit(),
		"artifact_build": validation.GetArtifactBuild(),
		"sbom":           validation.GetSbom(),
		"source_package": validation.GetSourcePackage(),
		"sync":           validation.GetSync(),
	} {
		if !operation.GetSupported() {
			t.Fatalf("%s is not advertised", name)
		}
		if len(operation.GetScopes()) != 1 || operation.GetScopes()[0] != agentv0.ValidationScope_VALIDATION_SCOPE_WORKSPACE {
			t.Fatalf("%s scopes = %v, want workspace", name, operation.GetScopes())
		}
	}
	if !validation.GetTest().GetSupported() {
		t.Fatal("test is not advertised")
	}
	suites := validation.GetTest().GetSuites()
	if len(suites) != 1 || suites[0].GetName() != "unit" || !suites[0].GetDefaultSuite() || suites[0].GetDependencyMode() != agentv0.TestDependencyMode_TEST_DEPENDENCY_MODE_NONE {
		t.Fatalf("test suites = %v, want default dependency-free unit suite", suites)
	}
}
