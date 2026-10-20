package models

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"fmt"
	"maps"
	"strings"
	"time"

	attest "github.com/sovereign46/attest"
)

// No production policy has been independently approved yet. Keep this asset
// empty until that approval; never populate it from registry/receipt evidence.
//
//go:embed policy/activation-root.json
var bundledActivationPolicy string

func prepareActivation(request InstallRequest) (InstallRequest, error) {
	body := request.trustedPolicy
	if body == nil {
		body = fixtureActivationPolicy(request.Env)
	}
	if body == nil {
		body = []byte(bundledActivationPolicy)
	}
	if len(body) > metadataMaxBytes {
		return request, fmt.Errorf("model activation policy exceeds %d bytes", metadataMaxBytes)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return request, fmt.Errorf("independently approved bundled model activation policy unavailable; model installation and cached activation are disabled")
	}
	root, err := attest.ParseTrustRoot(body)
	if err != nil {
		return request, fmt.Errorf("decode independent model activation policy: %w", err)
	}
	// attest permits signer-only roots for non-transparency uses. Model
	// activation requires a complete transparency policy before any I/O.
	if strings.TrimSpace(root.Sigsum.Policy) == "" && len(root.Sigsum.Logs) == 0 {
		return request, fmt.Errorf("independent model activation policy lacks transparency policy")
	}
	keys, err := trustedKeys(request.Env)
	if err != nil {
		return request, err
	}
	for id, key := range request.trustedKeys {
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	request.trustedKeys = keys
	request.activationRoot = root
	request.trustedPolicy = nil
	request.Env = maps.Clone(request.Env)
	clock := request.now
	if clock == nil {
		clock = time.Now
	}
	// Calls are sequential within an invocation. Reject clock rollback rather
	// than freezing freshness at the last observation. Failure is permanent
	// for this invocation, even if the clock subsequently recovers.
	var latest time.Time
	var clockErr error
	request.activationTime = func() (time.Time, error) {
		if clockErr != nil {
			return time.Time{}, clockErr
		}
		now := clock().UTC()
		if now.Before(latest) {
			clockErr = fmt.Errorf("model activation clock moved backward")
			return time.Time{}, clockErr
		}
		latest = now
		return now, nil
	}
	if err := validateTrustRootPinned(root, request); err != nil {
		return request, fmt.Errorf("independent model activation policy: %w", err)
	}
	return request, nil
}
