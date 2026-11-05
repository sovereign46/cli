# Mac airplane CLI candidate profile

`scripts/cli_mac_airplane_candidate.py` prepares a separate, unsigned authority
candidate for the first Darwin/arm64 airplane CLI. Its tag is
`v0.3.1-mac.1`, distinct from the four-platform `v0.3.1` release ID. It has
one CLI archive and three Gateway approval assets: the exact subject signed by
Fred and Gonçalo and each reviewer's SSH signature. The stage command verifies
the complete retained Gateway build and diagnostic packet with the existing
direct Mac verifier, checks both enrolled purpose-scoped signatures, and
requires an independently supplied SHA-256 for the Mac CLI archive.

The bundle directory must contain exactly:

```text
s46_0.3.1_darwin_arm64.tar.gz
gateway-approval-subject.json
fredoliveira.sig
goncalossilva.sig
```

The subject is copied byte for byte from the reviewed Gateway handoff packet. It must
have SHA-256 `e913b6ca19523abb0a6326143ba55b48d00a7320491f7af735ce1820f452eb7c`,
which binds the reviewed qualification and diagnostic manifest as well as the
Gateway bytes. It must also
pin Gateway archive SHA-256
`ef9d322d2bb775ae065d712280b786841c149ed959acf70942c25c74e8f32d3a`
and binary SHA-256
`6abaef72f4c0975fc459db8f7cad89b88f99b04e970464685c07180518382ca2`.
The enrolled reviewer keys live in
`scripts/gateway-direct-mac-approval-trust.json`; its current empty slots
prevent staging.

```sh
python3 scripts/cli_mac_airplane_candidate.py \
  --commit FULL_CLI_COMMIT \
  --source-tree FULL_CLI_TREE \
  --bundle /absolute/private/mac-cli-bundle \
  --archive-sha256 INDEPENDENT_MAC_CLI_ARCHIVE_SHA256 \
  --gateway-bundle /absolute/private/gateway-build \
  --gateway-qualification /absolute/private/qualification.candidate.json \
  --gateway-evidence-root /absolute/private/diagnostic \
  --output /absolute/private/new-candidate-records
```

The output contains deterministic sequence-1 and sequence-2 candidate records,
their ETags, a stale-write probe, and the derived authority ID. The record's
`bundleSha256` is the SHA-256 of the canonical sorted asset inventory; its
annotation and asset entry separately pin the Mac CLI archive. This command
does not upload the candidate, create signatures, append a qualified record,
tag, publish, or enable `qualified:true`. The remaining direct Mac steps are in
[`cli-direct-mac-release.md`](cli-direct-mac-release.md). The existing
four-platform profile, updater, and release workflow remain unchanged.
