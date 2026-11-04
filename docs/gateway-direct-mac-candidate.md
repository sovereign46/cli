# Gateway 0.3.1 direct Mac build candidate

This is an unsigned **airplane Gateway build candidate** for Darwin/arm64.
It has no release-authority, signing, publication, or model-certification
capability. The historical Gateway 0.2.0 release and qualification procedure is
unchanged.

`scripts/gateway_direct_mac.py` pins API commit
`2e2230aa575b8332d9d61bd1199536dde855ebd0`, Git tree
`e612f0c87e0a9f70caab4a9a29e2185b34de0ef5`, and its exact
`git archive --format=tar` SHA-256
`82940ebd45205c3cdd5cf9c14c72fceac1c5e866f01b224456582e3d50149752`.
It pins the official Go 1.26.10 Darwin/arm64 module ZIP SHA-256
`afb16ae24c172f008db87119f34cf903a295bbad3fa526f7443914b0240b33c7`.
The producer copies and rehashes the ZIP in private scratch, uses that
extracted compiler, disables Go module resolution, builds from two fresh source
and cache roots, and requires byte-identical binaries and deterministic
archives. It does not invoke Docker.

```sh
python3 scripts/gateway_direct_mac.py build \
  --api-repository /absolute/path/to/s46-api \
  --toolchain-zip /absolute/path/to/v0.0.1-go1.26.10.darwin-arm64.zip \
  --output /absolute/private/absent-candidate-directory

python3 scripts/gateway_direct_mac.py verify \
  --bundle /absolute/private/candidate-directory
```

The output contains one canonical `gateway-direct-mac-build.candidate.json`
and two copies of `s46-gateway_0.3.1_darwin_arm64.tar.gz` under `first/`
and `second/`. The verifier checks the exact source/toolchain/build profile,
archive metadata, contained binary, byte digests, and absence of extra files.
Three runs on this Mac produced the same archive SHA-256
`ef9d322d2bb775ae065d712280b786841c149ed959acf70942c25c74e8f32d3a`.
That is same-host reproducibility evidence, not independent cross-host
provenance or an approved release.

The build disables Go module network resolution; it does not prove an OS egress
block or protected-host isolation. The earlier Pi, Claude Code, and Codex
hello-world diagnostic was for API commit `ecb5ca7` and an older unsigned
archive. Its passing result cannot qualify this corrected source or establish
loaded model bytes. A fresh, retained 0.3.1 client run and independently
reviewed isolation evidence are still needed. After the Gateway archive is
separately approved and installed through a qualified CLI manifest, the
existing `scripts/e2e-airplane-harnesses` exercises the normal installer and
all three clients without bypassing `qualified:false`.

Fred and Gonçalo must receive and sign the exact reviewed release and
qualification inputs under approved role-specific trust. This candidate tool
does not create either input or accept signatures; those authority and
publication decisions remain pending.

## Direct Mac approval verifier

`scripts/gateway_direct_mac_approval.py` checks a **separate** qualification
record before preparing one canonical approval subject. The qualification
record must use schema `s46.gateway-direct-mac-qualification.v1`, bind the
candidate record and Gateway archive SHA-256, and bind the exact Devstral GGUF
and llama.cpp archive/binary identities. Its `controls` object must explicitly
mark independent isolation review, loaded-process binding, cleanup, and the
Pi, Claude Code, and Codex client tests as passed. Its `evidence` object must
hash the private diagnostic's `README.md`, `result.json`, `run.py`,
`combined.py`, and `control/diagnostic.sb`, plus **separate** retained files
for independent isolation review and loaded-process binding. The verifier
checks all those local evidence bytes and checks the diagnostic result's exact
candidate/runtime hashes, isolation probe, three client exits, application
outputs, and cleanup. The existing diagnostic does not have the two separate
review/binding files and cannot qualify itself.

The subject also fixes the source commit/archive, Go toolchain archive,
Gateway archive/binary, qualification-record digest, and consent to publish
that exact archive as a local airplane adapter under `models.s46.dev/cli-assets`.
Both reviewers sign the **same subject bytes** in the
`s46-gateway-direct-mac-approval-v1` SSH signature namespace. The separate
`scripts/gateway-direct-mac-approval-trust.json` has no enrolled keys; an
authenticated trust decision must enroll distinct SSH Ed25519 public keys for
`fredoliveira` and `goncalossilva` before verification can pass. This trust is
not shared with model-release or historical CLI-release keys.

```sh
python3 scripts/gateway_direct_mac_approval.py prepare-subject \
  --bundle /absolute/private/candidate-directory \
  --qualification /absolute/private/qualification.json \
  --evidence-root /absolute/private/evidence-directory \
  --subject /absolute/private/absent-subject.json

# Each reviewer signs the same subject using their enrolled private SSH key.
ssh-keygen -Y sign -f /absolute/path/to/reviewer-key \
  -n s46-gateway-direct-mac-approval-v1 \
  < /absolute/private/subject.json > /absolute/private/approvals/REVIEWER.sig

python3 scripts/gateway_direct_mac_approval.py verify \
  --bundle /absolute/private/candidate-directory \
  --qualification /absolute/private/qualification.json \
  --evidence-root /absolute/private/evidence-directory \
  --subject /absolute/private/subject.json \
  --approvals /absolute/private/approvals
```

Use `fredoliveira.sig` and `goncalossilva.sig` as the two signature names.
Verification checks signed assertions and exact retained bytes; reviewers
must inspect the isolation and loaded-process evidence itself. Approval
verification does not edit `gateway-release.json`, publish bytes, or certify
the model runtime.
