#!/usr/bin/env python3
"""Build or verify an unsigned Gateway 0.3.1 Darwin/arm64 review candidate."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile


VERSION = "0.3.1"
COMMIT = "2e2230aa575b8332d9d61bd1199536dde855ebd0"
TREE = "e612f0c87e0a9f70caab4a9a29e2185b34de0ef5"
EPOCH = 1793811475
SOURCE_SHA256 = "82940ebd45205c3cdd5cf9c14c72fceac1c5e866f01b224456582e3d50149752"
SOURCE_SIZE = 8345600
TOOLCHAIN_SHA256 = "afb16ae24c172f008db87119f34cf903a295bbad3fa526f7443914b0240b33c7"
TOOLCHAIN_SIZE = 69632655
TOOLCHAIN_ROOT = "golang.org/toolchain@v0.0.1-go1.26.10.darwin-arm64"
ARCHIVE = f"s46-gateway_{VERSION}_darwin_arm64.tar.gz"
RECORD = "gateway-direct-mac-build.candidate.json"
SCHEMA = "s46.gateway-direct-mac-build-candidate.v1"
MAX_BINARY = 64 << 20


class CandidateError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CandidateError(message)


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode()


def sha256(body):
    return hashlib.sha256(body).hexdigest()


def file_identity(path, maximum):
    path = Path(path)
    before = path.lstat()
    require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and 0 < before.st_size <= maximum,
            f"{path} must be one bounded regular file")
    descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    try:
        opened = os.fstat(descriptor)
        require(os.path.samestat(before, opened) and before.st_size == opened.st_size and
                before.st_mtime_ns == opened.st_mtime_ns and before.st_ctime_ns == opened.st_ctime_ns,
                f"{path} changed before opening")
        digest = hashlib.sha256()
        while chunk := os.read(descriptor, 1 << 20):
            digest.update(chunk)
        after = os.fstat(descriptor)
        require(os.path.samestat(opened, after) and opened.st_size == after.st_size and
                opened.st_mtime_ns == after.st_mtime_ns and opened.st_ctime_ns == after.st_ctime_ns,
                f"{path} changed while reading")
    finally:
        os.close(descriptor)
    return {"sha256": digest.hexdigest(), "sizeBytes": before.st_size}


def exact_source(repository):
    repository = Path(repository)
    require(repository.is_absolute() and repository.is_dir(), "API repository must be an absolute directory")
    environment = {"PATH": "/usr/bin:/bin", "HOME": "/var/empty", "LC_ALL": "C",
                   "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
                   "GIT_NO_REPLACE_OBJECTS": "1", "GIT_OPTIONAL_LOCKS": "0",
                   "GIT_TERMINAL_PROMPT": "0", "GIT_LFS_SKIP_SMUDGE": "1"}

    def git(*args):
        result = subprocess.run(["/usr/bin/git", "-C", str(repository), *args], env=environment,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120)
        require(result.returncode == 0, f"Git {args[0]} failed")
        return result.stdout

    require(git("cat-file", "-t", COMMIT) == b"commit\n", "source object is not a commit")
    require(git("rev-parse", f"{COMMIT}^{{tree}}") == (TREE + "\n").encode(), "source tree differs")
    require(git("show", "-s", "--format=%ct", COMMIT) == (str(EPOCH) + "\n").encode(), "source epoch differs")
    archive = git("archive", "--format=tar", COMMIT)
    require(len(archive) == SOURCE_SIZE and sha256(archive) == SOURCE_SHA256, "source archive differs")
    return archive


def make_archive(binary):
    require(0 < len(binary) <= MAX_BINARY, "Gateway binary is outside its size bound")
    tar_body = io.BytesIO()
    with tarfile.open(fileobj=tar_body, mode="w:", format=tarfile.USTAR_FORMAT) as output:
        member = tarfile.TarInfo("s46-gateway")
        member.mode, member.uid, member.gid, member.mtime = 0o755, 0, 0, EPOCH
        member.size = len(binary)
        output.addfile(member, io.BytesIO(binary))
    compressed = io.BytesIO()
    with gzip.GzipFile(fileobj=compressed, mode="wb", filename="", mtime=0, compresslevel=9) as output:
        output.write(tar_body.getvalue())
    return compressed.getvalue()


def inspect_archive(body):
    require(0 < len(body) <= MAX_BINARY + (1 << 20), "Gateway archive is outside its size bound")
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(body)) as source:
            tar_body = source.read(MAX_BINARY + (1 << 20) + 1)
        with tarfile.open(fileobj=io.BytesIO(tar_body), mode="r:") as source:
            members = source.getmembers()
            require(len(members) == 1, "Gateway archive must contain one member")
            member = members[0]
            require(member.name == "s46-gateway" and member.isreg() and member.mode == 0o755 and
                    member.uid == 0 and member.gid == 0 and member.mtime == EPOCH,
                    "Gateway member metadata differs")
            binary = source.extractfile(member).read(MAX_BINARY + 1)
            require(0 < len(binary) == member.size <= MAX_BINARY, "Gateway member size differs")
    except (OSError, EOFError, tarfile.TarError) as error:
        raise CandidateError(f"Gateway archive is invalid: {error}") from error
    require(make_archive(binary) == body, "Gateway archive encoding is not deterministic")
    return {"sha256": sha256(binary), "sizeBytes": len(binary)}


def write_new(path, body):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o400)
    try:
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(body)
            output.flush()
            os.fsync(output.fileno())
    finally:
        os.close(descriptor)


def build_once(go, source_tar, root):
    root.mkdir(mode=0o700)
    source = root / "source"
    source.mkdir(mode=0o700)
    source_archive = root / "source.tar"
    source_archive.write_bytes(source_tar)
    subprocess.run(["/usr/bin/tar", "-xf", str(source_archive), "-C", str(source)], check=True,
                   env={"PATH": "/usr/bin:/bin"}, stdout=subprocess.DEVNULL)
    for name in ("home", "tmp", "cache", "modules"):
        (root / name).mkdir(mode=0o700)
    binary = root / "s46-gateway"
    environment = {
        "CGO_ENABLED": "0", "GOOS": "darwin", "GOARCH": "arm64", "GOARM64": "v8.0",
        "GOENV": "off", "GOFLAGS": "-mod=vendor", "GOPROXY": "off", "GOSUMDB": "off",
        "GOTOOLCHAIN": "local", "GOWORK": "off", "GOTELEMETRY": "off",
        "GOROOT": str(go.parent.parent), "HOME": str(root / "home"), "TMPDIR": str(root / "tmp"),
        "GOCACHE": str(root / "cache"), "GOMODCACHE": str(root / "modules"),
        "PATH": "/usr/bin:/bin", "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
        "SOURCE_DATE_EPOCH": str(EPOCH),
    }
    result = subprocess.run([str(go), "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
                             "-ldflags=-s -w -buildid=", "-o", str(binary), "./cmd/s46-gateway"],
                            cwd=source, env=environment, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=900)
    require(result.returncode == 0, f"Gateway build failed: {result.stderr.decode(errors='replace')[-1000:]}")
    identity = file_identity(binary, MAX_BINARY)
    return make_archive(binary.read_bytes()), identity


def command_build(args):
    require(sys.platform == "darwin" and platform.machine() == "arm64", "native Darwin/arm64 is required")
    macos = subprocess.check_output(["/usr/bin/sw_vers", "-productVersion"], text=True).strip()
    require(re.fullmatch(r"26\.[0-9]+(?:\.[0-9]+)?", macos) is not None, "macOS 26 is required")
    toolchain = Path(args.toolchain_zip)
    require(file_identity(toolchain, 128 << 20) ==
            {"sha256": TOOLCHAIN_SHA256, "sizeBytes": TOOLCHAIN_SIZE}, "toolchain ZIP differs")
    source_tar = exact_source(args.api_repository)
    output = Path(args.output)
    require(output.is_absolute() and not output.exists() and not output.is_symlink(),
            "output must be one absent absolute directory")
    output.mkdir(mode=0o700)
    with tempfile.TemporaryDirectory(prefix="s46-gateway-direct-mac-") as temporary:
        root = Path(temporary)
        private_zip = root / "go1.26.10.darwin-arm64.zip"
        shutil.copyfile(toolchain, private_zip)
        require(file_identity(private_zip, 128 << 20) ==
                {"sha256": TOOLCHAIN_SHA256, "sizeBytes": TOOLCHAIN_SIZE},
                "private toolchain ZIP differs")
        subprocess.run(["/usr/bin/ditto", "-x", "-k", str(private_zip), str(root / "toolchain")],
                       check=True, env={"PATH": "/usr/bin:/bin"}, stdout=subprocess.DEVNULL)
        go = root / "toolchain" / TOOLCHAIN_ROOT / "bin/go"
        require(go.is_file(), "extracted toolchain is incomplete")
        version = subprocess.check_output(
            [str(go), "version"], text=True, timeout=15,
            env={"GOROOT": str(go.parent.parent), "GOTOOLCHAIN": "local",
                 "GOTELEMETRY": "off", "GOPROXY": "off", "GOSUMDB": "off",
                 "HOME": "/var/empty", "PATH": "/usr/bin:/bin"}).strip()
        require(version == "go version go1.26.10 darwin/arm64", "extracted Go version differs")
        first, binary_first = build_once(go, source_tar, root / "first")
        second, binary_second = build_once(go, source_tar, root / "second")
    require(first == second and binary_first == binary_second,
            "two fresh workroots did not reproduce exact Gateway bytes")
    require(inspect_archive(first) == binary_first, "Gateway archive differs from binary")
    for name in ("first", "second"):
        directory = output / name
        directory.mkdir(mode=0o700)
        write_new(directory / ARCHIVE, first)
    record = {
        "schema": SCHEMA, "state": "unsigned-candidate", "repository": "sovereign46/api",
        "version": VERSION, "platform": {"os": "darwin", "architecture": "arm64", "macOS": macos},
        "source": {"commit": COMMIT, "tree": TREE, "sourceDateEpoch": EPOCH,
                   "archiveSha256": SOURCE_SHA256, "archiveSizeBytes": SOURCE_SIZE},
        "toolchain": {"module": "golang.org/toolchain", "version": "go1.26.10",
                      "archiveSha256": TOOLCHAIN_SHA256, "archiveSizeBytes": TOOLCHAIN_SIZE},
        "build": {"workroots": 2, "sameHost": True, "network": "go-module-resolution-off",
                  "flags": ["-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid="]},
        "gateway": {"name": ARCHIVE, "archiveSha256": sha256(first), "archiveSizeBytes": len(first),
                    "binarySha256": binary_first["sha256"], "binarySizeBytes": binary_first["sizeBytes"]},
    }
    write_new(output / RECORD, canonical(record))
    print(f"unsigned Gateway review candidate: {output}")


def command_verify(args):
    root = Path(args.bundle)
    raw = (root / RECORD).read_bytes()
    record = json.loads(raw)
    require(isinstance(record, dict), "candidate record must be an object")
    require(raw == canonical(record), "candidate record is not canonical")
    require(set(record) == {"schema", "state", "repository", "version", "platform",
                            "source", "toolchain", "build", "gateway"},
            "candidate record has unexpected fields")
    require(record.get("schema") == SCHEMA and record.get("state") == "unsigned-candidate" and
            record.get("repository") == "sovereign46/api" and record.get("version") == VERSION,
            "candidate record has the wrong identity")
    require(record.get("source") == {"commit": COMMIT, "tree": TREE, "sourceDateEpoch": EPOCH,
                                     "archiveSha256": SOURCE_SHA256, "archiveSizeBytes": SOURCE_SIZE},
            "candidate source differs")
    require(record.get("toolchain") == {"module": "golang.org/toolchain", "version": "go1.26.10",
                                        "archiveSha256": TOOLCHAIN_SHA256, "archiveSizeBytes": TOOLCHAIN_SIZE},
            "candidate toolchain differs")
    expected_build = {"workroots": 2, "sameHost": True, "network": "go-module-resolution-off",
                      "flags": ["-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid="]}
    require(record.get("build") == expected_build, "candidate build profile differs")
    platform_record = record.get("platform", {})
    require(isinstance(platform_record, dict) and
            set(platform_record) == {"os", "architecture", "macOS"} and
            platform_record.get("os") == "darwin" and platform_record.get("architecture") == "arm64" and
            isinstance(platform_record.get("macOS"), str) and
            re.fullmatch(r"26\.[0-9]+(?:\.[0-9]+)?", platform_record["macOS"]) is not None,
            "candidate platform differs")
    gateway = record.get("gateway", {})
    require(isinstance(gateway, dict) and
            set(gateway) == {"name", "archiveSha256", "archiveSizeBytes", "binarySha256", "binarySizeBytes"}
            and gateway["name"] == ARCHIVE, "candidate Gateway fields differ")
    for name in ("first", "second"):
        directory = root / name
        require({item.name for item in directory.iterdir()} == {ARCHIVE},
                f"candidate {name} directory contains unexpected content")
        path = directory / ARCHIVE
        require(file_identity(path, MAX_BINARY + (1 << 20)) ==
                {"sha256": gateway["archiveSha256"], "sizeBytes": gateway["archiveSizeBytes"]},
                f"candidate {name} archive differs")
        require(inspect_archive(path.read_bytes()) ==
                {"sha256": gateway["binarySha256"], "sizeBytes": gateway["binarySizeBytes"]},
                f"candidate {name} binary differs")
    require({item.name for item in root.iterdir()} == {RECORD, "first", "second"},
            "candidate bundle contains unexpected content")
    print(f"unsigned candidate bytes verified: {sha256(raw)}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    build = commands.add_parser("build")
    build.add_argument("--api-repository", required=True)
    build.add_argument("--toolchain-zip", required=True)
    build.add_argument("--output", required=True)
    build.set_defaults(function=command_build)
    verify = commands.add_parser("verify")
    verify.add_argument("--bundle", required=True)
    verify.set_defaults(function=command_verify)
    args = parser.parse_args()
    try:
        args.function(args)
    except (CandidateError, OSError, ValueError, subprocess.TimeoutExpired, tarfile.TarError) as error:
        parser.exit(1, f"gateway-direct-mac: {error}\n")


if __name__ == "__main__":
    main()
