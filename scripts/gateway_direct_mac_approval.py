#!/usr/bin/env python3
"""Prepare or verify the exact two-reviewer Gateway 0.3.1 Mac approval subject."""

import argparse
import base64
import contextlib
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

if __package__:
    from .gateway_direct_mac import CandidateError, RECORD, canonical, command_verify, file_identity, sha256
else:
    from gateway_direct_mac import CandidateError, RECORD, canonical, command_verify, file_identity, sha256


SUBJECT_SCHEMA = "s46.gateway-direct-mac-approval-subject.v1"
QUALIFICATION_SCHEMA = "s46.gateway-direct-mac-qualification.v2"
TRUST_SCHEMA = "s46.gateway-direct-mac-approval-trust.v1"
PURPOSE = "s46-gateway-direct-mac-approval-v1"
TRUST = Path(__file__).with_name("gateway-direct-mac-approval-trust.json")
REVIEWERS = ("fredoliveira", "goncalossilva")
DIAGNOSTIC_KIND = "s46-private-devstral-mac-isolation-diagnostic-v2"
DIAGNOSTIC_INVENTORY = "s46-private-diagnostic-inventory-v1"
DIAGNOSTIC_REQUIRED = {
    "README.md", "combined.py", "control/diagnostic.sb", "probe.py", "result.json", "run.py",
    "out/anthropic-stream.sse", "out/claude.stderr", "out/claude.stdout",
    "out/claude_app.stderr", "out/claude_app.stdout", "out/codex-stream.sse",
    "out/codex.stderr", "out/codex.stdout", "out/codex_app.stderr", "out/codex_app.stdout",
    "out/gateway-before-lsof.txt", "out/gateway-before-ps.txt", "out/gateway-before-vmmap.txt",
    "out/gateway.log", "out/llama-after-lsof.txt", "out/llama-after-ps.txt",
    "out/llama-after-vmmap.txt", "out/llama-before-lsof.txt", "out/llama-before-ps.txt",
    "out/llama-before-vmmap.txt", "out/llama.log", "out/openai-stream.sse",
    "out/pi.stderr", "out/pi.stdout", "out/pi_app.stderr", "out/pi_app.stdout",
    "out/sandbox-probe.stderr", "out/sandbox-probe.stdout",
    "project/claude_hello.py", "project/codex_hello.py", "project/pi_hello.py",
}
CONTROLS = {
    "independentIsolationReviewPassed", "loadedProcessBindingPassed", "cleanupPassed",
    "piClientPassed", "claudeClientPassed", "codexClientPassed",
}
MODEL_SHA256 = "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496"
MODEL_SIZE = 14334446752
PUBLIC_MODEL = "s46/devstral-small-2-24b"
BACKEND_MODEL = "devstral-small-2:24b-instruct-2512-q4_K_M"
LLAMA_ARCHIVE_SHA256 = "1a41b5bb3585a79ce90535b2c61671eae60a032ddb2b4992a62eb0d221f9b2bb"
LLAMA_BINARY_SHA256 = "fc05cc348584b01226d1a95bc2282978193d009a02b16d739ff17f9b332b1cda"
SHA256 = re.compile(r"[0-9a-f]{64}\Z")


class ApprovalError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise ApprovalError(message)


def read_exact(path, maximum):
    path = Path(path)
    try:
        identity = file_identity(path, maximum)
    except CandidateError as error:
        raise ApprovalError(str(error)) from error
    raw = path.read_bytes()
    require(len(raw) == identity["sizeBytes"] and sha256(raw) == identity["sha256"],
            f"{path} changed while reading")
    return raw


def read_canonical(path, maximum):
    raw = read_exact(path, maximum)
    try:
        value = json.loads(raw)
    except (UnicodeError, ValueError) as error:
        raise ApprovalError(f"{path} is not JSON: {error}") from error
    require(isinstance(value, dict) and raw == canonical(value), f"{path} is not a canonical JSON object")
    return value, raw


def parse_json(raw, description):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            require(key not in value, f"{description} has duplicate key {key}")
            value[key] = item
        return value

    try:
        return json.loads(raw, object_pairs_hook=unique)
    except (UnicodeError, ValueError) as error:
        raise ApprovalError(f"{description} is not JSON: {error}") from error


def evidence_file(root, identity):
    require(isinstance(identity, dict) and set(identity) == {"path", "sha256", "sizeBytes"},
            "evidence identity must contain only path, sha256, and sizeBytes")
    name = identity["path"]
    require(isinstance(name, str) and name and all(part not in ("", ".", "..") for part in name.split("/"))
            and not name.startswith("/") and "\\" not in name, "evidence path is not relative and canonical")
    require(isinstance(identity["sha256"], str) and SHA256.fullmatch(identity["sha256"]),
            "evidence SHA-256 is invalid")
    require(type(identity["sizeBytes"]) is int and 0 <= identity["sizeBytes"] <= (16 << 20),
            "evidence size is invalid")
    path = root
    for part in name.split("/")[:-1]:
        path = path / part
        require(stat.S_ISDIR(path.lstat().st_mode), f"evidence parent {path} is not a real directory")
    path = path / name.split("/")[-1]
    if identity["sizeBytes"] == 0:
        before = path.lstat()
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_size == 0,
                f"evidence bytes differ: {name}")
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        try:
            opened = os.fstat(descriptor)
            require(os.path.samestat(before, opened) and os.read(descriptor, 1) == b"" and
                    os.path.samestat(opened, os.fstat(descriptor)), f"evidence bytes differ: {name}")
        finally:
            os.close(descriptor)
        require(identity["sha256"] == sha256(b""), f"evidence bytes differ: {name}")
        return b""
    try:
        actual = file_identity(path, 16 << 20)
    except CandidateError as error:
        raise ApprovalError(str(error)) from error
    require(actual == {"sha256": identity["sha256"], "sizeBytes": identity["sizeBytes"]},
            f"evidence bytes differ: {name}")
    return read_exact(path, 16 << 20)


def diagnostic_files(root, manifest_identity):
    manifest = parse_json(evidence_file(root, manifest_identity), "diagnostic manifest")
    require(isinstance(manifest, dict) and set(manifest) == {"schema", "files"} and
            manifest["schema"] == DIAGNOSTIC_INVENTORY and isinstance(manifest["files"], list) and
            0 < len(manifest["files"]) <= 128, "diagnostic inventory is incomplete")
    records = manifest["files"]
    require(all(isinstance(item, dict) and isinstance(item.get("path"), str) for item in records),
            "diagnostic inventory has malformed paths")
    names = [item["path"] for item in records]
    require(names == sorted(set(names)) and DIAGNOSTIC_REQUIRED <= set(names) and
            sum(name.startswith("home/pi/.pi/agent/sessions/") and name.endswith(".jsonl")
                for name in names) == 1,
            "diagnostic inventory omits required evidence")
    return {item["path"]: evidence_file(root, item) for item in records}


def recorded_origin(result):
    before = result.get("llamaBeforeRequest")
    process = before.get("process") if isinstance(before, dict) else None
    executable = process.get("executable") if isinstance(process, dict) else None
    suffix = "/runtime/llama/llama-b9270/llama-server"
    require(isinstance(executable, str) and executable.endswith(suffix),
            "diagnostic recorded origin is missing")
    origin = executable[:-len(suffix)]
    require(origin.startswith("/") and origin != "/" and str(Path(origin)) == origin and
            not any(part in (".", "..") for part in Path(origin).parts),
            "diagnostic recorded origin is not canonical")
    return Path(origin)


def observed_file(files, recorded_root, record, name):
    require(isinstance(record, dict) and record.get("path") == str(recorded_root / name) and
            record.get("sha256") == sha256(files[name]) and
            type(record.get("sizeBytes")) is int and record["sizeBytes"] == len(files[name]) and
            type(record.get("exit")) is int and record["exit"] == 0,
            f"diagnostic observation differs: {name}")
    return files[name].decode("utf-8", errors="replace")


def verify_process(files, recorded_root, snapshot, role, binary_sha, binary_path, model=None):
    require(isinstance(snapshot, dict) and isinstance(snapshot.get("process"), dict),
            f"diagnostic {role} process is missing")
    process = snapshot["process"]
    pid = process.get("pid")
    require(type(pid) is int and pid > 0 and process.get("executable") == str(recorded_root / binary_path) and
            process.get("executableSha256") == binary_sha, f"diagnostic {role} executable differs")
    lsof = observed_file(files, recorded_root, snapshot.get("lsof"), f"out/{role}-lsof.txt")
    observed_file(files, recorded_root, snapshot.get("ps"), f"out/{role}-ps.txt")
    vmmap = observed_file(files, recorded_root, snapshot.get("vmmap"), f"out/{role}-vmmap.txt")
    require(any(line.split()[1:2] == [str(pid)] and line.endswith(str(recorded_root / binary_path))
                for line in lsof.splitlines()), f"diagnostic {role} executable is not in lsof")
    if model is not None:
        path = model["path"]
        require(snapshot.get("modelPathObserved") is True and
                any(line.split()[1:2] == [str(pid)] and
                    re.search(rf"\b{model['sizeBytes']}\s+{model['inode']}\s+{re.escape(path)}$", line)
                    for line in lsof.splitlines()) and path in vmmap and
                str(recorded_root / "runtime/llama/llama-b9270/libggml-metal.0.12.0.dylib") in vmmap,
                f"diagnostic {role} model or Metal mapping is missing")
    return pid


def verify_diagnostic(files, candidate_record):
    result = parse_json(files["result.json"], "diagnostic result")
    require(isinstance(result, dict) and result.get("kind") == DIAGNOSTIC_KIND and
            result.get("error") is None and result.get("projectInitiallyEmpty") is True,
            "diagnostic result or fresh project differs")
    origin = recorded_origin(result)
    inputs = result.get("inputs")
    gateway = candidate_record["gateway"]
    require(isinstance(inputs, dict) and inputs.get("gatewayArchiveSha256") == gateway["archiveSha256"] and
            inputs.get("gatewayBinarySha256") == gateway["binarySha256"] and
            inputs.get("llamaArchiveSha256") == LLAMA_ARCHIVE_SHA256 and
            inputs.get("llamaBinarySha256") == LLAMA_BINARY_SHA256 and
            isinstance(inputs.get("model"), dict) and inputs["model"].get("sha256") == MODEL_SHA256 and
            type(inputs["model"].get("sizeBytes")) is int and
            inputs["model"]["sizeBytes"] == MODEL_SIZE and
            type(inputs["model"].get("inode")) is int and inputs["model"]["inode"] > 0 and
            result.get("modelSha256After") == MODEL_SHA256,
            "diagnostic input identities differ")
    model = inputs["model"]
    require(isinstance(model.get("path"), str) and model["path"].endswith(".gguf") and
            result.get("sandboxProfileSha256") == sha256(files["control/diagnostic.sb"]),
            "diagnostic model or sandbox profile differs")
    probe = parse_json(files["out/sandbox-probe.stdout"], "sandbox probe")
    require(isinstance(probe, dict) and all(isinstance(probe.get(name), dict) and
            probe[name].get("type") == "PermissionError" and
            type(probe[name].get("errno")) is int and probe[name]["errno"] == 1
            for name in ("file", "network")), "diagnostic sandbox denial is missing")
    before = verify_process(files, origin, result.get("llamaBeforeRequest"), "llama-before",
                            LLAMA_BINARY_SHA256, "runtime/llama/llama-b9270/llama-server", model)
    after = verify_process(files, origin, result.get("llamaAfterRequest"), "llama-after",
                           LLAMA_BINARY_SHA256, "runtime/llama/llama-b9270/llama-server", model)
    require(before == after, "diagnostic llama process changed during requests")
    verify_process(files, origin, result.get("gatewayBeforeRequest"), "gateway-before",
                   gateway["binarySha256"], "gateway/s46-gateway")
    verify_client_results(files, result.get("harnesses"))
    verify_streams(files, origin, result.get("streams"))
    inference = result.get("inference")
    choices = inference.get("body", {}).get("choices") if isinstance(inference, dict) and isinstance(
        inference.get("body"), dict) else None
    worker = result.get("worker")
    require(isinstance(inference, dict) and type(inference.get("status")) is int and
            inference["status"] == 200 and isinstance(choices, list) and len(choices) == 1 and
            isinstance(choices[0], dict) and choices[0].get("finish_reason") == "stop" and
            isinstance(choices[0].get("message"), dict) and
            choices[0]["message"].get("content") == "hello from devstral" and
            inference["body"].get("model") == PUBLIC_MODEL and
            isinstance(worker, dict) and worker.get("state") == "ready" and
            worker.get("mode") == "airplane" and
            worker.get("models") == [{"id": PUBLIC_MODEL, "backendModel": BACKEND_MODEL,
                                      "state": "ready"}], "diagnostic inference did not complete")
    require(result.get("cleanupErrors") == [] and result.get("servicesStopped") is True and
            result.get("portsFree") is True, "diagnostic cleanup did not pass")


def verify_client_results(files, harnesses):
    require(isinstance(harnesses, dict) and set(harnesses) == {"pi", "claude", "codex"},
            "diagnostic client set differs")
    for name, item in harnesses.items():
        require(isinstance(item, dict) and isinstance(item.get("app"), dict) and
                isinstance(item.get("client"), dict), f"diagnostic {name} client is malformed")
        app, client = item["app"], item["client"]
        run = app.get("run")
        source = files[f"project/{name}_hello.py"]
        expected = f'def hello_from_{name}():\n    return "hello from {name}"'.encode()
        require(isinstance(run, dict) and type(client.get("exit")) is int and client["exit"] == 0 and
                app.get("ok") is True and app.get("output") == f"hello from {name}" and
                type(run.get("exit")) is int and run["exit"] == 0 and
                source.strip() == expected and app.get("sourceSha256") == sha256(source) and
                files[f"out/{name}_app.stdout"].strip() == f"hello from {name}".encode() and
                client.get("stdoutSha256") == sha256(files[f"out/{name}.stdout"]) and
                client.get("stderrSha256") == sha256(files[f"out/{name}.stderr"]) and
                run.get("stdoutSha256") == sha256(files[f"out/{name}_app.stdout"]) and
                run.get("stderrSha256") == sha256(files[f"out/{name}_app.stderr"]),
                f"diagnostic {name} client did not pass")


def verify_streams(files, recorded_root, streams):
    terminals = {"openai": b"data: [DONE]", "anthropic": b"event: message_stop",
                 "codex": b"event: response.completed"}
    require(isinstance(streams, dict) and set(streams) == set(terminals),
            "diagnostic protocol streams differ")
    for name, terminal in terminals.items():
        record = streams[name]
        raw = files[f"out/{name}-stream.sse"]
        final_block = raw.strip().split(b"\n\n")[-1]
        require(isinstance(record, dict) and record.get("path") == str(recorded_root / f"out/{name}-stream.sse") and
                type(record.get("status")) is int and record["status"] == 200 and
                record.get("contentType") == "text/event-stream" and
                record.get("rawSha256") == sha256(raw) and
                type(record.get("sizeBytes")) is int and record["sizeBytes"] == len(raw) and
                record.get("terminal") == terminal.decode() and terminal in final_block.splitlines(),
                f"diagnostic {name} stream did not terminate")


def validate_qualification(value, evidence_root, candidate_record, candidate_sha):
    require(isinstance(value, dict) and set(value) ==
            {"schema", "candidateRecordSha256", "gatewayArchiveSha256", "runtime",
                           "controls", "evidence"} and value["schema"] == QUALIFICATION_SCHEMA,
            "qualification record has an unsupported shape")
    require(value["candidateRecordSha256"] == candidate_sha and
            value["gatewayArchiveSha256"] == candidate_record["gateway"]["archiveSha256"],
            "qualification targets a different candidate")
    require(value["runtime"] == {
        "modelSha256": MODEL_SHA256, "modelSizeBytes": MODEL_SIZE,
        "llamaArchiveSha256": LLAMA_ARCHIVE_SHA256, "llamaBinarySha256": LLAMA_BINARY_SHA256,
    }, "qualification runtime closure differs")
    controls = value["controls"]
    require(isinstance(controls, dict) and set(controls) == CONTROLS and
            all(controls[name] is True for name in CONTROLS),
            "qualification lacks a passed independent review, loaded-process binding, cleanup, or client test")
    evidence = value["evidence"]
    require(isinstance(evidence, dict) and set(evidence) ==
            {"diagnosticManifest", "independentIsolationReview", "loadedProcessBinding"},
            "qualification evidence is incomplete")
    require(stat.S_ISDIR(evidence_root.lstat().st_mode), "diagnostic root is not a real directory")
    manifest_identity = evidence["diagnosticManifest"]
    require(isinstance(manifest_identity, dict) and manifest_identity.get("path") ==
            "evidence-manifest.json", "qualification does not bind the diagnostic manifest")
    diagnostic_bytes = diagnostic_files(evidence_root, manifest_identity)
    verify_diagnostic(diagnostic_bytes, candidate_record)
    for name in ("independentIsolationReview", "loadedProcessBinding"):
        item = evidence[name]
        require(isinstance(item, dict) and item.get("path") not in diagnostic_bytes and
                type(item.get("sizeBytes")) is int and item["sizeBytes"] > 0,
                f"{name} needs separate retained evidence")
        evidence_file(evidence_root, item)
    require(evidence["independentIsolationReview"]["path"] !=
            evidence["loadedProcessBinding"]["path"], "independent review and loaded-process evidence must differ")


def subject_for(candidate_record, candidate_raw, qualification_raw):
    return {
        "schema": SUBJECT_SCHEMA,
        "purpose": PURPOSE,
        "build": {
            "candidateRecordSha256": sha256(candidate_raw),
            "sourceCommit": candidate_record["source"]["commit"],
            "sourceArchiveSha256": candidate_record["source"]["archiveSha256"],
            "toolchainArchiveSha256": candidate_record["toolchain"]["archiveSha256"],
            "gatewayArchiveSha256": candidate_record["gateway"]["archiveSha256"],
            "gatewayBinarySha256": candidate_record["gateway"]["binarySha256"],
        },
        "qualificationSha256": sha256(qualification_raw),
        "publicationConsent": {
            "approved": True,
            "scope": "local-airplane-gateway-adapter",
            "assetOrigin": "https://models.s46.dev/cli-assets",
            "archiveName": candidate_record["gateway"]["name"],
        },
    }


def checked_subject(bundle, qualification, evidence_root):
    with contextlib.redirect_stdout(io.StringIO()):
        command_verify(type("Args", (), {"bundle": str(bundle)})())
    candidate_record, candidate_raw = read_canonical(Path(bundle) / RECORD, 1 << 20)
    qualification_record, qualification_raw = read_canonical(qualification, 1 << 20)
    validate_qualification(qualification_record, Path(evidence_root), candidate_record, sha256(candidate_raw))
    return canonical(subject_for(candidate_record, candidate_raw, qualification_raw))


def trusted_reviewers(trust_path=None):
    trust, _ = read_canonical(trust_path or TRUST, 4096)
    require(set(trust) == {"schema", "purpose", "reviewers"} and trust["schema"] == TRUST_SCHEMA
            and trust["purpose"] == PURPOSE and isinstance(trust["reviewers"], dict)
            and set(trust["reviewers"]) == set(REVIEWERS), "Gateway approval trust is not explicitly enrolled")
    keys = {}
    for reviewer in REVIEWERS:
        key = trust["reviewers"][reviewer]
        require(isinstance(key, str) and re.fullmatch(r"ssh-ed25519 [A-Za-z0-9+/]+={0,2}", key),
                f"Gateway approval key for {reviewer} is not enrolled")
        try:
            wire = base64.b64decode(key.split()[1], validate=True)
        except ValueError as error:
            raise ApprovalError(f"invalid SSH Ed25519 key for {reviewer}") from error
        require(len(wire) == 51 and wire[:15] == b"\x00\x00\x00\x0bssh-ed25519" and wire[15:19] == b"\x00\x00\x00 " and
                base64.b64encode(wire).decode() == key.split()[1], f"invalid SSH Ed25519 key for {reviewer}")
        keys[reviewer] = key
    require(keys[REVIEWERS[0]] != keys[REVIEWERS[1]], "Gateway approval reviewers must have distinct keys")
    return keys


def verify_approval(subject, reviewer, signature, keys):
    require(reviewer in REVIEWERS, "Gateway approval reviewer is not recognized")
    read_exact(signature, 8192)
    with tempfile.TemporaryDirectory(prefix="s46-gateway-approval-") as temporary:
        signers = Path(temporary) / "allowed_signers"
        signers.write_text(f"{reviewer} {keys[reviewer]}\n", encoding="ascii")
        result = subprocess.run(["/usr/bin/ssh-keygen", "-Y", "verify", "-f", str(signers),
                                 "-I", reviewer, "-n", PURPOSE, "-s", str(signature)],
                                input=subject, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
    require(result.returncode == 0, f"{reviewer} signature is invalid for the exact Gateway subject")


def verify_approvals(subject, approvals):
    keys = trusted_reviewers()
    for reviewer in REVIEWERS:
        verify_approval(subject, reviewer, Path(approvals) / f"{reviewer}.sig", keys)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for command in ("prepare-subject", "verify"):
        sub = commands.add_parser(command)
        sub.add_argument("--bundle", required=True)
        sub.add_argument("--qualification", required=True)
        sub.add_argument("--evidence-root", required=True)
        sub.add_argument("--subject", required=True)
        if command == "verify":
            sub.add_argument("--approvals", required=True)
    args = parser.parse_args()
    try:
        subject = checked_subject(args.bundle, args.qualification, args.evidence_root)
        if args.command == "prepare-subject":
            descriptor = os.open(args.subject, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400)
            with os.fdopen(descriptor, "wb") as output:
                output.write(subject)
            print(f"Gateway approval subject SHA-256: {sha256(subject)}")
        else:
            require(read_exact(args.subject, 1 << 20) == subject,
                    "approval subject differs from exact build and qualification inputs")
            verify_approvals(subject, args.approvals)
            print(f"two Gateway approvals verified for subject SHA-256: {sha256(subject)}")
    except (ApprovalError, CandidateError, OSError, ValueError, subprocess.TimeoutExpired) as error:
        parser.exit(1, f"gateway-direct-mac-approval: {error}\n")


if __name__ == "__main__":
    main()
