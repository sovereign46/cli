# Direct Mac CLI qualification and publication

The `v0.3.1-mac.1` authority record is one Darwin/arm64 CLI archive, separate
from the four-platform `v0.3.1` release. It first needs the exact signed Gateway
0.3.1 packet described in [`cli-direct-mac-candidate.md`](cli-direct-mac-candidate.md),
then a successful installer run of that archive with Pi, Claude Code, and Codex.
No release broker or dedicated Linux builder participates.

The current source manifest has `qualified:false`, and both Gateway and CLI
reviewer trust files have empty key slots. Publication remains blocked until
Gateway signatures, Devstral rights and certified model installation, and both
CLI reviewer signatures exist. Do not change the manifest to `qualified:true`
merely to make a test pass. The source commit containing that change must have
the reviewed Gateway 0.3.1 pins and must be rebuilt twice into identical
archives before staging its candidate.

## Stage the candidate

Create the four-file candidate bundle and run
`scripts/cli_mac_airplane_candidate.py` as documented in the candidate page.
Use `scripts/release_authority.py put-blob`, `create`, and `append` with the
stage-only SSH credential to upload those exact assets and records. Submit
the generated stale request with `raw` and require HTTP 412. Then `capture`
the public sequence-2 record and ETag and compare them byte for byte with the
generated files. The authority ID, record SHA-256, and ETag from that capture
are inputs to the installer run.

## Run the installed CLI

Run the committed `scripts/e2e-airplane-harnesses` on a Darwin/arm64 Mac under
a reviewed, externally enforced filesystem and egress-denial profile that
allows loopback. Set `S46_E2E_PROVENANCE=direct-mac`,
`S46_E2E_QUALIFICATION=1`, `S46_E2E_QUALIFICATION_SCOPE=airplane`,
`S46_E2E_NETWORK_ISOLATION=external`, and
`S46_E2E_FILESYSTEM_ISOLATION=external`. The runner records the real Darwin
version and hostname; no GitHub Actions run or environment approval values
are used in this mode.

Supply these exact inputs from the staged record and independently checked
files:

| Variable | Value |
| --- | --- |
| `S46_E2E_CANDIDATE`, `S46_E2E_CANDIDATE_SHA256` | Absolute Mac archive path and its independent digest |
| `S46_E2E_EXPECTED_VERSION`, `S46_E2E_EXPECTED_COMMIT`, `S46_E2E_SOURCE_TREE` | `0.3.1`, staged source commit and tree |
| `S46_E2E_RELEASE_AUTHORITY_ORIGIN`, `S46_E2E_RELEASE_AUTHORITY_ID` | Production release authority origin and staged ID |
| `S46_E2E_CANDIDATE_SEQUENCE`, `S46_E2E_CANDIDATE_ETAG`, `S46_E2E_CANDIDATE_RECORD_SHA256`, `S46_E2E_CANDIDATE_STALE_IF_MATCH_STATUS` | `2`, captured ETag, canonical record digest, observed `412` |
| `S46_E2E_ISOLATION_PROFILE_SHA256` | Independently checked digest of the enforcing OS profile |
| `S46_E2E_MODEL_GGUF`, `S46_E2E_MODEL_SHA256` | Exact signed Devstral GGUF and its pinned digest |
| `S46_E2E_LLAMA_ARCHIVE`, `S46_E2E_LLAMA_ARCHIVE_SHA256`, `S46_E2E_LLAMA_SERVER_SHA256`, `S46_E2E_LLAMA_INVENTORY_SHA256` | Reviewed llama.cpp b9270 archive and runtime identities |
| `S46_E2E_GATEWAY_ARCHIVE`, `S46_E2E_GATEWAY_ARCHIVE_SHA256`, `S46_E2E_GATEWAY_SHA256` | Reviewed Gateway 0.3.1 archive and binary identities |
| `S46_E2E_QUALIFICATION_EVIDENCE` | New private path ending `s46_0.3.1_darwin_arm64.tar.gz.qualification.json` |

The runner creates a private HOME and TMPDIR, installs the archived `s46`
through `s46 airplane setup --mode=on --harness=pi --yes`, configures Claude
Code and Codex, checks the installed model, llama.cpp, and Gateway bytes,
and requires Pi, Claude Code, and Codex to each write a hello-world file. It
also checks local sessions, sharing, config restoration, and final process
cleanup before writing its qualification JSON. Set `S46_E2E_KEEP_SANDBOX=1`
and preserve the private run logs and sandbox for independent reviewer
inspection.

The runner's `external` flags state that the enclosing OS profile is active;
they do not install that profile. Reviewers must inspect the actual profile,
run log, generated files, and CLI output before signing. A JSON document by
itself cannot prove that a command ran.

## Sign and append qualification

Add the exact evidence JSON and two purpose-scoped SSH signatures to the same
bundle. The final bundle has these seven files and no others:

```text
s46_0.3.1_darwin_arm64.tar.gz
gateway-approval-subject.json
fredoliveira.sig
goncalossilva.sig
s46_0.3.1_darwin_arm64.tar.gz.qualification.json
fredoliveira.cli-qualification.sig
goncalossilva.cli-qualification.sig
```

Fred and Gonçalo each sign the **exact qualification JSON bytes** on their own
machines with SSH namespace `s46-cli-direct-mac-qualification-v1`. Their
independent public keys are enrolled in
`scripts/cli-direct-mac-qualification-trust.json`. Gateway approval keys are
separate and remain scoped to the Gateway subject.

```sh
python3 scripts/cli_mac_airplane_release.py qualified-record \
  --commit FULL_CLI_COMMIT --source-tree FULL_CLI_TREE \
  --bundle /absolute/private/seven-file-bundle \
  --archive-sha256 INDEPENDENT_MAC_ARCHIVE_SHA256 \
  --gateway-bundle /absolute/private/gateway-build \
  --gateway-qualification /absolute/private/gateway-qualification.json \
  --gateway-evidence-root /absolute/private/gateway-diagnostic \
  --sequence-two /absolute/private/candidate-sequence-2.json \
  --output /absolute/private/candidate-sequence-3.json
```

This command re-verifies the Gateway packet and signatures, committed
qualified manifest, source tree and runner script, exact CLI archive and
embedded executable, installer evidence, three harnesses, loaded runtime
pins, sequence-2 CAS identity, and both CLI signatures. It rejects adapter-only
diagnostics and Actions-shaped or incomplete direct Mac evidence. Upload the
three new blobs using the stage-only authority credential, append the exact
sequence-3 record with the captured sequence-2 ETag, then capture and compare
the public record and all seven assets.

## Tag and publish

After independent review, create an annotated `v0.3.1-mac.1` tag targeting the
exact source commit. Push and independently read back its tag object. Run the
same `cli_mac_airplane_release.py` inputs with `publication-plan`, plus
`--sequence-three` and `--tag-object`, to emit the exact sequence-4 candidate
and expected sequence-5 published record. The command verifies the annotated
tag object targets the reviewed commit, matches the remote tag, and re-verifies
the qualification before producing either record. Append sequence 4 with the
stage credential and its exact
sequence-3 ETag. Finalize with the separate publish credential and exact
sequence-4 ETag. Capture the public published record, compare it byte for byte
with expected sequence 5, download every logical asset with
`scripts/release_authority.py download`, and compare its SHA-256 and bytes to
the signed local bundle. Confirm the candidate endpoint is gone.

The source PR that introduces this path does not enroll keys, sign evidence,
set `qualified:true`, create a tag, or mutate production authority state.
