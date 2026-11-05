#!/usr/bin/env python3
"""Prepare a separately named, unsigned Mac airplane CLI authority candidate."""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import re

try:
    from cli_release_authority import _hash_stable, _write_new, _write_record, OBJECT, RECORD_SCHEMA, REPOSITORY
    from gateway_direct_mac_approval import checked_subject, parse_json, sha256, verify_approvals
    from release_authority import REQUEST_SCHEMA, canonical_record, canonical_request, record_etag, release_id
except ModuleNotFoundError:  # Imported as scripts.cli_mac_airplane_candidate in tests.
    from scripts.cli_release_authority import _hash_stable, _write_new, _write_record, OBJECT, RECORD_SCHEMA, REPOSITORY
    from scripts.gateway_direct_mac_approval import checked_subject, parse_json, sha256, verify_approvals
    from scripts.release_authority import REQUEST_SCHEMA, canonical_record, canonical_request, record_etag, release_id


TAG = "v0.3.1-mac.1"
ARCHIVE = "s46_0.3.1_darwin_arm64.tar.gz"
GATEWAY_ARCHIVE_SHA256 = "ef9d322d2bb775ae065d712280b786841c149ed959acf70942c25c74e8f32d3a"
GATEWAY_BINARY_SHA256 = "6abaef72f4c0975fc459db8f7cad89b88f99b04e970464685c07180518382ca2"
PROFILE = {
    ARCHIVE: ("application/gzip", "cli-archive"),
    "gateway-approval-subject.json": ("application/json", "gateway-approval-subject"),
    "fredoliveira.sig": ("text/plain", "gateway-approval-signature"),
    "goncalossilva.sig": ("text/plain", "gateway-approval-signature"),
}
SHA256 = re.compile(r"[0-9a-f]{64}\Z")


def candidate_assets(bundle, expected_archive_sha256, gateway_bundle, qualification, evidence_root):
    bundle = Path(bundle)
    if not bundle.is_absolute() or bundle.is_symlink() or not bundle.is_dir():
        raise SystemExit("Mac candidate bundle must be an absolute real directory")
    entries = list(bundle.iterdir())
    if len(entries) != len(PROFILE) or {item.name for item in entries} != set(PROFILE):
        raise SystemExit("Mac candidate bundle must contain only its four exact assets")
    if not SHA256.fullmatch(expected_archive_sha256):
        raise SystemExit("Mac CLI archive needs an independently supplied SHA-256")

    subject = checked_subject(gateway_bundle, qualification, evidence_root)
    subject_record = parse_json(subject, "Gateway approval subject")
    build = subject_record["build"]
    if (build["gatewayArchiveSha256"] != GATEWAY_ARCHIVE_SHA256 or
            build["gatewayBinarySha256"] != GATEWAY_BINARY_SHA256):
        raise SystemExit("Gateway approval does not pin the reviewed Mac archive and binary")
    subject_path = bundle / "gateway-approval-subject.json"
    subject_sha256, subject_size = _hash_stable(subject_path)
    if subject_sha256 != sha256(subject) or subject_size != len(subject):
        raise SystemExit("Gateway approval subject differs from the verified packet")
    verify_approvals(subject, bundle)

    archive_sha256, _ = _hash_stable(bundle / ARCHIVE)
    if archive_sha256 != expected_archive_sha256:
        raise SystemExit("Mac CLI archive does not match its independent SHA-256 pin")
    assets = []
    for name, (media_type, role) in sorted(PROFILE.items()):
        digest, size = _hash_stable(bundle / name)
        assets.append({"name": name, "sha256": digest, "size": size,
                       "mediaType": media_type, "role": role})
    return assets, subject_sha256


def candidate_records(commit, source_tree, assets, gateway_subject_sha256):
    if not OBJECT.fullmatch(commit) or not OBJECT.fullmatch(source_tree):
        raise SystemExit("Mac CLI commit and source tree must be full object IDs")
    annotations = [
        {"name": "gateway-approval-subject-sha256", "value": gateway_subject_sha256},
        {"name": "gateway-archive-sha256", "value": GATEWAY_ARCHIVE_SHA256},
        {"name": "mac-cli-archive-sha256", "value": assets_by_name(assets)[ARCHIVE]["sha256"]},
        {"name": "profile", "value": "mac-airplane-candidate-v1"},
        {"name": "source-tree", "value": source_tree},
    ]
    bundle_sha256 = hashlib.sha256((json.dumps(assets, sort_keys=True, separators=(",", ":")) + "\n").encode()).hexdigest()
    initial = {
        "schema": RECORD_SCHEMA,
        "id": release_id(REPOSITORY, TAG),
        "state": "candidate",
        "sequence": 1,
        "previousETag": "",
        "source": {"repository": REPOSITORY, "tag": TAG, "commit": commit,
                   "tagObject": "", "bundleSha256": bundle_sha256},
        "assets": assets,
        "annotations": annotations,
    }
    second = copy.deepcopy(initial)
    second["sequence"] = 2
    second["previousETag"] = record_etag(initial)
    second["annotations"] = sorted(annotations + [{"name": "cas-probe", "value": "sequence-2"}],
                                   key=lambda item: item["name"])
    return initial, second


def assets_by_name(assets):
    return {asset["name"]: asset for asset in assets}


def command_stage(args):
    assets, subject_sha256 = candidate_assets(args.bundle, args.archive_sha256,
                                              args.gateway_bundle, args.gateway_qualification,
                                              args.gateway_evidence_root)
    initial, second = candidate_records(args.commit, args.source_tree, assets, subject_sha256)
    output = Path(args.output)
    if not output.is_absolute() or output.parent.is_symlink():
        raise SystemExit("Mac candidate records need an absolute output under a real directory")
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    stale = copy.deepcopy(second)
    stale["annotations"] = sorted(
        [item if item["name"] != "cas-probe" else
         {"name": "cas-probe", "value": "stale-replacement-must-not-land"}
         for item in second["annotations"]], key=lambda item: item["name"])
    _write_record(output, "candidate-sequence-1", initial)
    _write_record(output, "candidate-sequence-2", second)
    _write_new(output / "candidate-stale-replace.request", canonical_request({
        "schema": REQUEST_SCHEMA, "operation": "replaceCandidate",
        "ifMatch": record_etag(initial), "id": "", "sha256": "", "size": 0,
        "record": stale,
    }))
    _write_new(output / "release-id", (initial["id"] + "\n").encode("ascii"))
    _write_new(output / "candidate-record.sha256",
               (hashlib.sha256(canonical_record(second)).hexdigest() + "\n").encode("ascii"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--source-tree", required=True)
    parser.add_argument("--bundle", required=True)
    parser.add_argument("--archive-sha256", required=True)
    parser.add_argument("--gateway-bundle", required=True)
    parser.add_argument("--gateway-qualification", required=True)
    parser.add_argument("--gateway-evidence-root", required=True)
    parser.add_argument("--output", required=True)
    command_stage(parser.parse_args())


if __name__ == "__main__":
    main()
