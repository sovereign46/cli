package api

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	PrelaunchReleaseChannel          = "prelaunch"
	MaxRuntimeClosureSizeBytes int64 = 64 << 20
)

// ValidateTeamReleaseMetadata rejects partial or unsupported release-channel
// claims before they can be persisted or used to configure a harness.
func ValidateTeamReleaseMetadata(team Team, now time.Time) error {
	if team.ReleaseChannel == "" {
		if team.HasReleaseMetadata() {
			return fmt.Errorf("team response contains release metadata without a release channel")
		}
		if TeamRequiresPrelaunchMetadata(team) {
			return fmt.Errorf("team response containing %s requires complete prelaunch release metadata", DeepSeekModelID)
		}
		return nil
	}
	if team.ReleaseChannel != PrelaunchReleaseChannel {
		return fmt.Errorf("team response has unsupported release channel %q", team.ReleaseChannel)
	}
	if team.ProductionCertified == nil || team.ProductionAPIPath == nil {
		return fmt.Errorf("prelaunch team response must explicitly deny production certification and the production API path")
	}
	if *team.ProductionCertified || *team.ProductionAPIPath {
		return fmt.Errorf("prelaunch team response must not claim production certification or the production API path")
	}
	if !validPrefixedSHA256(team.PolicySHA256) {
		return fmt.Errorf("prelaunch team response has an invalid policy digest")
	}
	policyExpiry, err := parseCanonicalFutureTime("policy", team.PolicyExpiresAt, now)
	if err != nil {
		return err
	}
	credentialExpiry, err := parseCanonicalFutureTime("upstream credential", team.UpstreamCredentialExpiresAt, now)
	if err != nil {
		return err
	}
	if credentialExpiry.After(policyExpiry) {
		return fmt.Errorf("prelaunch upstream credential expires after the route policy")
	}
	if team.DefaultModel == "" || !slices.Contains(team.Models, team.DefaultModel) {
		return fmt.Errorf("prelaunch team response default model is not in its authorized catalog")
	}
	if len(team.ModelMetadata) != len(team.Models) {
		return fmt.Errorf("prelaunch team response model metadata does not exactly match its authorized catalog")
	}
	seenModels := make(map[string]bool, len(team.Models))
	for _, id := range team.Models {
		if id == "" || seenModels[id] {
			return fmt.Errorf("prelaunch team response has an empty or duplicate model id")
		}
		seenModels[id] = true
		runtime, ok := team.ModelMetadata[id]
		if !ok || !runtime.HasValidTokenLimits() {
			return fmt.Errorf("prelaunch team response has invalid runtime metadata for %s", id)
		}
		if !validRuntimeIdentityValue(runtime.Engine) || !validRuntimeIdentityValue(runtime.Version) || runtime.Reasoning == nil {
			return fmt.Errorf("prelaunch team response must explicitly identify engine, version, and reasoning for %s", id)
		}
		if id == DeepSeekModelID {
			// Closure identity alone cannot justify Pi's runtime-specific
			// reasoning mapping. The protected prelaunch policy must bind and
			// return all three fields explicitly.
			if runtime.Engine != "vllm" || runtime.Version != "0.31.0" || !*runtime.Reasoning {
				return fmt.Errorf("prelaunch team response must identify DeepSeek as vLLM 0.31.0 with reasoning enabled")
			}
			if !ValidRuntimeClosureBinding(runtime) {
				return fmt.Errorf("prelaunch team response has an invalid DeepSeek runtime closure")
			}
		}
	}
	for id := range team.ModelMetadata {
		if !seenModels[id] {
			return fmt.Errorf("prelaunch team response has metadata outside its authorized catalog")
		}
	}
	return nil
}

func parseCanonicalFutureTime(name string, raw string, now time.Time) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil || parsed.Nanosecond() != 0 || raw != parsed.UTC().Format(time.RFC3339) {
		return time.Time{}, fmt.Errorf("prelaunch %s expiry is not canonical UTC", name)
	}
	if !now.Before(parsed) {
		return time.Time{}, fmt.Errorf("prelaunch %s has expired", name)
	}
	return parsed, nil
}

// TeamRequiresPrelaunchMetadata identifies the currently protected catalog.
// Future production support must introduce a distinct, explicit classified
// contract instead of silently treating this model as ordinary cloud.
func TeamRequiresPrelaunchMetadata(team Team) bool {
	if team.DefaultModel == DeepSeekModelID || slices.Contains(team.Models, DeepSeekModelID) {
		return true
	}
	_, present := team.ModelMetadata[DeepSeekModelID]
	return present
}

func validPrefixedSHA256(value string) bool {
	digest, ok := strings.CutPrefix(value, "sha256:")
	return ok && validLowerSHA256(digest) && !periodicHexDigest(digest)
}

func validRuntimeIdentityValue(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._+-"
	for _, character := range []byte(value) {
		if !strings.ContainsRune(allowed, rune(character)) {
			return false
		}
	}
	return true
}

// ValidRuntimeClosureBinding checks the bounded digest identity required by
// clients before configuring the protected DeepSeek runtime.
func ValidRuntimeClosureBinding(runtime ModelRuntime) bool {
	return runtime.ClosureSizeBytes > 0 && runtime.ClosureSizeBytes <= MaxRuntimeClosureSizeBytes && validLowerSHA256(runtime.ClosureSHA256) && !periodicHexDigest(runtime.ClosureSHA256)
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range []byte(value) {
		if !('0' <= character && character <= '9' || 'a' <= character && character <= 'f') {
			return false
		}
	}
	return true
}

func periodicHexDigest(digest string) bool {
	for period := 1; period < len(digest); period++ {
		if len(digest)%period != 0 {
			continue
		}
		periodic := true
		for index := period; index < len(digest); index++ {
			if digest[index] != digest[index%period] {
				periodic = false
				break
			}
		}
		if periodic {
			return true
		}
	}
	return false
}
