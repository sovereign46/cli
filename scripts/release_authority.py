#!/usr/bin/env python3
"""Strict client for the immutable S46 release authority."""

import argparse
import copy
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request


PRODUCTION_API_BASE = "https://models.s46.dev/release-authority/v1"
IMMUTABLE_CACHE_CONTROL = "public, max-age=31536000, immutable, no-transform"
RECORD_SCHEMA = "s46.release-authority.record.v1"
REQUEST_SCHEMA = "s46.release-authority.request.v1"
RESPONSE_SCHEMA = "s46.release-authority.response.v1"
RELEASE_ID_DOMAIN = b"s46/release-authority-id/v1\x00"
RECORD_ETAG_DOMAIN = b"s46/release-authority-record/v1\x00"
STATE_CANDIDATE = "candidate"
STATE_PUBLISHED = "published"
MAX_RECORD_BYTES = 1 << 20
MAX_REQUEST_BYTES = 2 * MAX_RECORD_BYTES
MAX_RESPONSE_BYTES = 16 << 10
MAX_ERROR_BYTES = 16 << 10
MAX_ASSETS = 256
MAX_ANNOTATIONS = 32
MAX_ANNOTATION_BYTES = 64 << 10
MAX_BLOB_BYTES = 64 << 30
MAX_REVISIONS = 256
CHUNK_BYTES = 1 << 20
UINT64_MAX = (1 << 64) - 1

ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
NAME_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]{0,255}$")
ROLE_PATTERN = re.compile(r"^[a-z][a-z0-9._-]{0,63}$")
ANNOTATION_PATTERN = re.compile(r"^[a-z][a-z0-9._-]{0,63}$")
REPOSITORY_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$")
TAG_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+-]{0,255}$")
OBJECT_PATTERN = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
DIGEST_PATTERN = re.compile(r"^[0-9a-f]{64}$")
MEDIA_TYPE_PATTERN = re.compile(r"^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$")
ETAG_PATTERN = re.compile(r'^"sha256:[0-9a-f]{64}"$')
SSH_HOST_PATTERN = re.compile(
    r"^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,63}@)?"
    r"[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$"
)
REMOTE_ERROR_PATTERN = re.compile(
    rb"^s46-release-authority: ([1-5][0-9]{2}) ([A-Za-z][A-Za-z ]*): ([^\r\n]+)$"
)

RECORD_FIELDS = ("schema", "id", "state", "sequence", "previousETag", "source", "assets", "annotations")
SOURCE_FIELDS = ("repository", "tag", "commit", "tagObject", "bundleSha256")
ASSET_FIELDS = ("name", "sha256", "size", "mediaType", "role")
ANNOTATION_FIELDS = ("name", "value")
REQUEST_FIELDS = ("schema", "operation", "ifMatch", "id", "sha256", "size", "record")
RESPONSE_FIELDS = ("schema", "etag", "state", "sequence")


class AuthorityError(Exception):
    """The authority protocol or a local security invariant failed."""

    status = None


class MutationFailed(AuthorityError):
    def __init__(self, status, message):
        super().__init__(message)
        self.status = status


class PreconditionFailed(MutationFailed):
    def __init__(self, message="release authority precondition failed"):
        super().__init__(412, message)


def _exact_fields(value, fields, description):
    if not isinstance(value, dict) or set(value) != set(fields):
        raise AuthorityError(f"{description} fields are not exact")


def _integer(value, description, minimum, maximum):
    if not isinstance(value, int) or isinstance(value, bool) or value < minimum or value > maximum:
        raise AuthorityError(f"{description} is outside the accepted bound")


def _utf8(value, description):
    if not isinstance(value, str):
        raise AuthorityError(f"{description} must be a string")
    try:
        value.encode("utf-8", "strict")
    except UnicodeEncodeError as error:
        raise AuthorityError(f"{description} is not valid UTF-8") from error


def _canonical_json(value):
    try:
        text = json.dumps(value, ensure_ascii=False, allow_nan=False, separators=(",", ":"))
        text.encode("utf-8", "strict")
    except (TypeError, ValueError, UnicodeEncodeError) as error:
        raise AuthorityError(f"cannot encode canonical JSON: {error}") from error
    # Go's encoding/json uses HTML-safe escaping and always escapes the two
    # JavaScript line separators. The protocol's Go implementation is normative.
    text = (text.replace("&", "\\u0026")
                .replace("<", "\\u003c")
                .replace(">", "\\u003e")
                .replace("\u2028", "\\u2028")
                .replace("\u2029", "\\u2029"))
    return text.encode("utf-8") + b"\n"


def _valid_source_tag(value):
    return (isinstance(value, str) and TAG_PATTERN.fullmatch(value) is not None and
            ".." not in value and "@{" not in value and not value.endswith(".") and
            not value.lower().endswith(".lock"))


def release_id(repository, tag):
    """Return the canonical repository/tag-derived release authority ID."""
    _utf8(repository, "source repository")
    _utf8(tag, "source tag")
    if not REPOSITORY_PATTERN.fullmatch(repository) or not _valid_source_tag(tag):
        raise AuthorityError("release source repository or tag is invalid")
    identity = RELEASE_ID_DOMAIN + repository.encode("ascii") + b"\x00" + tag.encode("ascii")
    return "sha256-" + hashlib.sha256(identity).hexdigest()


def _normalize_source(value):
    _exact_fields(value, SOURCE_FIELDS, "release source")
    for field in SOURCE_FIELDS:
        _utf8(value[field], f"source {field}")
    if (not REPOSITORY_PATTERN.fullmatch(value["repository"]) or
            not _valid_source_tag(value["tag"]) or
            not OBJECT_PATTERN.fullmatch(value["commit"]) or
            (value["tagObject"] != "" and not OBJECT_PATTERN.fullmatch(value["tagObject"])) or
            not DIGEST_PATTERN.fullmatch(value["bundleSha256"])):
        raise AuthorityError("release source identity is invalid")
    return {field: value[field] for field in SOURCE_FIELDS}


def _normalize_asset(value):
    _exact_fields(value, ASSET_FIELDS, "release asset")
    for field in ("name", "sha256", "mediaType", "role"):
        _utf8(value[field], f"asset {field}")
    if (not valid_logical_name(value["name"]) or
            not DIGEST_PATTERN.fullmatch(value["sha256"]) or
            not MEDIA_TYPE_PATTERN.fullmatch(value["mediaType"]) or
            not ROLE_PATTERN.fullmatch(value["role"])):
        raise AuthorityError("release asset identity is invalid")
    _integer(value["size"], "release asset size", 1, MAX_BLOB_BYTES)
    return {field: value[field] for field in ASSET_FIELDS}


def _normalize_annotation(value):
    _exact_fields(value, ANNOTATION_FIELDS, "release annotation")
    for field in ANNOTATION_FIELDS:
        _utf8(value[field], f"annotation {field}")
    if not ANNOTATION_PATTERN.fullmatch(value["name"]):
        raise AuthorityError("release annotation name is invalid")
    encoded = value["value"].encode("utf-8")
    if not encoded or len(encoded) > 4096 or b"\x00" in encoded:
        raise AuthorityError("release annotation value is invalid")
    return {field: value[field] for field in ANNOTATION_FIELDS}


def normalize_record(value):
    _exact_fields(value, RECORD_FIELDS, "release record")
    for field in ("schema", "id", "state", "previousETag"):
        _utf8(value[field], f"record {field}")
    if value["schema"] != RECORD_SCHEMA:
        raise AuthorityError("unsupported release record schema")
    if not valid_id(value["id"]):
        raise AuthorityError("invalid release record id")
    if value["state"] not in (STATE_CANDIDATE, STATE_PUBLISHED):
        raise AuthorityError("invalid release record state")
    _integer(value["sequence"], "release record sequence", 1, UINT64_MAX)
    if value["sequence"] == 1:
        if value["previousETag"] != "":
            raise AuthorityError("initial record must have an empty previousETag")
    elif not ETAG_PATTERN.fullmatch(value["previousETag"]):
        raise AuthorityError("noninitial record must have an exact strong previousETag")

    source = _normalize_source(value["source"])
    if value["id"] != release_id(source["repository"], source["tag"]):
        raise AuthorityError("release record id does not match its derived repository/tag identity")
    if value["state"] == STATE_CANDIDATE and value["sequence"] == 1 and source["tagObject"] != "":
        raise AuthorityError("initial candidate must not have an annotated tag object")
    if value["state"] == STATE_PUBLISHED and source["tagObject"] == "":
        raise AuthorityError("published release requires an annotated tag object")
    if not isinstance(value["assets"], list) or not 1 <= len(value["assets"]) <= MAX_ASSETS:
        raise AuthorityError("release record asset count is outside the accepted bound")
    assets = [_normalize_asset(asset) for asset in value["assets"]]
    if any(left["name"] >= right["name"] for left, right in zip(assets, assets[1:])):
        raise AuthorityError("release assets must be strictly sorted by logical name")

    if not isinstance(value["annotations"], list) or len(value["annotations"]) > MAX_ANNOTATIONS:
        raise AuthorityError("release record annotation count is outside the accepted bound")
    annotations = [_normalize_annotation(annotation) for annotation in value["annotations"]]
    if any(left["name"] >= right["name"] for left, right in zip(annotations, annotations[1:])):
        raise AuthorityError("release annotations must be strictly sorted by name")
    if sum(len(item["name"].encode()) + len(item["value"].encode()) for item in annotations) > MAX_ANNOTATION_BYTES:
        raise AuthorityError("release annotation bytes exceed the accepted bound")

    return {
        "schema": value["schema"],
        "id": value["id"],
        "state": value["state"],
        "sequence": value["sequence"],
        "previousETag": value["previousETag"],
        "source": source,
        "assets": assets,
        "annotations": annotations,
    }


def canonical_record(value):
    return _canonical_json(normalize_record(value))


def parse_record(raw):
    if not isinstance(raw, bytes) or not 1 <= len(raw) <= MAX_RECORD_BYTES:
        raise AuthorityError("release record size is outside the accepted bound")
    try:
        value = json.loads(raw.decode("utf-8", "strict"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise AuthorityError(f"invalid release record JSON: {error}") from error
    record = normalize_record(value)
    if canonical_record(record) != raw:
        raise AuthorityError("release record is not canonical JSON")
    return record


def record_etag(value):
    return '"sha256:' + hashlib.sha256(RECORD_ETAG_DOMAIN + canonical_record(value)).hexdigest() + '"'


def validate_append_only(previous, successor):
    previous = normalize_record(previous)
    successor = normalize_record(successor)
    previous_source = dict(previous["source"])
    successor_source = dict(successor["source"])
    previous_tag_object = previous_source.pop("tagObject")
    successor_tag_object = successor_source.pop("tagObject")
    if (previous["id"] != successor["id"] or previous_source != successor_source or
            (previous_tag_object != successor_tag_object and
             (previous_tag_object != "" or not OBJECT_PATTERN.fullmatch(successor_tag_object)))):
        raise AuthorityError("replacement changed immutable release source identity")
    if previous["state"] != STATE_CANDIDATE or successor["state"] != STATE_CANDIDATE:
        raise AuthorityError("only candidate records may be replaced")
    if successor["sequence"] != previous["sequence"] + 1:
        raise AuthorityError("replacement sequence is not the immediate successor")
    if successor["previousETag"] != record_etag(previous):
        raise AuthorityError("replacement previousETag does not bind the current record")
    next_assets = {asset["name"]: asset for asset in successor["assets"]}
    for asset in previous["assets"]:
        if next_assets.get(asset["name"]) != asset:
            raise AuthorityError(f"replacement removed or changed asset {asset['name']!r}")
    next_annotations = {item["name"]: item for item in successor["annotations"]}
    for annotation in previous["annotations"]:
        if next_annotations.get(annotation["name"]) != annotation:
            raise AuthorityError(f"replacement removed or changed annotation {annotation['name']!r}")


def finalized_record(candidate):
    result = copy.deepcopy(normalize_record(candidate))
    if result["state"] != STATE_CANDIDATE or result["sequence"] == UINT64_MAX:
        raise AuthorityError("candidate cannot be finalized")
    result["state"] = STATE_PUBLISHED
    result["sequence"] += 1
    result["previousETag"] = record_etag(candidate)
    return normalize_record(result)


def normalize_request(value):
    _exact_fields(value, REQUEST_FIELDS, "mutation request")
    for field in ("schema", "operation", "ifMatch", "id", "sha256"):
        _utf8(value[field], f"request {field}")
    if value["schema"] != REQUEST_SCHEMA:
        raise AuthorityError("unsupported mutation request schema")
    _integer(value["size"], "mutation request size", 0, MAX_BLOB_BYTES)
    operation = value["operation"]
    record = None if value["record"] is None else normalize_record(value["record"])
    if operation == "putBlob":
        valid = (value["ifMatch"] == "*" and value["id"] == "" and
                 DIGEST_PATTERN.fullmatch(value["sha256"]) and value["size"] > 0 and record is None)
    elif operation == "createCandidate":
        valid = (value["ifMatch"] == "*" and value["id"] == "" and
                 value["sha256"] == "" and value["size"] == 0 and record is not None)
    elif operation == "replaceCandidate":
        valid = (ETAG_PATTERN.fullmatch(value["ifMatch"]) and value["id"] == "" and
                 value["sha256"] == "" and value["size"] == 0 and record is not None)
    elif operation == "finalizeCandidate":
        valid = (ETAG_PATTERN.fullmatch(value["ifMatch"]) and valid_id(value["id"]) and
                 value["sha256"] == "" and value["size"] == 0 and record is None)
    else:
        raise AuthorityError("unsupported mutation operation")
    if not valid:
        raise AuthorityError(f"{operation} request has invalid fields")
    return {
        "schema": value["schema"], "operation": operation, "ifMatch": value["ifMatch"],
        "id": value["id"], "sha256": value["sha256"], "size": value["size"], "record": record,
    }


def canonical_request(value):
    body = _canonical_json(normalize_request(value))
    if len(body) > MAX_REQUEST_BYTES:
        raise AuthorityError("mutation request header exceeds its size bound")
    return body


def parse_request(raw):
    if not isinstance(raw, bytes) or not 1 <= len(raw) <= MAX_REQUEST_BYTES:
        raise AuthorityError("mutation request header size is outside the accepted bound")
    try:
        value = json.loads(raw.decode("utf-8", "strict"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise AuthorityError(f"invalid mutation request JSON: {error}") from error
    request = normalize_request(value)
    if canonical_request(request) != raw:
        raise AuthorityError("mutation request header is not canonical JSON")
    return request


def normalize_response(value):
    _exact_fields(value, RESPONSE_FIELDS, "mutation response")
    for field in ("schema", "etag", "state"):
        _utf8(value[field], f"response {field}")
    if value["schema"] != RESPONSE_SCHEMA or not ETAG_PATTERN.fullmatch(value["etag"]):
        raise AuthorityError("mutation response identity is invalid")
    if value["state"] not in ("", STATE_CANDIDATE, STATE_PUBLISHED):
        raise AuthorityError("mutation response state is invalid")
    _integer(value["sequence"], "mutation response sequence", 0, UINT64_MAX)
    if (value["state"] == "") != (value["sequence"] == 0):
        raise AuthorityError("mutation response state and sequence are inconsistent")
    return {field: value[field] for field in RESPONSE_FIELDS}


def canonical_response(value):
    return _canonical_json(normalize_response(value))


def parse_response(raw):
    if not isinstance(raw, bytes) or not 1 <= len(raw) <= MAX_RESPONSE_BYTES:
        raise AuthorityError("mutation response size is outside the accepted bound")
    try:
        value = json.loads(raw.decode("utf-8", "strict"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise AuthorityError(f"invalid mutation response JSON: {error}") from error
    response = normalize_response(value)
    if canonical_response(response) != raw:
        raise AuthorityError("mutation response is not canonical JSON")
    return response


def valid_id(value):
    return isinstance(value, str) and value not in (".", "..") and ID_PATTERN.fullmatch(value) is not None


def valid_logical_name(value):
    return isinstance(value, str) and value not in (".", "..") and NAME_PATTERN.fullmatch(value) is not None


def _validate_api_base(value):
    if value == PRODUCTION_API_BASE:
        return value
    try:
        parsed = urllib.parse.urlsplit(value)
        port = parsed.port
    except ValueError as error:
        raise AuthorityError("invalid release authority API base") from error
    if (parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "::1") or port is None or
            parsed.username is not None or parsed.password is not None or
            parsed.path != "/release-authority/v1" or parsed.query or parsed.fragment or
            value.endswith("/")):
        raise AuthorityError("API base must be production HTTPS or an explicit loopback test origin")
    return value


def _header_exact(headers, name):
    values = headers.get_all(name) or []
    if len(values) != 1:
        raise AuthorityError(f"HTTP response did not provide exactly one {name} header")
    return values[0]


def _content_length(headers, maximum):
    value = _header_exact(headers, "Content-Length")
    if not re.fullmatch(r"0|[1-9][0-9]*", value):
        raise AuthorityError("HTTP response Content-Length is not canonical")
    length = int(value)
    if length <= 0 or length > maximum:
        raise AuthorityError("HTTP response Content-Length is outside the accepted bound")
    if headers.get("Transfer-Encoding") is not None or headers.get("Content-Encoding") not in (None, "identity"):
        raise AuthorityError("HTTP response uses an unsupported transfer or content encoding")
    return length


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, file_pointer, code, message, headers, new_url):
        return None


class StableUpload:
    """A pinned, no-follow regular file that is checked across both read passes."""

    def __init__(self, path):
        self.path = os.fspath(path)
        self.file = None
        self.initial = None
        self.digest = None
        self.size = None

    def __enter__(self):
        try:
            before = os.lstat(self.path)
        except OSError as error:
            raise AuthorityError(f"cannot inspect blob source: {error}") from error
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        try:
            descriptor = os.open(self.path, flags)
        except OSError as error:
            raise AuthorityError(f"cannot open blob source without following links: {error}") from error
        self.file = os.fdopen(descriptor, "rb", buffering=0)
        opened = os.fstat(descriptor)
        try:
            self._validate_info(before)
            self._validate_info(opened)
        except AuthorityError:
            self.close()
            raise
        if not self._same(before, opened):
            self.close()
            raise AuthorityError("blob source changed while being opened")
        self.initial = opened
        self.size = opened.st_size
        hasher = hashlib.sha256()
        read = 0
        while chunk := self.file.read(CHUNK_BYTES):
            read += len(chunk)
            if read > self.size:
                self.close()
                raise AuthorityError("blob source grew while being hashed")
            hasher.update(chunk)
        if read != self.size:
            self.close()
            raise AuthorityError("blob source changed size while being hashed")
        self.digest = hasher.hexdigest()
        self._verify_unchanged()
        self.file.seek(0)
        return self

    def stream_to(self, destination):
        if self.file is None or self.digest is None:
            raise AuthorityError("blob source is not open")
        self.file.seek(0)
        hasher = hashlib.sha256()
        written = 0
        while written < self.size:
            chunk = self.file.read(min(CHUNK_BYTES, self.size - written))
            if not chunk:
                raise AuthorityError("blob source became shorter while being uploaded")
            _write_all(destination, chunk)
            written += len(chunk)
            hasher.update(chunk)
        if self.file.read(1):
            raise AuthorityError("blob source grew while being uploaded")
        if written != self.size or hasher.hexdigest() != self.digest:
            raise AuthorityError("blob source bytes changed while being uploaded")
        self._verify_unchanged()

    def _verify_unchanged(self):
        opened = os.fstat(self.file.fileno())
        try:
            current = os.lstat(self.path)
        except OSError as error:
            raise AuthorityError("blob source path changed while being read") from error
        self._validate_info(opened)
        self._validate_info(current)
        if not self._same(self.initial, opened) or not self._same(opened, current):
            raise AuthorityError("blob source changed while being read")

    @staticmethod
    def _same(left, right):
        return (left.st_dev, left.st_ino, left.st_size, left.st_mtime_ns, left.st_ctime_ns, left.st_nlink) == (
            right.st_dev, right.st_ino, right.st_size, right.st_mtime_ns, right.st_ctime_ns, right.st_nlink)

    @staticmethod
    def _validate_info(info):
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size <= 0 or info.st_size > MAX_BLOB_BYTES:
            raise AuthorityError("blob source must be a singly linked, bounded, nonempty regular file")

    def close(self):
        if self.file is not None:
            self.file.close()
            self.file = None

    def __exit__(self, *_):
        self.close()
        return False


def _write_all(destination, body):
    view = memoryview(body)
    while view:
        count = destination.write(view)
        if count is None:
            count = len(view)
        if count <= 0:
            raise BrokenPipeError("short write to SSH transport")
        view = view[count:]


def _read_bounded(stream, maximum, result):
    output = bytearray()
    oversized = False
    try:
        while chunk := stream.read(CHUNK_BYTES):
            if len(output) < maximum + 1:
                output.extend(chunk[:maximum + 1 - len(output)])
            if len(output) > maximum or len(chunk) > maximum + 1:
                oversized = True
        result.extend((bytes(output[:maximum + 1]), oversized or len(output) > maximum))
    except BaseException as error:  # Preserve reader failures for the main thread.
        result.extend((b"", error))


def _read_small_regular(path, maximum, description):
    path = os.fspath(path)
    try:
        before = os.lstat(path)
        flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        descriptor = os.open(path, flags)
    except OSError as error:
        raise AuthorityError(f"cannot open {description}: {error}") from error
    try:
        opened = os.fstat(descriptor)
        if (not stat.S_ISREG(before.st_mode) or not stat.S_ISREG(opened.st_mode) or
                before.st_dev != opened.st_dev or before.st_ino != opened.st_ino or
                opened.st_size <= 0 or opened.st_size > maximum):
            raise AuthorityError(f"{description} is not one bounded stable regular file")
        with os.fdopen(descriptor, "rb", closefd=False) as source:
            raw = source.read(maximum + 1)
        after = os.fstat(descriptor)
        current = os.lstat(path)
        identity = lambda item: (item.st_dev, item.st_ino, item.st_size, item.st_mtime_ns, item.st_ctime_ns)
        identities = (identity(before), identity(opened), identity(after), identity(current))
        if len(raw) != opened.st_size or len(raw) > maximum or len(set(identities)) != 1:
            raise AuthorityError(f"{description} changed while being read")
        return raw
    finally:
        os.close(descriptor)


def _validate_ssh_file(path, secret, description):
    if not path or not os.path.isabs(path):
        raise AuthorityError(f"{description} path must be explicitly provided and absolute")
    try:
        info = os.lstat(path)
    except OSError as error:
        raise AuthorityError(f"cannot inspect {description}: {error}") from error
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size <= 0 or info.st_size > (1 << 20) or
            (secret and stat.S_IMODE(info.st_mode) & 0o077) or
            (not secret and stat.S_IMODE(info.st_mode) & 0o022)):
        raise AuthorityError(f"{description} has unsafe type, links, size, or mode")
    if secret and info.st_uid != os.geteuid():
        raise AuthorityError(f"{description} is not owned by the effective user")


class ReleaseAuthorityClient:
    def __init__(self, api_base=PRODUCTION_API_BASE, *, ssh_host=None, ssh_key=None,
                 known_hosts=None, ssh_program="ssh", timeout=3600):
        self.api_base = _validate_api_base(api_base)
        self.ssh_host = ssh_host
        self.ssh_key = os.fspath(ssh_key) if ssh_key is not None else None
        self.known_hosts = os.fspath(known_hosts) if known_hosts is not None else None
        self.ssh_program = os.fspath(ssh_program)
        if not isinstance(timeout, (int, float)) or isinstance(timeout, bool) or timeout <= 0:
            raise AuthorityError("transport timeout must be positive")
        self.timeout = timeout
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect())

    def capture(self, kind, identifier):
        plural, cache = self._record_scope(kind)
        if not valid_id(identifier):
            raise AuthorityError("invalid release authority record id")
        status, headers, body = self._get_bounded(f"/{plural}/{identifier}", MAX_RECORD_BYTES)
        if status != 200:
            raise AuthorityError(f"record capture returned HTTP {status}")
        if _header_exact(headers, "Content-Type") != "application/json":
            raise AuthorityError("record response has an unexpected Content-Type")
        if _header_exact(headers, "Cache-Control") != cache:
            raise AuthorityError("record response has an unexpected cache policy")
        if _header_exact(headers, "X-Content-Type-Options") != "nosniff":
            raise AuthorityError("record response is missing its exact nosniff policy")
        record = parse_record(body)
        expected_state = STATE_CANDIDATE if kind == "candidate" else STATE_PUBLISHED
        if record["id"] != identifier or record["state"] != expected_state:
            raise AuthorityError("record response does not match the requested identity")
        etag = _header_exact(headers, "ETag")
        expected = record_etag(record)
        if not ETAG_PATTERN.fullmatch(etag) or etag != expected:
            raise AuthorityError("record response ETag is not its exact strong domain-separated validator")
        return record, etag, body

    def capture_candidate(self, identifier):
        return self.capture("candidate", identifier)

    def capture_release(self, identifier):
        return self.capture("release", identifier)

    def download_asset(self, kind, identifier, name, output=None):
        record, _, _ = self.capture(kind, identifier)
        if not valid_logical_name(name):
            raise AuthorityError("invalid logical asset name")
        assets = {asset["name"]: asset for asset in record["assets"]}
        if name not in assets:
            raise AuthorityError("record does not contain the requested asset")
        asset = assets[name]
        plural, cache = self._record_scope(kind)
        path = f"/{plural}/{identifier}/assets/{name}"
        if output is None:
            self._stream_asset(path, asset, cache, None)
        else:
            self._download_new(path, asset, cache, output)
        return asset

    def verify_record_assets(self, kind, record):
        record = normalize_record(record)
        for asset in record["assets"]:
            self.download_asset(kind, record["id"], asset["name"])

    def put_blob(self, path):
        with StableUpload(path) as source:
            request = self._request("putBlob", "*", sha256=source.digest, size=source.size)
            response = self._mutate("stage", request, source)
            source._verify_unchanged()
            expected = self._response('"sha256:' + source.digest + '"')
            self._require_response(response, expected, "blob upload")
            asset = {
                "name": source.digest,
                "sha256": source.digest,
                "size": source.size,
                "mediaType": "application/octet-stream",
                "role": "blob",
            }
            self._stream_asset(f"/blobs/sha256/{source.digest}", asset,
                               IMMUTABLE_CACHE_CONTROL, None, logical=False)
            return response

    def create_candidate(self, record):
        record = normalize_record(record)
        if record["state"] != STATE_CANDIDATE or record["sequence"] != 1 or record["previousETag"] != "":
            raise AuthorityError("candidate creation requires the initial candidate record")
        request = self._request("createCandidate", "*", record=record)
        expected = self._response(record_etag(record), STATE_CANDIDATE, 1)
        try:
            response = self._mutate("stage", request)
            self._require_response(response, expected, "candidate creation")
        except MutationFailed as error:
            if error.status != 409 or not self._record_is_current("candidate", record, expected["etag"]):
                raise
            response = expected
        self._verify_record("candidate", record, expected["etag"], verify_assets=True)
        return response

    def append_candidate(self, record, if_match):
        record = normalize_record(record)
        if not ETAG_PATTERN.fullmatch(if_match):
            raise AuthorityError("candidate replacement requires one exact strong ETag")
        current, current_etag, _ = self.capture_candidate(record["id"])
        expected_etag = record_etag(record)
        expected = self._response(expected_etag, STATE_CANDIDATE, record["sequence"])
        if current_etag != if_match:
            if current_etag == expected_etag and current == record:
                self.verify_record_assets("candidate", record)
                return expected
            raise PreconditionFailed("candidate changed before replacement")
        if current["sequence"] >= MAX_REVISIONS - 1:
            raise MutationFailed(409, "release authority revision limit reached")
        validate_append_only(current, record)
        request = self._request("replaceCandidate", if_match, record=record)
        try:
            response = self._mutate("stage", request)
            self._require_response(response, expected, "candidate replacement")
        except PreconditionFailed:
            if not self._record_is_current("candidate", record, expected_etag):
                raise
            response = expected
        self._verify_record("candidate", record, expected_etag, verify_assets=True)
        return response

    replace_candidate = append_candidate

    def empirical_stale_replace(self, request):
        request = normalize_request(request)
        if request["operation"] != "replaceCandidate":
            raise AuthorityError("empirical stale probe requires a candidate replacement request")
        try:
            self._mutate("stage", request)
        except PreconditionFailed:
            return 412
        raise AuthorityError("empirical stale candidate replacement unexpectedly succeeded")

    def finalize_candidate(self, identifier, if_match):
        if not valid_id(identifier) or not ETAG_PATTERN.fullmatch(if_match):
            raise AuthorityError("candidate finalization requires a valid id and exact strong ETag")
        try:
            candidate, current_etag, _ = self.capture_candidate(identifier)
        except MutationFailed as error:
            if error.status != 404:
                raise
            return self._adopt_finalized_release(identifier, if_match)
        published = finalized_record(candidate)
        final_etag = record_etag(published)
        expected = self._response(final_etag, STATE_PUBLISHED, published["sequence"])
        if current_etag != if_match:
            raise PreconditionFailed("candidate changed before finalization")
        if candidate["sequence"] >= MAX_REVISIONS:
            raise MutationFailed(409, "release authority revision limit reached")
        request = self._request("finalizeCandidate", if_match, identifier=identifier)
        try:
            response = self._mutate("publish", request)
            self._require_response(response, expected, "candidate finalization")
        except MutationFailed as error:
            if error.status != 409 or not self._record_is_current("release", published, final_etag):
                raise
            response = expected
        self._verify_record("release", published, final_etag, verify_assets=True)
        return response

    def _adopt_finalized_release(self, identifier, candidate_etag):
        published, published_etag, _ = self.capture_release(identifier)
        if published["previousETag"] != candidate_etag:
            raise PreconditionFailed("published release does not finalize the requested candidate")
        if not 2 <= published["sequence"] <= MAX_REVISIONS:
            raise AuthorityError("published release is outside the candidate finalization sequence profile")
        self._verify_record("release", published, published_etag, verify_assets=True)
        return self._response(published_etag, STATE_PUBLISHED, published["sequence"])

    def _record_is_current(self, kind, expected, etag):
        try:
            record, current_etag, body = self.capture(kind, expected["id"])
        except AuthorityError:
            return False
        return current_etag == etag and record == expected and body == canonical_record(expected)

    def _verify_record(self, kind, expected, etag, verify_assets):
        record, current_etag, body = self.capture(kind, expected["id"])
        if record != expected or current_etag != etag or body != canonical_record(expected):
            raise AuthorityError("public authority record does not match the exact mutation result")
        if verify_assets:
            self.verify_record_assets(kind, record)

    @staticmethod
    def _request(operation, if_match, *, identifier="", sha256="", size=0, record=None):
        return normalize_request({
            "schema": REQUEST_SCHEMA, "operation": operation, "ifMatch": if_match,
            "id": identifier, "sha256": sha256, "size": size, "record": record,
        })

    @staticmethod
    def _response(etag, state="", sequence=0):
        return normalize_response({"schema": RESPONSE_SCHEMA, "etag": etag, "state": state, "sequence": sequence})

    @staticmethod
    def _require_response(actual, expected, operation):
        if actual != expected:
            raise AuthorityError(f"{operation} returned a response that does not bind the exact result")

    def _record_scope(self, kind):
        if kind == "candidate":
            return "candidates", "no-store"
        if kind == "release":
            return "releases", IMMUTABLE_CACHE_CONTROL
        raise AuthorityError("record kind must be candidate or release")

    def _get_bounded(self, path, maximum):
        response = self._open(path)
        with response:
            if response.status != 200:
                raise AuthorityError(f"release authority returned HTTP {response.status}")
            length = _content_length(response.headers, maximum)
            body = response.read(length + 1)
            if len(body) != length or response.read(1):
                raise AuthorityError("HTTP response body does not have its exact declared length")
            return response.status, response.headers, body

    def _open(self, path):
        if not path.startswith("/") or "?" in path or "#" in path or "%" in path or "//" in path:
            raise AuthorityError("unsafe release authority URL path")
        request = urllib.request.Request(
            self.api_base + path,
            headers={"Accept-Encoding": "identity", "User-Agent": "s46-release-authority-client/1"},
            method="GET",
        )
        try:
            return self.opener.open(request, timeout=min(self.timeout, 60))
        except urllib.error.HTTPError as error:
            body = error.read(512)
            status = error.code
            error.close()
            raise MutationFailed(status, f"release authority returned HTTP {status}: {body!r}") from error
        except (OSError, urllib.error.URLError) as error:
            raise AuthorityError(f"release authority request failed: {error}") from error

    def _stream_asset(self, path, asset, cache, output, logical=True):
        response = self._open(path)
        with response:
            if response.status != 200:
                raise AuthorityError(f"asset download returned HTTP {response.status}")
            if _header_exact(response.headers, "ETag") != '"sha256:' + asset["sha256"] + '"':
                raise AuthorityError("asset response has the wrong exact strong ETag")
            expected_type = asset["mediaType"] if logical else "application/octet-stream"
            if _header_exact(response.headers, "Content-Type") != expected_type:
                raise AuthorityError("asset response has the wrong Content-Type")
            if _header_exact(response.headers, "Cache-Control") != cache:
                raise AuthorityError("asset response has the wrong cache policy")
            if _header_exact(response.headers, "X-Content-Type-Options") != "nosniff":
                raise AuthorityError("asset response is missing its exact nosniff policy")
            if _content_length(response.headers, MAX_BLOB_BYTES) != asset["size"]:
                raise AuthorityError("asset response has the wrong size")
            digest = hashlib.sha256()
            remaining = asset["size"]
            while remaining:
                chunk = response.read(min(CHUNK_BYTES, remaining))
                if not chunk:
                    raise AuthorityError("asset response is shorter than its record")
                remaining -= len(chunk)
                digest.update(chunk)
                if output is not None:
                    _write_all(output, chunk)
            if response.read(1):
                raise AuthorityError("asset response contains trailing bytes")
            if digest.hexdigest() != asset["sha256"]:
                raise AuthorityError("asset response bytes do not match their SHA-256 identity")

    def _download_new(self, path, asset, cache, output_path):
        output_path = os.fspath(output_path)
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        try:
            descriptor = os.open(output_path, flags, 0o644)
        except OSError as error:
            raise AuthorityError(f"cannot create download output without replacement: {error}") from error
        complete = False
        try:
            with os.fdopen(descriptor, "wb") as output:
                self._stream_asset(path, asset, cache, output)
                output.flush()
                os.fsync(output.fileno())
            complete = True
        finally:
            if not complete:
                try:
                    os.unlink(output_path)
                except OSError:
                    pass

    def _mutate(self, role, request, source=None):
        self._validate_transport(role)
        request = normalize_request(request)
        operation = request["operation"]
        if role == "stage" and operation == "finalizeCandidate":
            raise AuthorityError("stage role cannot finalize a candidate")
        if role == "publish" and operation != "finalizeCandidate":
            raise AuthorityError("publish role permits only candidate finalization")
        header = canonical_request(request)
        command = self._ssh_command(role)
        environment = {"PATH": "/usr/bin:/bin", "LC_ALL": "C"}
        try:
            process = subprocess.Popen(
                command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                env=environment, bufsize=0,
            )
        except OSError as error:
            raise AuthorityError(f"cannot start SSH mutation transport: {error}") from error

        stdout_result = []
        stderr_result = []
        writer_result = []
        stdout_thread = threading.Thread(
            target=_read_bounded, args=(process.stdout, MAX_RESPONSE_BYTES, stdout_result), daemon=True,
        )
        stderr_thread = threading.Thread(
            target=_read_bounded, args=(process.stderr, MAX_ERROR_BYTES, stderr_result), daemon=True,
        )

        def write_request():
            try:
                _write_all(process.stdin, header)
                if source is not None:
                    source.stream_to(process.stdin)
                process.stdin.close()
                writer_result.append(None)
            except BaseException as error:
                writer_result.append(error)
                try:
                    process.stdin.close()
                except OSError:
                    pass

        writer_thread = threading.Thread(target=write_request, daemon=True)
        stdout_thread.start()
        stderr_thread.start()
        writer_thread.start()
        timed_out = False
        try:
            return_code = process.wait(timeout=self.timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            process.kill()
            return_code = process.wait()
        writer_thread.join()
        stdout_thread.join()
        stderr_thread.join()
        process.stdout.close()
        process.stderr.close()
        if timed_out:
            raise AuthorityError("SSH mutation transport timed out")
        stdout, stdout_problem = stdout_result
        stderr, stderr_problem = stderr_result
        if isinstance(stdout_problem, BaseException) or isinstance(stderr_problem, BaseException):
            raise AuthorityError("cannot read SSH mutation response")
        remote_error = self._remote_error(stderr)
        if return_code != 0:
            if remote_error is not None:
                status, message = remote_error
                if status == 412:
                    raise PreconditionFailed(message)
                raise MutationFailed(status, message)
            raise AuthorityError(f"SSH mutation transport failed with exit {return_code}: {stderr[:512]!r}")
        if writer_result and writer_result[0] is not None:
            raise AuthorityError(f"cannot stream mutation request: {writer_result[0]}") from writer_result[0]
        if stdout_problem or stderr_problem or stderr:
            raise AuthorityError("SSH mutation transport returned oversized output or unexpected stderr")
        return parse_response(stdout)

    @staticmethod
    def _remote_error(stderr):
        if not stderr.endswith(b"\n") or stderr.count(b"\n") != 1:
            return None
        match = REMOTE_ERROR_PATTERN.fullmatch(stderr[:-1])
        if match is None:
            return None
        return int(match.group(1)), match.group(3).decode("utf-8", "replace")

    def _validate_transport(self, role):
        if role not in ("stage", "publish"):
            raise AuthorityError("mutation role must be stage or publish")
        if not isinstance(self.ssh_host, str) or not SSH_HOST_PATTERN.fullmatch(self.ssh_host):
            raise AuthorityError("an explicit canonical SSH host is required")
        _validate_ssh_file(self.ssh_key, True, "SSH private key")
        _validate_ssh_file(self.known_hosts, False, "SSH known_hosts")

    def _ssh_command(self, role):
        return [
            self.ssh_program,
            "-F", "/dev/null",
            "-T",
            "-oBatchMode=yes",
            "-oIdentitiesOnly=yes",
            "-oIdentityAgent=none",
            "-oCertificateFile=none",
            "-oPubkeyAuthentication=yes",
            "-oPasswordAuthentication=no",
            "-oKbdInteractiveAuthentication=no",
            "-oGSSAPIAuthentication=no",
            "-oHostbasedAuthentication=no",
            "-oStrictHostKeyChecking=yes",
            f"-oUserKnownHostsFile={self.known_hosts}",
            "-oGlobalKnownHostsFile=/dev/null",
            "-oCheckHostIP=no",
            "-oVerifyHostKeyDNS=no",
            "-oUpdateHostKeys=no",
            "-oControlMaster=no",
            "-oControlPath=none",
            "-oClearAllForwardings=yes",
            "-oPermitLocalCommand=no",
            "-oProxyCommand=none",
            "-oProxyJump=none",
            "-oRequestTTY=no",
            "-i", self.ssh_key,
            "--", self.ssh_host,
            f"s46-release-authority {role}",
        ]


def _load_record(path):
    return parse_record(_read_small_regular(path, MAX_RECORD_BYTES, "release record"))


def _load_etag(path):
    raw = _read_small_regular(path, 256, "ETag file")
    try:
        value = raw.decode("ascii")
    except UnicodeDecodeError as error:
        raise AuthorityError("ETag file is not ASCII") from error
    if not value.endswith("\n") or value.count("\n") != 1 or not ETAG_PATTERN.fullmatch(value[:-1]):
        raise AuthorityError("ETag file must contain one exact strong ETag and newline")
    return value[:-1]


def _write_new(path, body, mode=0o644):
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(os.fspath(path), flags, mode)
    except OSError as error:
        raise AuthorityError(f"cannot create output without replacement: {error}") from error
    complete = False
    try:
        with os.fdopen(descriptor, "wb") as output:
            _write_all(output, body)
            output.flush()
            os.fsync(output.fileno())
        complete = True
    finally:
        if not complete:
            try:
                os.unlink(path)
            except OSError:
                pass


def _emit_response(response, output):
    body = canonical_response(response)
    if output:
        _write_new(output, body)
    else:
        sys.stdout.buffer.write(body)
        sys.stdout.buffer.flush()


def _client(args, mutation=False):
    options = {"api_base": args.api_base, "timeout": args.timeout}
    if mutation:
        options.update(ssh_host=args.ssh_host, ssh_key=args.ssh_key, known_hosts=args.known_hosts)
    return ReleaseAuthorityClient(**options)


def _if_match(args):
    return args.if_match if args.if_match is not None else _load_etag(args.etag_file)


def command_capture(args):
    _, etag, body = _client(args).capture(args.kind, args.id)
    _write_new(args.record_output, body)
    _write_new(args.etag_output, (etag + "\n").encode("ascii"))


def command_download(args):
    _client(args).download_asset(args.kind, args.id, args.asset, args.output)


def command_put_blob(args):
    _emit_response(_client(args, True).put_blob(args.file), args.response_output)


def command_create(args):
    _emit_response(_client(args, True).create_candidate(_load_record(args.record)), args.response_output)


def command_append(args):
    response = _client(args, True).append_candidate(_load_record(args.record), _if_match(args))
    _emit_response(response, args.response_output)


def command_finalize(args):
    response = _client(args, True).finalize_candidate(args.id, _if_match(args))
    _emit_response(response, args.response_output)


def command_raw(args):
    request = parse_request(_read_small_regular(args.request, MAX_REQUEST_BYTES, "mutation request"))
    print(_client(args, True).empirical_stale_replace(request))


def _add_public_arguments(parser):
    parser.add_argument("--api-base", default=PRODUCTION_API_BASE, help=argparse.SUPPRESS)
    parser.add_argument("--timeout", type=float, default=3600, help=argparse.SUPPRESS)


def _add_mutation_arguments(parser):
    _add_public_arguments(parser)
    parser.add_argument("--ssh-host", required=True)
    parser.add_argument("--ssh-key", required=True)
    parser.add_argument("--known-hosts", required=True)
    parser.add_argument("--response-output")


def _add_if_match(parser):
    choice = parser.add_mutually_exclusive_group(required=True)
    choice.add_argument("--if-match")
    choice.add_argument("--etag-file")


def argument_parser():
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)

    capture = commands.add_parser("capture", help="capture one exact public candidate or release record")
    _add_public_arguments(capture)
    capture.add_argument("--kind", choices=("candidate", "release"), required=True)
    capture.add_argument("--id", required=True)
    capture.add_argument("--record-output", required=True)
    capture.add_argument("--etag-output", required=True)
    capture.set_defaults(function=command_capture)

    download = commands.add_parser("download", help="download and verify one logical record asset")
    _add_public_arguments(download)
    download.add_argument("--kind", choices=("candidate", "release"), required=True)
    download.add_argument("--id", required=True)
    download.add_argument("--asset", required=True)
    download.add_argument("--output", required=True)
    download.set_defaults(function=command_download)

    put_blob = commands.add_parser("put-blob", help="stream one stable file into content-addressed storage")
    _add_mutation_arguments(put_blob)
    put_blob.add_argument("--file", required=True)
    put_blob.set_defaults(function=command_put_blob)

    create = commands.add_parser("create", help="create an initial candidate record")
    _add_mutation_arguments(create)
    create.add_argument("--record", required=True)
    create.set_defaults(function=command_create)

    append = commands.add_parser("append", aliases=("replace",), help="append one exact candidate revision")
    _add_mutation_arguments(append)
    append.add_argument("--record", required=True)
    _add_if_match(append)
    append.set_defaults(function=command_append)

    finalize = commands.add_parser("finalize", help="publish a candidate with exact ETag CAS")
    _add_mutation_arguments(finalize)
    finalize.add_argument("--id", required=True)
    _add_if_match(finalize)
    finalize.set_defaults(function=command_finalize)

    raw = commands.add_parser("raw", help=argparse.SUPPRESS)
    _add_mutation_arguments(raw)
    raw.add_argument("--request", required=True)
    raw.set_defaults(function=command_raw)
    return result


def main(argv=None):
    args = argument_parser().parse_args(argv)
    try:
        args.function(args)
    except PreconditionFailed as error:
        print(f"error: 412 Precondition Failed: {error}", file=sys.stderr)
        return 12
    except MutationFailed as error:
        print(f"error: authority status {error.status}: {error}", file=sys.stderr)
        return 1
    except AuthorityError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
