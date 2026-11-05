# Devstral Mac airplane diagnostic

This is an **unsigned local diagnostic candidate**, not a qualified CLI release or a runtime certification. It is available only in a CLI built without the `release` tag. Normal `s46 airplane setup` remains closed while the embedded release manifest has `qualified:false`.

The candidate pins one Devstral GGUF, llama.cpp b9270, and the Gateway 0.3.1 protocol adapter by exact SHA-256 and size. The model still needs a valid s46 signed local receipt. The Gateway serves Pi, Claude Code, and Codex on loopback; it is not part of the model attestation. Candidate archives must be local regular files. Setup verifies the archive and installed runtime bytes and records process identity before considering airplane mode ready.

On a Darwin/arm64 Mac with the signed Devstral model already installed, build the source candidate and point it at the two local archives:

```sh
go build -o /absolute/private/path/s46-diagnostic ./cmd/s46

export S46_AIRPLANE_CANDIDATE=1
export S46_AIRPLANE_CANDIDATE_LLAMA_ARCHIVE=/absolute/private/path/llama-b9270-bin-macos-arm64.tar.gz
export S46_AIRPLANE_CANDIDATE_GATEWAY_ARCHIVE=/absolute/private/path/s46-gateway_0.3.1_darwin_arm64.tar.gz
export S46_OFFLINE=1

/absolute/private/path/s46-diagnostic airplane setup --candidate --mode=on --harness=pi --yes
/absolute/private/path/s46-diagnostic connect local --harness=claude-code
/absolute/private/path/s46-diagnostic connect local --harness=codex
/absolute/private/path/s46-diagnostic status
```

Keep `S46_AIRPLANE_CANDIDATE=1` in the shell for later CLI readiness checks. The `--candidate` flag also enables the candidate for that setup invocation; the environment variable makes subsequent commands use the same diagnostic pins. `S46_OFFLINE=1` is appropriate only after the signed model receipt is installed. The CLI prints an unsigned-candidate warning and does not change the embedded `qualified:false` status. A `release` build rejects `--candidate` and ignores the candidate environment variable.

The exact local archives used for this Mac diagnostic have SHA-256:

- llama.cpp archive: `1a41b5bb3585a79ce90535b2c61671eae60a032ddb2b4992a62eb0d221f9b2bb`
- Gateway archive: `ef9d322d2bb775ae065d712280b786841c149ed959acf70942c25c74e8f32d3a`

The CLI checks these against its embedded pins before installation. It also verifies the llama.cpp runtime inventory and executable, the Gateway executable, the signed model receipt and GGUF, and the live process identities. A successful diagnostic establishes that the local CLI path works on the tested Mac; it does not certify release provenance, model quality, or safety.
