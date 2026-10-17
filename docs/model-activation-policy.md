# Independent model activation policy

## Implemented mechanism; production authority unavailable

The CLI loads a complete policy embedded at `internal/models/policy/activation-root.json` before model-registry requests, receipt reads, or artifact access in `Install` and `VerifyInstalled`. **That asset is intentionally empty: no real complete production policy has been independently approved.** Model installation and cached activation therefore fail closed with an explicit unavailable-policy error. This also gates the model-verification portion of airplane setup. Devstral's model selection, llama.cpp settings, and harness restoration behavior are unchanged.

Do not populate the asset from a registry response, a receipt, or a generated test root. Approval must cover the entire policy, including release signing identities and keys, Sigsum logs, witnesses/quorum or embedded Sigsum policy, submit-key authorizations, revocations, transparency status, and expiry. Matching the release signer alone does not authenticate these other fields. Approval and provisioning of that production artifact remain separate release prerequisites.

## Verification behavior

- The release binary has no file or environment policy override. It does not request `/trust/v1/root.json` and never falls back to origin or receipt policy.
- Policy is bounded and parsed with `attest.ParseTrustRoot`; signer-only roots are insufficient. Pinned signer identity/key checks use `attest.ValidateActivationTrust`.
- Each invocation snapshots the independent policy and signer pins. Callbacks cannot replace that invocation's authority. Any observed backward clock movement permanently refuses that invocation; freshness is never frozen at an earlier observation.
- Every manifest, audit index, and advisory index is authenticated in production+strict mode under that policy. Activation checks enforce current trust and fresh witness evidence, including after artifact download or checksum work.
- Cached verification authenticates the stored evidence under the current binary's policy without network access. `Receipt.TrustRoot` is ignored; newly written receipts omit it. Missing policy is an error even for an existing receipt.
- Signed audit/advisory refusals still invalidate only the matching cached receipt. Warning allowance remains request-only, and valid artifact bytes can be reused when evidence is refreshed.

A bundled policy is not a live revocation feed. Newly approved policy changes require a new CLI build; scheduled revocations and expiry in the bundled policy are checked at activation time. Offline verification does not discover new registry advisories or externally issued revocations. None of these mechanisms proves that production approval, certification, deployment, or live model/tool-loop qualification has occurred.

## Generated test fixtures

In-package tests inject complete generated public policies privately per request, without global policy replacement. Cross-package fixtures in **non-release builds only** may supply complete JSON in `S46_MODELS_ACTIVATION_POLICY`, alongside the existing `S46_MODELS_KEY_ID`/`S46_MODELS_PUBLIC_KEY` signer seam. This is inline public data, not a filename or URL. Release builds ignore these environment overrides. Generated fixture keys and policies are never production authority.
