//go:build !agent_examples

package main

import (
	"net/http"

	"github.com/temporality-project/temporality/controlplane"
	"github.com/temporality-project/temporality/kernel/agent"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

func registerExampleWorkflow(worker.Worker) {}
func registerExampleRoutes(*http.ServeMux, client.Client, string, *agent.Activities, *controlplane.Gate) {
}
