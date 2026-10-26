import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import unittest

from scripts.release_authority import (
    AuthorityError,
    IMMUTABLE_CACHE_CONTROL,
    PreconditionFailed,
    RECORD_SCHEMA,
    REQUEST_SCHEMA,
    RESPONSE_SCHEMA,
    ReleaseAuthorityClient,
    canonical_record,
    canonical_request,
    canonical_response,
    finalized_record,
    normalize_record,
    parse_record,
    parse_request,
    parse_response,
    record_etag,
    release_id,
    validate_append_only,
)


class AuthorityState:
    def __init__(self):
        self.lock = threading.Lock()
        self.blobs = {}
        self.candidates = {}
        self.releases = {}
        self.commands = []
        self.record_etag_override = None
        self.record_nosniff = ["nosniff"]
        self.asset_etag_override = None
        self.record_trailing = b""
        self.asset_trailing = b""
        self.response_trailing = b""
        self.stale_operation = None
        self.mutate_file = None

    def execute(self, role, body):
        line_end = body.find(b"\n")
        if line_end < 0:
            return 500, b"missing header"
        request = parse_request(body[:line_end + 1])
        trailing = body[line_end + 1:]
        operation = request["operation"]
        self.commands.append((role, operation))
        if role == "stage" and operation == "finalizeCandidate":
            return 403, b"stage role cannot finalize"
        if role == "publish" and operation != "finalizeCandidate":
            return 403, b"publish role permits only candidate finalization"
        if self.stale_operation == operation:
            self.stale_operation = None
            return 412, b"release authority precondition failed"
        if operation == "putBlob":
            if len(trailing) != request["size"] or hashlib.sha256(trailing).hexdigest() != request["sha256"]:
                return 500, b"blob identity mismatch"
            self.blobs.setdefault(request["sha256"], trailing)
            response = response_value('"sha256:' + request["sha256"] + '"')
        else:
            if trailing:
                return 500, b"trailing protocol bytes"
            if operation == "createCandidate":
                record = request["record"]
                if record["id"] in self.candidates or record["id"] in self.releases:
                    return 409, b"already exists"
                self.candidates[record["id"]] = record
                response = response_value(record_etag(record), "candidate", record["sequence"])
            elif operation == "replaceCandidate":
                record = request["record"]
                current = self.candidates.get(record["id"])
                if current is None:
                    return 404, b"not found"
                if record_etag(current) != request["ifMatch"]:
                    return 412, b"release authority precondition failed"
                try:
                    validate_append_only(current, record)
                except AuthorityError as error:
                    return 500, str(error).encode()
                self.candidates[record["id"]] = record
                response = response_value(record_etag(record), "candidate", record["sequence"])
            elif operation == "finalizeCandidate":
                current = self.candidates.get(request["id"])
                if current is None:
                    return 404, b"not found"
                if record_etag(current) != request["ifMatch"]:
                    return 412, b"release authority precondition failed"
                published = finalized_record(current)
                del self.candidates[request["id"]]
                self.releases[request["id"]] = published
                response = response_value(
                    record_etag(published), "published", published["sequence"],
                )
            else:
                return 500, b"unsupported operation"
        if self.mutate_file is not None:
            path = self.mutate_file
            self.mutate_file = None
            original = path.read_bytes()
            path.write_bytes(bytes([original[0] ^ 1]) + original[1:])
        return 200, canonical_response(response) + self.response_trailing


def response_value(etag, state="", sequence=0):
    return {"schema": RESPONSE_SCHEMA, "etag": etag, "state": state, "sequence": sequence}


def asset_value(name, body, role="build"):
    return {
        "name": name,
        "sha256": hashlib.sha256(body).hexdigest(),
        "size": len(body),
        "mediaType": "application/octet-stream",
        "role": role,
    }


def record_value(tag, assets, sequence=1, previous="", annotations=None):
    repository = "sovereign46/api"
    return {
        "schema": RECORD_SCHEMA,
        "id": release_id(repository, tag),
        "state": "candidate",
        "sequence": sequence,
        "previousETag": previous,
        "source": {
            "repository": repository,
            "tag": tag,
            "commit": "a" * 40,
            "tagObject": "" if sequence == 1 else "b" * 40,
            "bundleSha256": "c" * 64,
        },
        "assets": sorted(assets, key=lambda item: item["name"]),
        "annotations": annotations or [],
    }


class PublicHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        prefix = "/release-authority/v1/"
        if not self.path.startswith(prefix) or "?" in self.path:
            return self.send_error(404)
        parts = self.path[len(prefix):].split("/")
        state = self.server.state
        with state.lock:
            if len(parts) == 2 and parts[0] in ("candidates", "releases"):
                records = state.candidates if parts[0] == "candidates" else state.releases
                record = records.get(parts[1])
                if record is None:
                    return self.send_error(404)
                body = canonical_record(record) + state.record_trailing
                etag = state.record_etag_override or record_etag(record)
                cache = "no-store" if parts[0] == "candidates" else IMMUTABLE_CACHE_CONTROL
                return self.reply(body, etag, "application/json", cache, state.record_nosniff)
            if len(parts) == 4 and parts[0] in ("candidates", "releases") and parts[2] == "assets":
                records = state.candidates if parts[0] == "candidates" else state.releases
                record = records.get(parts[1])
                if record is None:
                    return self.send_error(404)
                assets = {asset["name"]: asset for asset in record["assets"]}
                asset = assets.get(parts[3])
                if asset is None:
                    return self.send_error(404)
                body = state.blobs[asset["sha256"]] + state.asset_trailing
                etag = state.asset_etag_override or '"sha256:' + asset["sha256"] + '"'
                cache = "no-store" if parts[0] == "candidates" else IMMUTABLE_CACHE_CONTROL
                return self.reply(body, etag, asset["mediaType"], cache)
            if len(parts) == 3 and parts[:2] == ["blobs", "sha256"]:
                body = state.blobs.get(parts[2])
                if body is None:
                    return self.send_error(404)
                body += state.asset_trailing
                etag = state.asset_etag_override or '"sha256:' + parts[2] + '"'
                return self.reply(body, etag, "application/octet-stream", IMMUTABLE_CACHE_CONTROL)
        self.send_error(404)

    def do_POST(self):
        if self.path not in ("/stage", "/publish"):
            return self.send_error(404)
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        with self.server.state.lock:
            status, output = self.server.state.execute(self.path[1:], body)
        self.send_response(status)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(output)))
        self.end_headers()
        self.wfile.write(output)

    def reply(self, body, etag, content_type, cache, nosniff=("nosniff",)):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("ETag", etag)
        self.send_header("Cache-Control", cache)
        for value in nosniff:
            self.send_header("X-Content-Type-Options", value)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


SSH_STUB = r'''#!{python}
import json
from pathlib import Path
import sys
import urllib.error
import urllib.request

args = sys.argv[1:]
Path({arguments!r}).write_text(json.dumps(args))
command = args[-1]
if command not in ("s46-release-authority stage", "s46-release-authority publish"):
    raise SystemExit(97)
role = command.rsplit(" ", 1)[-1]
body = sys.stdin.buffer.read()
request = urllib.request.Request({mutation_url!r} + "/" + role, data=body, method="POST")
try:
    with urllib.request.urlopen(request, timeout=10) as response:
        sys.stdout.buffer.write(response.read())
except urllib.error.HTTPError as error:
    message = error.read().decode("utf-8", "replace")
    reason = {{403: "Forbidden", 404: "Not Found", 409: "Conflict", 412: "Precondition Failed"}}.get(error.code, "Internal Server Error")
    sys.stderr.write(f"s46-release-authority: {{error.code}} {{reason}}: {{message}}\n")
    raise SystemExit(1)
'''


class ReleaseAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.state = AuthorityState()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), PublicHandler)
        self.server.state = self.state
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.shutdown)
        self.addCleanup(self.server.server_close)
        origin = f"http://127.0.0.1:{self.server.server_port}"
        self.api_base = origin + "/release-authority/v1"

        self.arguments = self.root / "ssh-arguments.json"
        self.ssh = self.root / "ssh"
        self.ssh.write_text(SSH_STUB.format(
            python=os.sys.executable,
            arguments=str(self.arguments),
            mutation_url=origin,
        ))
        self.ssh.chmod(0o755)
        self.key = self.root / "stage-key"
        self.key.write_text("test key\n")
        self.key.chmod(0o600)
        self.known_hosts = self.root / "known_hosts"
        self.known_hosts.write_text("authority.test ssh-ed25519 AAAAtest\n")
        self.known_hosts.chmod(0o644)
        self.client = ReleaseAuthorityClient(
            self.api_base,
            ssh_host="publisher@authority.test",
            ssh_key=self.key,
            known_hosts=self.known_hosts,
            ssh_program=self.ssh,
            timeout=10,
        )

    def write_blob(self, name, body):
        path = self.root / name
        path.write_bytes(body)
        return path, asset_value(name, body)

    def test_full_create_append_finalize_and_download_lifecycle(self):
        first_path, first = self.write_blob("archive.tar.gz", b"exact archive bytes")
        second_path, second = self.write_blob("evidence.json", b"exact evidence bytes\n")
        put = self.client.put_blob(first_path)
        self.assertEqual(put["etag"], '"sha256:' + first["sha256"] + '"')
        self.client.put_blob(second_path)

        initial = record_value("gateway-v1.2.3", [first])
        created = self.client.create_candidate(initial)
        first_etag = record_etag(initial)
        self.assertEqual(created, response_value(first_etag, "candidate", 1))

        successor = record_value(
            initial["source"]["tag"], [first, second], sequence=2, previous=first_etag,
            annotations=[{"name": "review", "value": "Fred & Gonçalo <approved>"}],
        )
        appended = self.client.append_candidate(successor, first_etag)
        second_etag = record_etag(successor)
        self.assertEqual(appended, response_value(second_etag, "candidate", 2))

        published = self.client.finalize_candidate(successor["id"], second_etag)
        final = finalized_record(successor)
        self.assertEqual(published, response_value(record_etag(final), "published", 3))
        captured, captured_etag, raw = self.client.capture_release(successor["id"])
        self.assertEqual(captured, final)
        self.assertEqual(captured_etag, record_etag(final))
        self.assertEqual(raw, canonical_record(final))

        destination = self.root / "downloaded-evidence.json"
        self.client.download_asset("release", successor["id"], second["name"], destination)
        self.assertEqual(destination.read_bytes(), second_path.read_bytes())
        arguments = json.loads(self.arguments.read_text())
        self.assertEqual(arguments[-1], "s46-release-authority publish")
        self.assertIn("-oStrictHostKeyChecking=yes", arguments)
        self.assertIn("-oIdentityAgent=none", arguments)
        self.assertIn(str(self.key), arguments)
        self.assertIn("publisher@authority.test", arguments)
        self.assertEqual(self.state.commands, [
            ("stage", "putBlob"),
            ("stage", "putBlob"),
            ("stage", "createCandidate"),
            ("stage", "replaceCandidate"),
            ("publish", "finalizeCandidate"),
        ])

    def test_exact_retries_recover_each_completed_mutation(self):
        first_path, first = self.write_blob("retry-one.bin", b"one")
        second_path, second = self.write_blob("retry-two.bin", b"two")
        first_put = self.client.put_blob(first_path)
        self.assertEqual(self.client.put_blob(first_path), first_put)
        self.client.put_blob(second_path)

        initial = record_value("retry", [first])
        created = self.client.create_candidate(initial)
        self.assertEqual(self.client.create_candidate(initial), created)
        first_etag = record_etag(initial)

        successor = record_value("retry", [first, second], sequence=2, previous=first_etag)
        appended = self.client.append_candidate(successor, first_etag)
        self.assertEqual(self.client.append_candidate(successor, first_etag), appended)
        second_etag = record_etag(successor)

        published = self.client.finalize_candidate(initial["id"], second_etag)
        self.assertNotIn(initial["id"], self.state.candidates)
        self.assertEqual(self.client.finalize_candidate(initial["id"], second_etag), published)
        self.assertEqual(
            self.state.commands.count(("publish", "finalizeCandidate")), 1,
        )

    def test_finalize_retry_adopts_exact_sequence_five_release(self):
        path, asset = self.write_blob("sequence-five.bin", b"sequence five")
        self.state.blobs[asset["sha256"]] = path.read_bytes()
        candidate = record_value(
            "sequence-five", [asset], sequence=4, previous='"sha256:' + "d" * 64 + '"',
        )
        published = finalized_record(candidate)
        self.state.releases[candidate["id"]] = published

        response = self.client.finalize_candidate(candidate["id"], record_etag(candidate))

        self.assertEqual(
            response, response_value(record_etag(published), "published", 5),
        )
        self.assertEqual(self.state.commands, [])

    def test_finalize_retry_rejects_unrelated_sequence_five_release(self):
        asset = asset_value("unrelated.bin", b"unrelated")
        expected_candidate = record_value(
            "mismatched-release", [asset], sequence=4,
            previous='"sha256:' + "d" * 64 + '"',
            annotations=[{"name": "profile", "value": "expected"}],
        )
        unrelated_candidate = copy.deepcopy(expected_candidate)
        unrelated_candidate["annotations"][0]["value"] = "unrelated"
        self.state.releases[expected_candidate["id"]] = finalized_record(unrelated_candidate)

        with self.assertRaisesRegex(PreconditionFailed, "does not finalize"):
            self.client.finalize_candidate(
                expected_candidate["id"], record_etag(expected_candidate),
            )
        self.assertEqual(self.state.commands, [])

    def test_release_id_shared_vector_and_record_binding(self):
        expected = "sha256-bc59a6d476bec9e478897571c18d04ee88e050da6d60958563f101f8a878af68"
        self.assertEqual(release_id("sovereign46/api", "gateway-v1.2.3"), expected)

        record = record_value("gateway-v1.2.3", [asset_value("asset", b"asset")])
        self.assertEqual(record["id"], expected)
        record["id"] = "sha256-" + "0" * 64
        with self.assertRaisesRegex(AuthorityError, "derived repository/tag identity"):
            normalize_record(record)
        with self.assertRaises(AuthorityError):
            release_id("sovereign46/api", "refs/tags/gateway-v1.2.3")
        with self.assertRaises(AuthorityError):
            release_id("sovereign46/ápí", "gateway-v1.2.3")
        with self.assertRaises(AuthorityError):
            release_id("Sovereign46/api", "gateway-v1.2.3")

    def test_record_and_protocol_parsing_are_byte_canonical(self):
        _, asset = self.write_blob("a.bin", b"a")
        record = record_value("canonical", [asset], annotations=[{"name": "note", "value": "<&>\u2028"}])
        body = canonical_record(record)
        self.assertEqual(parse_record(body), normalize_record(record))
        self.assertIn(b"\\u003c\\u0026\\u003e\\u2028", body)
        with self.assertRaises(AuthorityError):
            parse_record(json.dumps(record, indent=2, ensure_ascii=False).encode() + b"\n")
        with self.assertRaises(AuthorityError):
            parse_record(body + b"x")

        request = {
            "schema": REQUEST_SCHEMA, "operation": "createCandidate", "ifMatch": "*",
            "id": "", "sha256": "", "size": 0, "record": record,
        }
        request_body = canonical_request(request)
        self.assertEqual(parse_request(request_body), parse_request(canonical_request(parse_request(request_body))))
        noncanonical = json.dumps(request, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode() + b"\n"
        self.assertNotEqual(noncanonical, request_body)
        with self.assertRaises(AuthorityError):
            parse_request(noncanonical)

        response = response_value(record_etag(record), "candidate", 1)
        self.assertEqual(parse_response(canonical_response(response)), response)
        with self.assertRaises(AuthorityError):
            parse_response(canonical_response(response) + b"x")

    def test_rejects_weak_malformed_or_non_domain_record_etags(self):
        path, asset = self.write_blob("asset.bin", b"asset")
        self.state.blobs[asset["sha256"]] = path.read_bytes()
        record = record_value("etag", [asset])
        self.state.candidates[record["id"]] = record
        for etag in ('W/"sha256:' + "0" * 64 + '"', '"other"', '"sha256:' + "0" * 64 + '"'):
            with self.subTest(etag=etag):
                self.state.record_etag_override = etag
                with self.assertRaises(AuthorityError):
                    self.client.capture_candidate(record["id"])
        self.state.record_etag_override = None
        captured, etag, _ = self.client.capture_candidate(record["id"])
        self.assertEqual(captured, record)
        self.assertEqual(etag, record_etag(record))

    def test_record_capture_requires_one_exact_nosniff_header(self):
        _, asset = self.write_blob("record-policy.bin", b"record policy")
        record = record_value("record-policy", [asset])
        self.state.candidates[record["id"]] = record

        for values in ([], ["nosniff", "nosniff"], ["sniff"]):
            with self.subTest(values=values):
                self.state.record_nosniff = values
                with self.assertRaises(AuthorityError):
                    self.client.capture_candidate(record["id"])

        self.state.record_nosniff = ["nosniff"]
        captured, _, _ = self.client.capture_candidate(record["id"])
        self.assertEqual(captured, record)

    def test_rejects_wrong_asset_etag_trailing_bytes_and_response_trailing_bytes(self):
        path, asset = self.write_blob("asset.bin", b"asset")
        self.state.blobs[asset["sha256"]] = path.read_bytes()
        record = record_value("hostile", [asset])
        self.state.candidates[record["id"]] = record
        for etag in ('W/"sha256:' + asset["sha256"] + '"', '"sha256:' + "0" * 64 + '"'):
            with self.subTest(etag=etag):
                self.state.asset_etag_override = etag
                with self.assertRaises(AuthorityError):
                    self.client.download_asset("candidate", record["id"], asset["name"])
        self.state.asset_etag_override = None
        self.state.asset_trailing = b"x"
        with self.assertRaises(AuthorityError):
            self.client.download_asset("candidate", record["id"], asset["name"])
        self.state.asset_trailing = b""
        self.state.record_trailing = b"x"
        with self.assertRaises(AuthorityError):
            self.client.capture_candidate(record["id"])
        self.state.record_trailing = b""

        upload, _ = self.write_blob("upload.bin", b"upload")
        self.state.response_trailing = b"x"
        with self.assertRaises(AuthorityError):
            self.client.put_blob(upload)

    def test_stale_replace_is_distinct_412_and_does_not_mutate(self):
        first_path, first = self.write_blob("one.bin", b"one")
        second_path, second = self.write_blob("two.bin", b"two")
        self.client.put_blob(first_path)
        self.client.put_blob(second_path)
        record = record_value("stale", [first])
        self.client.create_candidate(record)
        etag = record_etag(record)
        successor = record_value("stale", [first, second], sequence=2, previous=etag)
        self.state.stale_operation = "replaceCandidate"
        with self.assertRaises(PreconditionFailed) as captured:
            self.client.append_candidate(successor, etag)
        self.assertEqual(captured.exception.status, 412)
        request = {
            "schema": REQUEST_SCHEMA, "operation": "replaceCandidate", "ifMatch": etag,
            "id": "", "sha256": "", "size": 0, "record": successor,
        }
        self.state.stale_operation = "replaceCandidate"
        self.assertEqual(self.client.empirical_stale_replace(request), 412)
        self.assertEqual(self.state.candidates[record["id"]], record)

    def test_changed_or_unsafe_upload_files_fail_closed(self):
        upload, _ = self.write_blob("changing.bin", b"original")
        self.state.mutate_file = upload
        with self.assertRaises(AuthorityError):
            self.client.put_blob(upload)

        symlink = self.root / "linked.bin"
        symlink.symlink_to(upload)
        with self.assertRaises(AuthorityError):
            self.client.put_blob(symlink)
        fifo = self.root / "fifo"
        os.mkfifo(fifo)
        with self.assertRaises(AuthorityError):
            self.client.put_blob(fifo)
        empty = self.root / "empty"
        empty.write_bytes(b"")
        with self.assertRaises(AuthorityError):
            self.client.put_blob(empty)

    def test_unsafe_bases_hosts_paths_and_outputs_are_rejected(self):
        for base in (
            "http://models.s46.dev/release-authority/v1",
            "https://example.com/release-authority/v1",
            "http://127.0.0.1:1/release-authority/v1/../v1",
            "http://user@127.0.0.1:1/release-authority/v1",
        ):
            with self.subTest(base=base), self.assertRaises(AuthorityError):
                ReleaseAuthorityClient(base)
        with self.assertRaises(AuthorityError):
            self.client.capture_candidate("../release")
        with self.assertRaises(AuthorityError):
            self.client.download_asset("candidate", "safe", "../asset")

        unsafe_client = ReleaseAuthorityClient(
            self.api_base, ssh_host="-oProxyCommand=evil", ssh_key=self.key,
            known_hosts=self.known_hosts, ssh_program=self.ssh,
        )
        upload, _ = self.write_blob("safe.bin", b"safe")
        with self.assertRaises(AuthorityError):
            unsafe_client.put_blob(upload)

        destination = self.root / "existing"
        destination.write_bytes(b"preserve")
        path, asset = self.write_blob("download-source.bin", b"download")
        self.state.blobs[asset["sha256"]] = path.read_bytes()
        record = record_value("output", [asset])
        self.state.candidates[record["id"]] = record
        with self.assertRaises(AuthorityError):
            self.client.download_asset("candidate", record["id"], asset["name"], destination)
        self.assertEqual(destination.read_bytes(), b"preserve")

    def test_append_only_validation_allows_one_late_tag_object_but_rejects_replacement(self):
        one = asset_value("one", b"one")
        current = record_value("append", [one])
        successor = record_value(
            "append", [copy.deepcopy(one)], sequence=2, previous=record_etag(current),
        )
        validate_append_only(current, successor)

        retained_tag = copy.deepcopy(successor)
        retained_tag["sequence"] = 3
        retained_tag["previousETag"] = record_etag(successor)
        validate_append_only(successor, retained_tag)

        changed_tag = copy.deepcopy(retained_tag)
        changed_tag["source"]["tagObject"] = "d" * 40
        with self.assertRaisesRegex(AuthorityError, "source identity"):
            validate_append_only(successor, changed_tag)

        cleared_tag = copy.deepcopy(retained_tag)
        cleared_tag["source"]["tagObject"] = ""
        with self.assertRaisesRegex(AuthorityError, "source identity"):
            validate_append_only(successor, cleared_tag)

        changed_asset = copy.deepcopy(successor)
        changed_asset["assets"][0]["size"] += 1
        with self.assertRaisesRegex(AuthorityError, "changed asset"):
            validate_append_only(current, changed_asset)

        invalid_initial = copy.deepcopy(current)
        invalid_initial["source"]["tagObject"] = "d" * 40
        with self.assertRaisesRegex(AuthorityError, "initial candidate"):
            normalize_record(invalid_initial)

    def test_published_records_require_a_late_bound_tag_object(self):
        current = record_value("published-tag", [asset_value("one", b"one")])
        published_without_tag = copy.deepcopy(current)
        published_without_tag["state"] = "published"
        published_without_tag["sequence"] = 2
        published_without_tag["previousETag"] = record_etag(current)
        with self.assertRaisesRegex(AuthorityError, "annotated tag"):
            normalize_record(published_without_tag)

        successor = record_value(
            "published-tag", current["assets"], sequence=2, previous=record_etag(current),
        )
        self.assertEqual(finalized_record(successor)["state"], "published")

    def test_remote_errors_require_one_canonical_status_line(self):
        exact = b"s46-release-authority: 412 Precondition Failed: stale candidate\n"
        self.assertEqual(
            self.client._remote_error(exact), (412, "stale candidate"),
        )
        for stderr in (
            exact[:-1],
            exact + b"additional detail\n",
            b"warning\n" + exact,
            exact + exact,
            exact[:-1] + b"\r\n",
        ):
            with self.subTest(stderr=stderr):
                self.assertIsNone(self.client._remote_error(stderr))

    def test_mutation_transport_enforces_role_restrictions_before_ssh(self):
        record = record_value("roles", [asset_value("one", b"one")])
        create = self.client._request("createCandidate", "*", record=record)
        with self.assertRaisesRegex(AuthorityError, "publish role permits only"):
            self.client._mutate("publish", create)

        finalize = self.client._request(
            "finalizeCandidate", record_etag(record), identifier=record["id"],
        )
        with self.assertRaisesRegex(AuthorityError, "stage role cannot finalize"):
            self.client._mutate("stage", finalize)
        self.assertFalse(self.arguments.exists())


if __name__ == "__main__":
    unittest.main()
