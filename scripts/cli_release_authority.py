#!/usr/bin/env python3
"""Construct the CLI's exact deterministic release-authority revisions."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import stat

try:
    from release_authority import (
        REQUEST_SCHEMA,
        canonical_record,
        canonical_request,
        finalized_record,
        parse_record,
        record_etag,
        release_id,
    )
except ModuleNotFoundError:  # Imported as scripts.cli_release_authority in tests.
    from scripts.release_authority import (
        REQUEST_SCHEMA,
        canonical_record,
        canonical_request,
        finalized_record,
        parse_record,
        record_etag,
        release_id,
    )

REPOSITORY = "sovereign46/cli"
AUTHORITY_ORIGIN = "https://models.s46.dev/release-authority/v1"
RECORD_SCHEMA = "s46.release-authority.record.v1"
OBJECT = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
VERSION_TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
MAX_FILE_BYTES = 256 * 1024 * 1024


def _profile(tag, qualified):
    version = tag[1:]
    archives = [f"s46_{version}_{platform}.tar.gz" for platform in (
        "darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64",
    )]
    profile = {
        "checksums.txt": ("text/plain", "archive-checksums"),
        "release-build.json": ("application/json", "build-receipt"),
        "release-build.json.sig": ("text/plain", "build-receipt-signature"),
    }
    for archive in archives:
        profile[archive] = ("application/gzip", "cli-archive")
        if qualified:
            profile[archive + ".qualification.json"] = ("application/json", "qualification")
            profile[archive + ".qualification.json.sig"] = ("text/plain", "qualification-signature")
    return profile


def _hash_stable(path):
    before = os.lstat(path)
    if (not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or
            before.st_size <= 0 or before.st_size > MAX_FILE_BYTES):
        raise SystemExit(f"release asset is not a singly linked bounded regular file: {path}")
    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    try:
        opened = os.fstat(descriptor)
        digest = hashlib.sha256()
        size = 0
        while chunk := os.read(descriptor, 1024 * 1024):
            size += len(chunk)
            if size > MAX_FILE_BYTES:
                raise SystemExit(f"release asset exceeds its size bound: {path}")
            digest.update(chunk)
        after = os.fstat(descriptor)
    finally:
        os.close(descriptor)
    current = os.lstat(path)
    identity = lambda value: (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns, value.st_nlink)
    if len({identity(before), identity(opened), identity(after), identity(current)}) != 1 or size != before.st_size:
        raise SystemExit(f"release asset changed while being hashed: {path}")
    return digest.hexdigest(), size


def _assets(directory, tag, qualified):
    directory = Path(directory)
    if not directory.is_absolute() or directory.is_symlink() or not directory.is_dir():
        raise SystemExit("release bundle must be an absolute real directory")
    profile = _profile(tag, qualified)
    entries = [item for item in directory.iterdir()]
    if {item.name for item in entries} != set(profile) or len(entries) != len(profile):
        raise SystemExit(f"release bundle must contain the exact {len(profile)}-file cohort")
    result = []
    for name in sorted(profile):
        digest, size = _hash_stable(directory / name)
        media_type, role = profile[name]
        result.append({"name": name, "sha256": digest, "size": size, "mediaType": media_type, "role": role})
    return result


def _candidate(tag, commit, source_tree, directory, qualified_bundle=False):
    if not VERSION_TAG.fullmatch(tag) or not OBJECT.fullmatch(commit) or not OBJECT.fullmatch(source_tree):
        raise SystemExit("release tag, commit, or source tree is not canonical")
    assets = _assets(directory, tag, qualified_bundle)
    if qualified_bundle:
        build_names = set(_profile(tag, False))
        assets = [asset for asset in assets if asset["name"] in build_names]
    by_name = {asset["name"]: asset for asset in assets}
    receipt = by_name["release-build.json"]["sha256"]
    signature = by_name["release-build.json.sig"]["sha256"]
    annotations = [
        {"name": "build-receipt-sha256", "value": receipt},
        {"name": "build-signature-sha256", "value": signature},
        {"name": "source-tree", "value": source_tree},
    ]
    initial = {
        "schema": RECORD_SCHEMA,
        "id": release_id(REPOSITORY, tag),
        "state": "candidate",
        "sequence": 1,
        "previousETag": "",
        "source": {
            "repository": REPOSITORY,
            "tag": tag,
            "commit": commit,
            "tagObject": "",
            "bundleSha256": receipt,
        },
        "assets": assets,
        "annotations": annotations,
    }
    sequence_two = copy.deepcopy(initial)
    sequence_two["sequence"] = 2
    sequence_two["previousETag"] = record_etag(initial)
    sequence_two["annotations"] = sorted(
        annotations + [{"name": "cas-probe", "value": "sequence-2"}], key=lambda item: item["name"],
    )
    return initial, sequence_two


def _write_new(path, body):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(descriptor, "wb") as output:
        output.write(body)
        output.flush()
        os.fsync(output.fileno())


def _write_record(directory, name, record):
    _write_new(directory / f"{name}.json", canonical_record(record))
    _write_new(directory / f"{name}.etag", (record_etag(record) + "\n").encode("ascii"))


def command_stage(args):
    output = Path(args.output)
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    initial, sequence_two = _candidate(args.tag, args.commit, args.source_tree, args.bundle)
    stale = copy.deepcopy(sequence_two)
    annotations = {item["name"]: item["value"] for item in stale["annotations"]}
    annotations["cas-probe"] = "stale-replacement-must-not-land"
    stale["annotations"] = [{"name": name, "value": annotations[name]} for name in sorted(annotations)]
    _write_record(output, "candidate-sequence-1", initial)
    _write_record(output, "candidate-sequence-2", sequence_two)
    _write_new(output / "candidate-stale-replace.request", canonical_request({
        "schema": REQUEST_SCHEMA,
        "operation": "replaceCandidate",
        "ifMatch": record_etag(initial),
        "id": "",
        "sha256": "",
        "size": 0,
        "record": stale,
    }))
    _write_new(output / "release-id", (initial["id"] + "\n").encode("ascii"))
    _write_new(output / "candidate-record.sha256", (hashlib.sha256(canonical_record(sequence_two)).hexdigest() + "\n").encode("ascii"))


def _qualified_records(args):
    sequence_one, sequence_two = _candidate(args.tag, args.commit, args.source_tree, args.bundle, qualified_bundle=True)
    sequence_three = copy.deepcopy(sequence_two)
    sequence_three["sequence"] = 3
    sequence_three["previousETag"] = record_etag(sequence_two)
    sequence_three["assets"] = _assets(args.bundle, args.tag, True)
    return sequence_one, sequence_two, sequence_three


def command_qualified(args):
    output = Path(args.output)
    if output.exists() or output.is_symlink():
        raise SystemExit("qualified record output already exists")
    _, sequence_two, sequence_three = _qualified_records(args)
    previous = parse_record(Path(args.sequence_two).read_bytes())
    if canonical_record(previous) != canonical_record(sequence_two):
        raise SystemExit("qualified append does not start at the exact deterministic sequence-2 candidate")
    _write_record_output(output, sequence_three)


def command_qualified_recovery(args):
    sequence_one, sequence_two, sequence_three = _qualified_records(args)
    _write_record_output(Path(args.sequence_one_output), sequence_one)
    _write_record_output(Path(args.sequence_two_output), sequence_two)
    _write_record_output(Path(args.sequence_three_output), sequence_three)


def _write_record_output(output, record):
    _write_new(output, canonical_record(record))
    _write_new(Path(str(output) + ".etag"), (record_etag(record) + "\n").encode("ascii"))


def command_tagged(args):
    if not OBJECT.fullmatch(args.tag_object):
        raise SystemExit("annotated tag object is not canonical")
    previous = parse_record(Path(args.sequence_three).read_bytes())
    if previous["state"] != "candidate" or previous["sequence"] != 3 or previous["source"]["tagObject"] != "":
        raise SystemExit("tag-object append requires the exact untagged sequence-3 candidate")
    successor = copy.deepcopy(previous)
    successor["sequence"] = 4
    successor["previousETag"] = record_etag(previous)
    successor["source"]["tagObject"] = args.tag_object
    _write_new(args.output, canonical_record(successor))
    _write_new(args.output + ".etag", (record_etag(successor) + "\n").encode("ascii"))


def command_expected_release(args):
    candidate = parse_record(Path(args.sequence_four).read_bytes())
    if candidate["state"] != "candidate" or candidate["sequence"] != 4:
        raise SystemExit("expected release requires the exact sequence-4 candidate")
    _write_new(args.output, canonical_record(finalized_record(candidate)))
    _write_new(args.output + ".etag", (record_etag(finalized_record(candidate)) + "\n").encode("ascii"))


def parser():
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    stage = commands.add_parser("stage-records")
    for item in (stage,):
        item.add_argument("--tag", required=True)
        item.add_argument("--commit", required=True)
        item.add_argument("--source-tree", required=True)
        item.add_argument("--bundle", required=True)
    stage.add_argument("--output", required=True)
    stage.set_defaults(function=command_stage)

    qualified = commands.add_parser("qualified-record")
    qualified.add_argument("--tag", required=True)
    qualified.add_argument("--commit", required=True)
    qualified.add_argument("--source-tree", required=True)
    qualified.add_argument("--bundle", required=True)
    qualified.add_argument("--sequence-two", required=True)
    qualified.add_argument("--output", required=True)
    qualified.set_defaults(function=command_qualified)

    recovery = commands.add_parser("qualified-recovery-records")
    recovery.add_argument("--tag", required=True)
    recovery.add_argument("--commit", required=True)
    recovery.add_argument("--source-tree", required=True)
    recovery.add_argument("--bundle", required=True)
    recovery.add_argument("--sequence-one-output", required=True)
    recovery.add_argument("--sequence-two-output", required=True)
    recovery.add_argument("--sequence-three-output", required=True)
    recovery.set_defaults(function=command_qualified_recovery)

    tagged = commands.add_parser("tagged-record")
    tagged.add_argument("--sequence-three", required=True)
    tagged.add_argument("--tag-object", required=True)
    tagged.add_argument("--output", required=True)
    tagged.set_defaults(function=command_tagged)

    published = commands.add_parser("expected-release")
    published.add_argument("--sequence-four", required=True)
    published.add_argument("--output", required=True)
    published.set_defaults(function=command_expected_release)
    return result


def main():
    args = parser().parse_args()
    args.function(args)


if __name__ == "__main__":
    main()
