package main

import (
	"testing"

	agenttesting "github.com/codefly-dev/core/agents/testing"
	gobuilder "github.com/codefly-dev/service-go/pkg/builder"
)

func TestDeploymentTemplates(t *testing.T) {
	agenttesting.AssertKustomizeTemplates(t, deploymentFS, gobuilder.NewDeploymentParameters())
}
