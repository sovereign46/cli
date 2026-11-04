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
QUALIFICATION_SCHEMA = "s46.gateway-direct-mac-qualification.v1"
TRUST_SCHEMA = "s46.gateway-direct-mac-approval-trust.v1"
PURPOSE = "s46-gateway-direct-mac-approval-v1"
TRUST = Path(__file__).with_name("gateway-direct-mac-approval-trust.json")
REVIEWERS = ("fredoliveira", "goncalossilva")
DIAGNOSTIC_FILES = {"README.md", "result.json", "run.py", "combined.py", "control/diagnostic.sb"}
CONTROLS = {
    "independentIsolationReviewPassed", "loadedProcessBindingPassed", "cleanupPassed",
    "piClientPassed", "claudeClientPassed", "codexClientPassed",
}
MODEL_SHA256 = "d14ba9edee1bb4c4996a726deb81e49ae81800a3216f0774634238c380aee496"
MODEL_SIZE = 14334446752
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


def evidence_file(root, identity):
    require(isinstance(identity, dict) and set(identity) == {"path", "sha256", "sizeBytes"},
            "evidence identity must contain only path, sha256, and sizeBytes")
    name = identity["path"]
    require(isinstance(name, str) and name and all(part not in ("", ".", "..") for part in name.split("/"))
            and not name.startswith("/") and "\\" not in name, "evidence path is not relative and canonical")
    require(isinstance(identity["sha256"], str) and SHA256.fullmatch(identity["sha256"]),
            "evidence SHA-256 is invalid")
    require(type(identity["sizeBytes"]) is int and 0 < identity["sizeBytes"] <= (16 << 20),
            "evidence size is invalid")
    path = root
    for part in name.split("/")[:-1]:
        path = path / part
        require(stat.S_ISDIR(path.lstat().st_mode), f"evidence parent {path} is not a real directory")
    path = path / name.split("/")[-1]
    try:
        actual = file_identity(path, 16 << 20)
    except CandidateError as error:
        raise ApprovalError(str(error)) from error
    require(actual == {"sha256": identity["sha256"], "sizeBytes": identity["sizeBytes"]},
            f"evidence bytes differ: {name}")
    return read_exact(path, 16 << 20)


def verify_diagnostic(raw, candidate_record):
    result = json.loads(raw)
    require(isinstance(result, dict), "diagnostic result is not a JSON object")
    gateway = candidate_record["gateway"]
    require(result.get("kind") == "s46-gateway-031-loopback-diagnostic-not-release-or-qualification",
            "diagnostic result has the wrong kind")
    require(result.get("apiCommit") == candidate_record["source"]["commit"] and
            result.get("apiTree") == candidate_record["source"]["tree"] and
            result.get("gatewayArchiveSha256") == gateway["archiveSha256"] and
            result.get("modelSha256") == MODEL_SHA256 and
            result.get("llamaArchiveSha256") == LLAMA_ARCHIVE_SHA256,
            "diagnostic result is for different inputs")
    expected_inputs = {"gatewayArchive": gateway["archiveSha256"],
                       "gatewayBinary": gateway["binarySha256"], "model": MODEL_SHA256,
                       "llamaArchive": LLAMA_ARCHIVE_SHA256, "llamaBinary": LLAMA_BINARY_SHA256}
    inputs = result.get("inputs")
    require(isinstance(inputs, dict) and all(isinstance(inputs.get(name), dict) and
            inputs[name].get("sha256") == digest for name, digest in expected_inputs.items()),
            "diagnostic input identities differ")
    require(result.get("isolationProbe") ==
            "host private file denied; external egress denied; scratch write permitted" and
            result.get("cleanupErrors") == [] and result.get("servicesStopped") is True and
            result.get("portsFree") is True, "diagnostic isolation or cleanup did not pass")
    harnesses = result.get("harnesses")
    require(isinstance(harnesses, dict) and set(harnesses) == {"pi", "claude", "codex"},
            "diagnostic client set differs")
    for name in harnesses:
        item = harnesses[name]
        require(isinstance(item, dict), f"diagnostic {name} client is malformed")
        app = item.get("app")
        client = item.get("client")
        require(isinstance(app, dict) and isinstance(client, dict) and
                isinstance(app.get("run"), dict), f"diagnostic {name} client is malformed")
        require(client.get("exit") == 0 and app.get("ok") is True and
                app.get("output") == f"hello from {name}" and app["run"].get("exit") == 0,
                f"diagnostic {name} client did not pass")


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
            {"diagnostic", "independentIsolationReview", "loadedProcessBinding"},
            "qualification evidence is incomplete")
    diagnostic = evidence["diagnostic"]
    require(isinstance(diagnostic, list) and len(diagnostic) == len(DIAGNOSTIC_FILES) and
            all(isinstance(item, dict) and isinstance(item.get("path"), str) for item in diagnostic) and
            {item["path"] for item in diagnostic} == DIAGNOSTIC_FILES,
            "qualification does not bind the complete private diagnostic")
    diagnostic_bytes = {item["path"]: evidence_file(evidence_root, item) for item in diagnostic}
    verify_diagnostic(diagnostic_bytes["result.json"], candidate_record)
    for name in ("independentIsolationReview", "loadedProcessBinding"):
        item = evidence[name]
        require(isinstance(item, dict) and item.get("path") not in DIAGNOSTIC_FILES,
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


def trusted_reviewers():
    trust, _ = read_canonical(TRUST, 4096)
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


def verify_approvals(subject, approvals):
    keys = trusted_reviewers()
    for reviewer in REVIEWERS:
        signature = Path(approvals) / f"{reviewer}.sig"
        read_exact(signature, 8192)
        with tempfile.TemporaryDirectory(prefix="s46-gateway-approval-") as temporary:
            signers = Path(temporary) / "allowed_signers"
            signers.write_text(f"{reviewer} {keys[reviewer]}\n", encoding="ascii")
            result = subprocess.run(["/usr/bin/ssh-keygen", "-Y", "verify", "-f", str(signers),
                                     "-I", reviewer, "-n", PURPOSE, "-s", str(signature)],
                                    input=subject, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15)
        require(result.returncode == 0, f"{reviewer} signature is invalid for the exact Gateway subject")


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
