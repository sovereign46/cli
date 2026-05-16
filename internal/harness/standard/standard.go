package standard

import (
	"context"

	"github.com/sovereign46/s46-cli/internal/harness"
)

type Adapter struct{}

func New() Adapter { return Adapter{} }

func (a Adapter) Name() string { return "standard" }

func (a Adapter) Detect(ctx context.Context, env map[string]string) (harness.Detection, error) {
	return harness.Detection{Installed: true}, nil
}

func (a Adapter) PlanConnect(ctx context.Context, req harness.ConnectRequest) (harness.Plan, error) {
	return harness.Plan{
		Harness: "standard",
		Title:   "Configure direct s46 runner",
		Env:     req.Env,
		Summary: "harness: s46 (default direct runner; no third-party harness config)",
		Operations: []string{
			"store team, lane, endpoint, default model, and direct-runner harness in s46 config",
			"no external harness files will be written",
		},
		Files: nil,
	}, nil
}

func (a Adapter) ApplyConnect(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.AppliedPlan{Plan: plan}, nil
}
