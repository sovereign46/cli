package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	modelspkg "github.com/sovereign46/cli/internal/models"
)

type modelsVerifyOptions struct {
	BackendModel string
	Output       string
	Yes          bool
}

type modelsVerifyResult struct {
	ModelID      string                   `json:"modelId"`
	Version      string                   `json:"version,omitempty"`
	BackendModel string                   `json:"backendModel,omitempty"`
	Path         string                   `json:"path"`
	ArtifactURL  string                   `json:"artifactUrl,omitempty"`
	RegistryURL  string                   `json:"registryUrl"`
	EvidenceURL  string                   `json:"evidenceUrl"`
	Warnings     []modelspkg.ModelWarning `json:"warnings,omitempty"`
}

func modelsCommand(runtime Runtime, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "verify attested model registry artifacts"}
	var verifyOptions modelsVerifyOptions
	verify := &cobra.Command{
		Use:   "verify <model-id>",
		Short: "download or verify an attested model artifact",
		Args:  exactArgs("s46 models verify <model-id>", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := newApp(runtime, opts)
			if err != nil {
				return err
			}
			return runModelsVerify(cmd.Context(), app, args[0], verifyOptions)
		},
	}
	verify.Flags().StringVar(&verifyOptions.BackendModel, "backend-model", "", "expected backend model id")
	verify.Flags().StringVar(&verifyOptions.Output, "output", "", "artifact path to verify or download; defaults to the s46 model cache")
	verify.Flags().BoolVar(&verifyOptions.Yes, "yes", false, "accept model advisory warnings non-interactively")
	cmd.AddCommand(verify)
	return cmd
}

func runModelsVerify(ctx context.Context, app *app, modelID string, options modelsVerifyOptions) error {
	registryURL := modelspkg.BaseURL(app.runtime.Env)
	result := modelsVerifyResult{ModelID: modelID, Path: strings.TrimSpace(options.Output), RegistryURL: registryURL, EvidenceURL: modelsEvidenceURL(registryURL)}
	printedEvidence := false

	if !app.options.machineReadable() {
		if err := app.renderer.Lines(
			"[s46] verifying s46-attest model attestation, trust root, advisory index, and artifact...",
			fmt.Sprintf("[s46] model: %s", modelID),
		); err != nil {
			return err
		}
	}

	install := func(allowWarnings bool) error {
		return modelspkg.Install(ctx, modelspkg.InstallRequest{
			Env:           app.runtime.Env,
			ModelID:       modelID,
			BackendModel:  options.BackendModel,
			TargetPath:    options.Output,
			AllowWarnings: allowWarnings,
			OnResolve: func(resolution modelspkg.InstallResolution) {
				result.Path = resolution.TargetPath
				result.RegistryURL = resolution.RegistryURL
				result.EvidenceURL = resolution.EvidenceURL
				result.Version = resolution.Manifest.Version
				result.BackendModel = resolution.Manifest.BackendModel
				result.ArtifactURL = resolution.Manifest.URL
				result.Warnings = resolution.Warnings
				if !app.options.machineReadable() && !printedEvidence {
					printedEvidence = true
					_ = app.renderer.Lines(fmt.Sprintf("[s46] signed run: %s", resolution.EvidenceURL))
				}
			},
		})
	}

	if err := install(options.Yes); err != nil {
		var warningsErr modelspkg.WarningsError
		if errors.As(err, &warningsErr) && !options.Yes {
			result.Warnings = warningsErr.Warnings
			if !app.canPrompt() {
				return fmt.Errorf("verify model %s: %w", modelID, err)
			}
			for _, warning := range warningsErr.Warnings {
				line := fmt.Sprintf("[s46] warning: %s", warning.Message)
				if strings.TrimSpace(warning.URL) != "" {
					line += " (" + warning.URL + ")"
				}
				if err := app.renderer.Lines(line); err != nil {
					return err
				}
			}
			yes, promptErr := promptYesNo(app, "[s46] Continue with this model? [y/N] ", false)
			if promptErr != nil {
				return promptErr
			}
			if !yes {
				return fmt.Errorf("model verification canceled")
			}
			if retryErr := install(true); retryErr != nil {
				return fmt.Errorf("verify model %s: %w", modelID, retryErr)
			}
		} else {
			return fmt.Errorf("verify model %s: %w", modelID, err)
		}
	}

	if ok, err := app.writeStructured(result); ok {
		return err
	}
	lines := []string{
		fmt.Sprintf("[s46] verified: %s", modelID),
		fmt.Sprintf("[s46] artifact: %s", result.Path),
		fmt.Sprintf("[s46] registry: %s", result.RegistryURL),
		fmt.Sprintf("[s46] signed run: %s", result.EvidenceURL),
	}
	for _, warning := range result.Warnings {
		lines = append(lines, fmt.Sprintf("[s46] warning accepted: %s", warning.Message))
	}
	return app.renderer.Lines(lines...)
}

func modelsEvidenceURL(registryURL string) string {
	base := strings.TrimRight(registryURL, "/")
	if strings.HasSuffix(base, "/models/v1") {
		return strings.TrimSuffix(base, "/models/v1") + "/audit/v1/"
	}
	return "https://models.s46.dev/audit/v1/"
}
