//go:build !release

package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sovereign46/cli/internal/api"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/harness"
)

type teamDiscoveryFunc func(context.Context, string, api.TeamOptions) (api.Team, error)

func (f teamDiscoveryFunc) Team(ctx context.Context, name string, opts api.TeamOptions) (api.Team, error) {
	return f(ctx, name, opts)
}

func TestFreshProductionDeepSeekDiscoveryFailsClosed(t *testing.T) {
	saved := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
	certified := true
	saved.ReleaseChannel = api.ProductionReleaseChannel
	saved.ProductionCertified = &certified
	saved.ProductionAPIPath = &certified
	saved.PolicySHA256 = ""
	saved.PolicyExpiresAt = ""
	saved.UpstreamCredentialExpiresAt = ""
	now := time.Now().UTC()
	if err := api.ValidateTeamReleaseMetadata(saved, now); err != nil {
		t.Fatal(err)
	}

	withoutGrant := api.Team{
		Name: saved.Name, Endpoint: api.DefaultGatewayURL,
		DefaultModel: api.DefaultModel, Models: []string{api.DefaultModel},
		ModelMetadata: map[string]api.ModelRuntime{api.DefaultModel: {
			ContextWindow: 8192, MaxTokens: 1024,
		}},
	}
	changedRelease := saved
	changedRelease.ModelMetadata = map[string]api.ModelRuntime{}
	for model, runtime := range saved.ModelMetadata {
		runtime.ClosureSHA256 = "a" + runtime.ClosureSHA256[1:]
		changedRelease.ModelMetadata[model] = runtime
	}
	wrongTeam := saved
	wrongTeam.Name = "@s46/other"

	for _, tc := range []struct {
		name string
		team api.Team
		err  error
		want string
	}{
		{name: "current release", team: saved},
		{name: "downgraded", team: testPrelaunchDeepSeekTeam(api.DefaultGatewayURL), want: "does not authorize production DeepSeek"},
		{name: "grant removed", team: withoutGrant, want: "does not authorize production DeepSeek"},
		{name: "release changed", team: changedRelease, want: "release differs"},
		{name: "wrong team", team: wrongTeam, want: "instead of"},
		{name: "discovery error", err: errors.New("unavailable"), want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			discovery := teamDiscoveryFunc(func(_ context.Context, name string, opts api.TeamOptions) (api.Team, error) {
				calls++
				if name != saved.Name || opts.AccessToken != "test-bearer" || opts.Endpoint != "" || opts.Region != "" || opts.DefaultModel != "" {
					t.Fatalf("team discovery used inexact request: name=%q opts=%#v", name, opts)
				}
				return tc.team, tc.err
			})
			err := verifyCurrentProductionDeepSeek(context.Background(), discovery, saved.Name, saved, "test-bearer", now)
			if calls != 1 {
				t.Fatalf("team discovery calls = %d, want 1", calls)
			}
			if tc.want == "" && err != nil {
				t.Fatalf("current production release refused: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("expected %q refusal, got %v", tc.want, err)
			}
		})
	}

	called := false
	discovery := teamDiscoveryFunc(func(context.Context, string, api.TeamOptions) (api.Team, error) {
		called = true
		return saved, nil
	})
	if err := verifyCurrentProductionDeepSeek(context.Background(), discovery, saved.Name, saved, "", now); err == nil || called {
		t.Fatalf("missing token reached team discovery: err=%v called=%v", err, called)
	}

	result, err := connectResult(saved, "codex", api.DeepSeekModelID, config.ModeCloud, harness.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if result["releaseChannelAtConnect"] != api.ProductionReleaseChannel ||
		result["productionCertifiedAtConnect"] != true || result["productionAPIPathAtConnect"] != true {
		t.Fatalf("connect did not attribute production classification to discovery: %#v", result)
	}
	for _, field := range []string{"releaseChannel", "productionCertified", "productionAPIPath"} {
		if _, present := result[field]; present {
			t.Fatalf("connect emitted timeless production field %s: %#v", field, result)
		}
	}
	var output bytes.Buffer
	env := testEnv(t)
	app, err := newApp(Runtime{Env: env, Stdout: &output}, &options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := renderConnectApplied(app, saved, config.ModeCloud, harness.Plan{}, harness.AppliedPlan{}, result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "team discovery at connect: production-certified") {
		t.Fatalf("connect rendered a timeless production badge: %s", output.String())
	}
}

func TestNonPiDeepSeekTokenHelperRequiresFreshTeamDiscovery(t *testing.T) {
	env := testEnv(t)
	requireOK(t, run(t, env, "login", "--email", "dscape@s46.dev"))
	team := testPrelaunchDeepSeekTeam(api.DefaultGatewayURL)
	certified := true
	team.ReleaseChannel = api.ProductionReleaseChannel
	team.ProductionCertified = &certified
	team.ProductionAPIPath = &certified
	team.PolicySHA256 = ""
	team.PolicyExpiresAt = ""
	team.UpstreamCredentialExpiresAt = ""
	store := config.NewStore(env, "")
	if err := store.SaveConfig(config.Config{
		ActiveTeam: team.Name,
		Mode:       config.ModeCloud,
		Teams: map[string]config.TeamConfig{
			team.Name: config.TeamConfigFromAPI(team, "codex", team.DefaultModel, config.ModeCloud),
		},
	}); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(Runtime{Env: env}, &options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	discovery := teamDiscoveryFunc(func(_ context.Context, name string, opts api.TeamOptions) (api.Team, error) {
		calls++
		if name != team.Name || opts.AccessToken == "" {
			t.Fatalf("token helper used an unauthenticated team request: name=%q opts=%#v", name, opts)
		}
		return testPrelaunchDeepSeekTeam(api.DefaultGatewayURL), nil
	})
	if err := validateCurrentProductionTokenHelper(context.Background(), app, discovery); err == nil ||
		!strings.Contains(err.Error(), "current team discovery does not authorize production DeepSeek") || calls != 1 {
		t.Fatalf("token helper accepted revoked production authorization: err=%v calls=%d", err, calls)
	}
}
