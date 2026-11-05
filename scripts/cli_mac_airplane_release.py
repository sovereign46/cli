#!/usr/bin/env python3
"""Verify the direct Mac installer qualification and prepare immutable authority records."""

import argparse
import copy
import datetime
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile

try:
    import cli_mac_airplane_candidate as mac
    import gateway_direct_mac_approval as approval
    from cli_release_authority import AUTHORITY_ORIGIN, _hash_stable, _write_new, _write_record
    from release_authority import (canonical_record, finalized_record, parse_record,
                                   record_etag, validate_append_only)
except ModuleNotFoundError:  # Imported as scripts.cli_mac_airplane_release in tests.
    from scripts import cli_mac_airplane_candidate as mac
    from scripts import gateway_direct_mac_approval as approval
    from scripts.cli_release_authority import AUTHORITY_ORIGIN, _hash_stable, _write_new, _write_record
    from scripts.release_authority import (canonical_record, finalized_record, parse_record,
                                           record_etag, validate_append_only)


QUALIFICATION = mac.ARCHIVE + ".qualification.json"
QUALIFIED_PROFILE = {
    **mac.PROFILE,
    QUALIFICATION: ("application/json", "qualification"),
    "fredoliveira.cli-qualification.sig": ("text/plain", "qualification-signature"),
    "goncalossilva.cli-qualification.sig": ("text/plain", "qualification-signature"),
}
TRUST = Path(__file__).with_name("cli-direct-mac-qualification-trust.json")
TRUST_SCHEMA = "s46.cli-direct-mac-qualification-trust.v1"
PURPOSE = "s46-cli-direct-mac-qualification-v1"
RUNNER = "scripts/e2e-airplane-harnesses"
MANIFEST = "internal/airplane/gateway-release.json"
SOURCE = Path(__file__).resolve().parent.parent
SHA256 = re.compile(r"[0-9a-f]{64}\Z")


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def source_bytes(repo, commit, path):
    require(mac.OBJECT.fullmatch(commit) is not None, "source commit is not canonical")
    result = subprocess.run(["git", "-C", str(repo), "show", f"{commit}:{path}"],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    require(result.returncode == 0, f"source commit lacks {path}")
    return result.stdout


def source_tree(repo, commit):
    result = subprocess.run(["git", "-C", str(repo), "rev-parse", "--verify", f"{commit}^{{tree}}"],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    require(result.returncode == 0, "source commit is not present locally")
    return result.stdout.decode("ascii").strip()


def archive_binary(path):
    binary = None
    members = 0
    total = 0
    with tarfile.open(path, "r|gz") as archive:
        for member in archive:
            members += 1
            require(members <= 64 and member.size >= 0 and member.size <= 128 << 20,
                    "Mac CLI archive has an unbounded member")
            total += member.size
            require(total <= 256 << 20, "Mac CLI archive expands past its size bound")
            name = member.name
            require(name and not name.startswith("/") and "\\" not in name and
                    all(part not in ("", ".", "..") for part in name.split("/")),
                    "Mac CLI archive has an unsafe path")
            require(member.isfile() or member.isdir(), "Mac CLI archive has a link or special member")
            require(member.mode & 0o7000 == 0, "Mac CLI archive has privilege bits")
            if name != "s46":
                require(member.mode & 0o111 == 0, "Mac CLI archive has another executable")
                continue
            require(binary is None and member.isfile() and member.mode == 0o755 and member.size > 0,
                    "Mac CLI archive must contain one top-level s46 executable")
            stream = archive.extractfile(member)
            require(stream is not None, "Mac CLI executable is unreadable")
            digest = hashlib.sha256()
            size = 0
            while chunk := stream.read(1 << 20):
                size += len(chunk)
                require(size <= member.size, "Mac CLI executable grew while reading")
                digest.update(chunk)
            require(size == member.size, "Mac CLI executable is truncated")
            binary = {"sha256": digest.hexdigest(), "size": size, "sourceMode": "0755"}
    require(binary is not None, "Mac CLI archive lacks its executable")
    return binary


def checked_manifest(repo, commit, tree):
    require(source_tree(repo, commit) == tree, "source tree differs from committed source")
    manifest = approval.parse_json(source_bytes(repo, commit, MANIFEST), "committed airplane manifest")
    require(manifest.get("schema") == "s46.airplane-release.v3" and manifest.get("qualified") is True,
            "committed airplane manifest is not qualified")
    model = manifest.get("model", {})
    llama = manifest.get("llamaCpp", {}).get("platforms", {}).get("darwin/arm64", {})
    gateway = manifest.get("gateway", {}).get("platforms", {}).get("darwin/arm64", {})
    require(model.get("id") == approval.PUBLIC_MODEL and model.get("backendModel") == approval.BACKEND_MODEL and
            model.get("sha256") == approval.MODEL_SHA256 and type(model.get("size")) is int and
            model["size"] == approval.MODEL_SIZE, "committed Devstral identity differs")
    require(llama.get("archiveSha256") == approval.LLAMA_ARCHIVE_SHA256 and
            llama.get("binarySha256") == approval.LLAMA_BINARY_SHA256 and
            llama.get("inventorySha256") == "5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167",
            "committed llama.cpp identity differs")
    require(manifest.get("gateway", {}).get("version") == "0.3.1" and
            gateway.get("archiveSha256") == mac.GATEWAY_ARCHIVE_SHA256 and
            gateway.get("binarySha256") == mac.GATEWAY_BINARY_SHA256,
            "committed Gateway 0.3.1 identity differs")
    return hashlib.sha256(source_bytes(repo, commit, RUNNER)).hexdigest(), len(source_bytes(repo, commit, RUNNER))


def qualification_bytes(bundle):
    raw = approval.read_exact(Path(bundle) / QUALIFICATION, 1 << 20)
    record = approval.parse_json(raw, "Mac CLI qualification")
    require(raw == (json.dumps(record, indent=2, sort_keys=True) + "\n").encode(),
            "Mac CLI qualification is not the runner's canonical JSON")
    return record, raw


def checked_qualification(bundle, seq2, repo):
    evidence, raw = qualification_bytes(bundle)
    require(set(evidence) == {"schema", "result", "qualifiedAt", "artifact", "binary", "build", "qualification"} and
            evidence["schema"] == "s46.release-qualification.v1" and evidence["result"] == "passed",
            "Mac CLI qualification is not a passed installer run")
    try:
        qualified_at = datetime.datetime.fromisoformat(evidence["qualifiedAt"].replace("Z", "+00:00"))
    except (TypeError, ValueError) as error:
        raise SystemExit("Mac CLI qualification time is invalid") from error
    require(qualified_at.tzinfo is not None, "Mac CLI qualification time needs a timezone")
    archive = mac.assets_by_name(seq2["assets"])[mac.ARCHIVE]
    artifact = evidence["artifact"]
    require(artifact == {"name": mac.ARCHIVE, "sha256": archive["sha256"],
                         "size": archive["size"], "kind": "archive"},
            "Mac CLI qualification does not cover the exact staged archive")
    require(evidence["binary"] == archive_binary(Path(bundle) / mac.ARCHIVE),
            "Mac CLI qualification does not cover the archive executable")

    source = seq2["source"]
    build = evidence["build"]
    require(isinstance(build, dict) and set(build) == {"version", "commit", "date", "goVersion", "release",
                                                   "sourceTree"} and
            build["version"] == "0.3.1" and build["commit"] == source["commit"] and
            build["sourceTree"] == dict((item["name"], item["value"]) for item in seq2["annotations"])["source-tree"] and
            build["release"] is True and isinstance(build["date"], str) and build["date"] and
            isinstance(build["goVersion"], str) and build["goVersion"],
            "Mac CLI build identity does not match the staged source")
    runner_sha, runner_size = checked_manifest(repo, source["commit"], build["sourceTree"])
    qualification = evidence["qualification"]
    require(isinstance(qualification, dict) and set(qualification) == {
        "expectedVersion", "expectedCommit", "networkIsolation", "filesystemIsolation",
        "installation", "capabilities", "airplaneHarnesses", "execution", "runner", "runtime"},
        "Mac CLI qualification must come from the full airplane installer runner")
    require(qualification["expectedVersion"] == "0.3.1" and
            qualification["expectedCommit"] == source["commit"] and
            qualification["networkIsolation"] == "external" and
            qualification["filesystemIsolation"] == "external" and
            qualification["capabilities"] == ["cloud-client", "airplane"] and
            qualification["airplaneHarnesses"] == ["pi", "claude-code", "codex"],
            "Mac CLI qualification lacks full isolated Pi, Claude, and Codex coverage")
    require(qualification["installation"] == {
        "command": "s46 airplane setup --mode=on --harness=pi --yes",
        "harnessConfiguration": ["s46 connect local --harness=claude-code",
                                 "s46 connect local --harness=codex"],
        "home": "runner-created", "tmpdir": "runner-created", "installedRuntimeVerified": True,
    }, "Mac CLI qualification lacks the isolated installer invocation")
    runtime = qualification["runtime"]
    require(isinstance(runtime, dict) and
            runtime.get("model") == {"name": "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf",
                                     "sha256": approval.MODEL_SHA256, "size": approval.MODEL_SIZE} and
            isinstance(runtime.get("llamaServer"), dict) and
            runtime["llamaServer"].get("sha256") == approval.LLAMA_BINARY_SHA256 and
            runtime.get("llamaRuntimeInventory", {}).get("sha256") ==
            "5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167" and
            isinstance(runtime.get("gateway"), dict) and
            runtime["gateway"].get("sha256") == mac.GATEWAY_BINARY_SHA256,
            "Mac CLI qualification runtime differs from committed pins")
    runner = qualification["runner"]
    require(isinstance(runner, dict) and runner.get("script") == {
        "name": "e2e-airplane-harnesses", "sha256": runner_sha, "size": runner_size} and
            isinstance(runner.get("id"), str) and
            re.fullmatch(r"[0-9]+-[0-9a-f]{32}", runner["id"]) and
            type(runner.get("harnessProcessCount")) is int and runner["harnessProcessCount"] == 3 and
            type(runner.get("serviceProcessCount")) is int and runner["serviceProcessCount"] >= 2 and
            type(runner.get("processCount")) is int and
            runner["processCount"] == 1 + runner["harnessProcessCount"] + runner["serviceProcessCount"] and
            runner.get("verifiedNoSurvivors") is True and
            isinstance(runner.get("processCohortSha256"), str) and
            SHA256.fullmatch(runner["processCohortSha256"]) and
            isinstance(runner.get("cleanupVerifiedAt"), str) and runner["cleanupVerifiedAt"],
            "Mac CLI qualification process cohort or cleanup is incomplete")
    execution = qualification["execution"]
    require(isinstance(execution, dict) and set(execution) == {
        "provenance", "runnerOs", "runnerArch", "osVersion", "runnerName",
        "releaseAuthorityOrigin", "releaseAuthorityId", "candidateSequence", "candidateEtag",
        "candidateRecordSha256", "candidateStaleIfMatchStatus", "isolationProfileSha256"} and
            execution["provenance"] == "direct-mac" and execution["runnerOs"] == "Darwin" and
            execution["runnerArch"] == "arm64" and isinstance(execution["osVersion"], str) and
            re.fullmatch(r"26(?:\.[0-9]+){1,2}", execution["osVersion"]) and
            isinstance(execution["runnerName"], str) and
            execution["runnerName"] and execution["releaseAuthorityOrigin"] == AUTHORITY_ORIGIN and
            execution["releaseAuthorityId"] == seq2["id"] and
            type(execution["candidateSequence"]) is int and execution["candidateSequence"] == 2 and
            execution["candidateEtag"] == record_etag(seq2) and
            execution["candidateRecordSha256"] == hashlib.sha256(canonical_record(seq2)).hexdigest() and
            type(execution["candidateStaleIfMatchStatus"]) is int and
            execution["candidateStaleIfMatchStatus"] == 412 and
            isinstance(execution["isolationProfileSha256"], str) and
            SHA256.fullmatch(execution["isolationProfileSha256"]),
            "Mac CLI qualification provenance does not bind the exact authority candidate")
    keys = approval.trusted_reviewers(TRUST, schema=TRUST_SCHEMA, purpose=PURPOSE, label="CLI")
    for reviewer in approval.REVIEWERS:
        approval.verify_approval(raw, reviewer, Path(bundle) / f"{reviewer}.cli-qualification.sig",
                                 keys, purpose=PURPOSE, label="CLI")
    return raw


def qualified_record(args):
    extra = set(QUALIFIED_PROFILE) - set(mac.PROFILE)
    assets, subject_sha = mac.candidate_assets(args.bundle, args.archive_sha256,
                                                args.gateway_bundle, args.gateway_qualification,
                                                args.gateway_evidence_root, extra=extra)
    _, expected = mac.candidate_records(args.commit, args.source_tree, assets, subject_sha)
    previous = parse_record(Path(args.sequence_two).read_bytes())
    require(canonical_record(previous) == canonical_record(expected),
            "Mac CLI qualification does not start at the exact deterministic sequence-2 candidate")
    raw = checked_qualification(args.bundle, previous, Path(args.source_repo))
    successor = copy.deepcopy(previous)
    successor["sequence"] = 3
    successor["previousETag"] = record_etag(previous)
    successor["assets"] = []
    for name, (media_type, role) in sorted(QUALIFIED_PROFILE.items()):
        digest, size = _hash_stable(Path(args.bundle) / name)
        successor["assets"].append({"name": name, "sha256": digest, "size": size,
                                    "mediaType": media_type, "role": role})
    successor["annotations"] = sorted(previous["annotations"] + [
        {"name": "cli-qualification-sha256", "value": hashlib.sha256(raw).hexdigest()}],
        key=lambda item: item["name"])
    validate_append_only(previous, successor)
    return successor


def command_qualified(args):
    record = qualified_record(args)
    output = Path(args.output)
    require(output.is_absolute() and not output.exists(), "qualified record needs a new absolute output")
    _write_new(output, canonical_record(record))
    _write_new(Path(str(output) + ".etag"), (record_etag(record) + "\n").encode("ascii"))


def command_publication(args):
    qualified = qualified_record(args)
    prior = parse_record(Path(args.sequence_three).read_bytes())
    require(canonical_record(prior) == canonical_record(qualified),
            "publication does not start at the exact signed Mac qualification")
    require(mac.OBJECT.fullmatch(args.tag_object) is not None, "annotated tag object is invalid")
    tag = subprocess.run(["git", "-C", args.source_repo, "cat-file", "-p", args.tag_object],
                         stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    require(tag.returncode == 0 and tag.stdout.startswith(
        f"object {args.commit}\ntype commit\ntag {mac.TAG}\n".encode()),
        "annotated Mac release tag does not target the qualified commit")
    remote = subprocess.run(["git", "-C", args.source_repo, "ls-remote", "--refs", "origin",
                             f"refs/tags/{mac.TAG}"], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=30)
    require(remote.returncode == 0 and remote.stdout ==
            f"{args.tag_object}\trefs/tags/{mac.TAG}\n".encode(),
            "remote Mac release tag object differs from the reviewed tag")
    fourth = copy.deepcopy(prior)
    fourth["sequence"] = 4
    fourth["previousETag"] = record_etag(prior)
    fourth["source"]["tagObject"] = args.tag_object
    validate_append_only(prior, fourth)
    fifth = finalized_record(fourth)
    output = Path(args.output)
    require(output.is_absolute() and not output.exists(), "publication plan needs a new absolute output")
    output.mkdir(mode=0o700, parents=False)
    _write_record(output, "candidate-sequence-4", fourth)
    _write_record(output, "expected-published-sequence-5", fifth)


def parser():
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    for command in ("qualified-record", "publication-plan"):
        sub = commands.add_parser(command)
        sub.add_argument("--commit", required=True)
        sub.add_argument("--source-tree", required=True)
        sub.add_argument("--source-repo", default=str(SOURCE))
        sub.add_argument("--bundle", required=True)
        sub.add_argument("--archive-sha256", required=True)
        sub.add_argument("--gateway-bundle", required=True)
        sub.add_argument("--gateway-qualification", required=True)
        sub.add_argument("--gateway-evidence-root", required=True)
        sub.add_argument("--sequence-two", required=True)
        sub.add_argument("--output", required=True)
        if command == "qualified-record":
            sub.set_defaults(function=command_qualified)
        else:
            sub.add_argument("--sequence-three", required=True)
            sub.add_argument("--tag-object", required=True)
            sub.set_defaults(function=command_publication)
    return result


def main():
    args = parser().parse_args()
    args.function(args)


if __name__ == "__main__":
    main()
