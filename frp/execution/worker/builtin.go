package worker

import (
	"context"
	"errors"
	"github.com/temporality-project/temporality/frp/affordance"
	"github.com/temporality-project/temporality/frp/execution"
	"os"
)

type InspectEnvironmentWorkflow struct{}

func (InspectEnvironmentWorkflow) Plan(request affordance.Request) (execution.Plan, error) {
	path, ok := request.Arguments["path"].(string)
	if !ok || path == "" {
		return execution.Plan{}, errors.New("path argument is required")
	}
	return execution.Plan{Steps: []execution.Step{{ID: "inspect-path", Capability: "filesystem.read", Operation: "stat", Input: map[string]any{"path": path}}}}, nil
}

type SafeAdapter struct{}

func (SafeAdapter) Execute(_ context.Context, step execution.Step) (map[string]any, error) {
	if step.Capability != "filesystem.read" || step.Operation != "stat" {
		return nil, errors.New("operation is not supported by safe adapter")
	}
	path, ok := step.Input["path"].(string)
	if !ok || path == "" {
		return nil, errors.New("path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "exists": true, "directory": info.IsDir(), "size": info.Size()}, nil
}
