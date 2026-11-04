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
