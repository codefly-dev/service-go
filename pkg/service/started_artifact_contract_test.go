package service_test

import (
	"context"
	"slices"
	"testing"

	"github.com/codefly-dev/core/agents/contract"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"

	goservice "github.com/codefly-dev/service-go/pkg/service"
)

// TestAdvertisementAdoptsRuntimeStartedArtifact: Runtime.Start reports what it
// started, so this agent advertises runtime-started-artifact/v1. Core's shared
// contract deliberately does not advertise it for every agent built on this
// core — an agent that never calls ReportStartedArtifact and claims it anyway
// is the false promise a consumer then trusts — so the claim is made here, and
// is true only as long as Start keeps reporting (pkg/runtime's
// started_artifact_test.go holds that end).
func TestAdvertisementAdoptsRuntimeStartedArtifact(t *testing.T) {
	info, err := goservice.New(&resources.Agent{Kind: "codefly:service", Name: "go"}).GetAgentInformation(context.Background(), &agentv0.AgentInformationRequest{})
	if err != nil {
		t.Fatalf("GetAgentInformation: %v", err)
	}
	if !slices.Contains(info.GetContract().GetCapabilities(), contract.RuntimeStartedArtifact) {
		t.Fatalf("contract capabilities %v do not advertise %s", info.GetContract().GetCapabilities(), contract.RuntimeStartedArtifact)
	}
	if err := contract.Check(info.GetContract(), contract.RuntimeStartedArtifact); err != nil {
		t.Fatalf("the advertised contract does not satisfy %s: %v", contract.RuntimeStartedArtifact, err)
	}
	// The capability this agent already claimed must survive beside the new one.
	if !slices.Contains(info.GetContract().GetCapabilities(), contract.DeploymentCompositionProvenance) {
		t.Fatalf("contract capabilities %v dropped %s", info.GetContract().GetCapabilities(), contract.DeploymentCompositionProvenance)
	}
}
