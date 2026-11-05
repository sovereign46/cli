#!/usr/bin/env python3
"""Submit one exact Gateway Mac approval through an authenticated private issue."""

import argparse
import base64
import binascii
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

if __package__:
    from . import gateway_direct_mac_approval as approval
    from .gateway_direct_mac import canonical, sha256
else:
    import gateway_direct_mac_approval as approval
    from gateway_direct_mac import canonical, sha256


SUBJECT_SHA256 = "e913b6ca19523abb0a6326143ba55b48d00a7320491f7af735ce1820f452eb7c"
PACKET_MANIFEST_SHA256 = "e3fe07a3beaf165890ba7042e6709f6269d0da797589d63acb8ba00cebefb64c"
ISSUE_URL = re.compile(r"https://github\.com/sovereign46/models-cli/issues/([1-9][0-9]*)\Z")
TRUST_API_PATH = "repos/sovereign46/cli/contents/scripts/gateway-direct-mac-approval-trust.json?ref=main"
REVIEW_REPOSITORY = "sovereign46/models-cli"
RECORD_SCHEMA = "s46.gateway-direct-mac-reviewer-submission.v1"


def require(condition, message):
    approval.require(condition, message)


def gh_api(path, payload=None):
    command = ["gh", "api", "--hostname", "github.com"]
    if payload is not None:
        command += ["--method", "POST", "--input", "-"]
    command.append(path)
    result = subprocess.run(command, input=canonical(payload) if payload is not None else None,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)
    require(result.returncode == 0, f"GitHub API request failed: {path}: "
            f"{result.stderr.decode('utf-8', 'replace')[:300]}")
    try:
        return json.loads(result.stdout)
    except ValueError as error:
        raise approval.ApprovalError(f"GitHub API returned invalid JSON for {path}") from error


def packet_file(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        before = os.fstat(descriptor)
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1
                and 0 <= before.st_size <= (16 << 20), f"Gateway packet file is not bounded: {path}")
        with os.fdopen(os.dup(descriptor), "rb") as source:
            raw = source.read((16 << 20) + 1)
        after = os.fstat(descriptor)
        require(len(raw) == before.st_size and (before.st_dev, before.st_ino, before.st_mtime_ns,
                before.st_size) == (after.st_dev, after.st_ino, after.st_mtime_ns, after.st_size),
                f"Gateway packet file changed while reading: {path}")
        return raw
    finally:
        os.close(descriptor)


def checked_packet(packet):
    packet = Path(packet)
    manifest = approval.read_exact(packet / "SHA256SUMS", 16384)
    require(sha256(manifest) == PACKET_MANIFEST_SHA256, "Gateway review packet manifest differs")
    for line in manifest.decode("ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^\s]+)", line)
        require(match is not None, "Gateway review packet manifest is malformed")
        name = match.group(2)
        require(not name.startswith("/") and all(part not in ("", ".", "..") for part in name.split("/")),
                "Gateway review packet manifest contains an unsafe path")
        require(sha256(packet_file(packet / name)) == match.group(1),
                f"Gateway review packet file differs: {name}")
    subject = approval.checked_subject(packet / "build",
                                       packet / "qualification/qualification.candidate.json",
                                       packet / "diagnostic")
    require(sha256(subject) == SUBJECT_SHA256, "Gateway approval subject differs from handoff")
    require(approval.read_exact(packet / "approval/gateway-approval-subject.candidate.json", 4096) == subject,
            "Gateway approval subject file differs from verified inputs")
    return subject


def checked_issue(issue_url, trust_digest):
    match = ISSUE_URL.fullmatch(issue_url)
    require(match is not None, "Gateway review issue must be a sovereign46/models-cli issue URL")
    issue_number = match.group(1)
    user = gh_api("user")
    login = user.get("login") if isinstance(user, dict) else None
    require(login in approval.REVIEWERS, "authenticated GitHub user is not a Gateway reviewer")
    repository = gh_api(f"repos/{REVIEW_REPOSITORY}")
    require(isinstance(repository, dict) and repository.get("full_name") == REVIEW_REPOSITORY
            and repository.get("private") is True, "Gateway review repository is not the expected private repo")
    issue = gh_api(f"repos/{REVIEW_REPOSITORY}/issues/{issue_number}")
    require(isinstance(issue, dict) and issue.get("state") == "open" and issue.get("locked") is False
            and "pull_request" not in issue, "Gateway review issue is not open and unlocked")
    body = issue.get("body")
    require(isinstance(body, str) and SUBJECT_SHA256 in body and PACKET_MANIFEST_SHA256 in body
            and trust_digest in body, "Gateway review issue does not bind the subject, packet, and trust")
    return login, issue_number


def checked_trust(trust_file):
    local = approval.read_exact(trust_file, 4096)
    remote = gh_api(TRUST_API_PATH)
    require(isinstance(remote, dict) and remote.get("type") == "file"
            and remote.get("encoding") == "base64" and isinstance(remote.get("content"), str),
            "Gateway trust is unavailable on CLI main")
    try:
        published = base64.b64decode("".join(remote["content"].split()), validate=True)
    except (ValueError, binascii.Error) as error:
        raise approval.ApprovalError("Gateway trust on CLI main is malformed") from error
    require(local == published, "local Gateway trust differs from CLI main")
    keys = approval.trusted_reviewers(trust_file)
    require(approval.read_exact(trust_file, 4096) == local, "Gateway trust changed while reading")
    return keys, sha256(local)


def checked_private_key(key_path, login, trusted_key):
    result = subprocess.run(["ssh-keygen", "-y", "-f", str(key_path)], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=30)
    require(result.returncode == 0, "cannot read the reviewer's SSH private key")
    fields = result.stdout.decode("ascii", "replace").split()
    require(len(fields) >= 2 and " ".join(fields[:2]) == trusted_key,
            f"private SSH key does not match the enrolled Gateway key for {login}")


def sign_subject(subject, key_path, login, trusted_keys):
    result = subprocess.run(["ssh-keygen", "-Y", "sign", "-f", str(key_path), "-n", approval.PURPOSE],
                            input=subject, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
    require(result.returncode == 0, "SSH signing failed")
    with tempfile.TemporaryDirectory(prefix="s46-gateway-review-") as directory:
        signature = Path(directory) / f"{login}.sig"
        signature.write_bytes(result.stdout)
        approval.verify_approval(subject, login, signature, trusted_keys)
    return result.stdout.decode("ascii")


def comments(issue_number):
    for page in range(1, 101):
        items = gh_api(f"repos/{REVIEW_REPOSITORY}/issues/{issue_number}/comments?per_page=100&page={page}")
        require(isinstance(items, list), "Gateway review comments response is invalid")
        yield from items
        if len(items) < 100:
            return
    raise approval.ApprovalError("Gateway review issue has too many comments")


def checked_comment(comment, login, body, issue_number):
    require(isinstance(comment, dict) and isinstance(comment.get("user"), dict)
            and comment["user"].get("login") == login and comment.get("body") == body,
            "persisted Gateway review comment differs from authenticated submission")
    require(isinstance(comment.get("id"), int) and comment.get("html_url") ==
            f"https://github.com/{REVIEW_REPOSITORY}/issues/{issue_number}#issuecomment-{comment['id']}",
            "persisted Gateway review comment URL is invalid")
    return comment["html_url"]


def publish_signature(issue_number, login, record):
    user = gh_api("user")
    require(isinstance(user, dict) and user.get("login") == login,
            "authenticated GitHub reviewer changed before submission")
    body = canonical(record).decode("ascii")
    existing = []
    for item in comments(issue_number):
        if not isinstance(item, dict) or not isinstance(item.get("user"), dict) \
                or item["user"].get("login") != login:
            continue
        try:
            previous = json.loads(item.get("body", ""))
        except (TypeError, ValueError):
            continue
        if isinstance(previous, dict) and previous.get("schema") == RECORD_SCHEMA:
            require(item.get("body") == body, "reviewer already submitted a different Gateway approval")
            existing.append(item)
    require(len(existing) <= 1, "duplicate Gateway approval comments already exist")
    if existing:
        comment = existing[0]
    else:
        comment = gh_api(f"repos/{REVIEW_REPOSITORY}/issues/{issue_number}/comments", {"body": body})
    comment_id = comment.get("id") if isinstance(comment, dict) else None
    require(isinstance(comment_id, int), "Gateway review comment has no id")
    persisted = gh_api(f"repos/{REVIEW_REPOSITORY}/issues/comments/{comment_id}")
    return checked_comment(persisted, login, body, issue_number)


def submit(packet, key, issue_url, trust_file):
    subject = checked_packet(packet)
    trusted_keys, trust_digest = checked_trust(trust_file)
    login, issue_number = checked_issue(issue_url, trust_digest)
    checked_private_key(key, login, trusted_keys[login])
    print(f"Reviewer: {login}\nSubject SHA-256: {SUBJECT_SHA256}\n"
          f"Packet manifest SHA-256: {PACKET_MANIFEST_SHA256}\nTrust SHA-256: {trust_digest}")
    require(input("Type the subject SHA-256 after reviewing the packet to approve publication: ").strip()
            == SUBJECT_SHA256, "Gateway publication approval was not confirmed")
    signature = sign_subject(subject, key, login, trusted_keys)
    record = {"schema": RECORD_SCHEMA, "githubLogin": login, "subjectSha256": SUBJECT_SHA256,
              "packetManifestSha256": PACKET_MANIFEST_SHA256, "trustSha256": trust_digest,
              "signature": signature}
    url = publish_signature(issue_number, login, record)
    print(f"Verified Gateway approval submitted: {url}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packet", required=True, type=Path, help="unmodified 20261105 Gateway review packet")
    parser.add_argument("--key", required=True, type=Path, help="reviewer-local enrolled Ed25519 private key")
    parser.add_argument("--issue", required=True, help="private sovereign46/models-cli review issue URL")
    parser.add_argument("--trust", type=Path, default=approval.TRUST,
                        help="purpose-enrolled Gateway trust file committed to CLI main")
    args = parser.parse_args()
    try:
        submit(args.packet, args.key, args.issue, args.trust)
    except (approval.ApprovalError, approval.CandidateError, OSError, ValueError, UnicodeError,
            subprocess.TimeoutExpired,
            KeyboardInterrupt, EOFError) as error:
        parser.exit(1, f"gateway-direct-mac-submit: {error}\n")


if __name__ == "__main__":
    main()
