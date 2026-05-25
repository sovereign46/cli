package standard

import (
	"context"

	"github.com/sovereign46/cli/internal/harness"
	"github.com/sovereign46/cli/internal/share"
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
		Summary: "harness: s46 (direct runner; no third-party harness config)",
		Operations: []string{
			"store team, region, endpoint, default model, and direct-runner harness in s46 config",
			"no external harness files will be written",
		},
		Files: nil,
	}, nil
}

func (a Adapter) PlanDisconnect(ctx context.Context, req harness.DisconnectRequest) (harness.Plan, error) {
	return harness.Plan{
		Harness:    "standard",
		Title:      "Disconnect direct s46 runner",
		Env:        req.Env,
		Summary:    "harness: s46 (no third-party harness config)",
		Operations: []string{"remove team/default harness from s46 config only"},
	}, nil
}

func (a Adapter) Apply(ctx context.Context, plan harness.Plan) (harness.AppliedPlan, error) {
	return harness.AppliedPlan{Plan: plan}, nil
}

func (a Adapter) Status(ctx context.Context, req harness.StatusRequest) []harness.StatusCheck {
	return []harness.StatusCheck{{Name: "standard", OK: true, Message: "no third-party harness config required"}}
}

func (a Adapter) ShareArtifact(ctx context.Context, req harness.ShareRequest) (share.Artifact, bool, error) {
	return share.Artifact{}, false, nil
}
