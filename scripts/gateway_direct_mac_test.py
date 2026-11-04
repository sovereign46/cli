"""Security checks for the unsigned direct Mac Gateway candidate."""

import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest

from scripts import gateway_direct_mac as candidate


class GatewayDirectMacTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def bundle(self):
        archive = candidate.make_archive(b"test Gateway binary")
        binary = candidate.inspect_archive(archive)
        for name in ("first", "second"):
            directory = self.root / name
            directory.mkdir()
            (directory / candidate.ARCHIVE).write_bytes(archive)
        record = {
            "schema": candidate.SCHEMA, "state": "unsigned-candidate",
            "repository": "sovereign46/api", "version": candidate.VERSION,
            "platform": {"os": "darwin", "architecture": "arm64", "macOS": "26.3.1"},
            "source": {"commit": candidate.COMMIT, "tree": candidate.TREE,
                       "sourceDateEpoch": candidate.EPOCH,
                       "archiveSha256": candidate.SOURCE_SHA256,
                       "archiveSizeBytes": candidate.SOURCE_SIZE},
            "toolchain": {"module": "golang.org/toolchain", "version": "go1.26.10",
                          "archiveSha256": candidate.TOOLCHAIN_SHA256,
                          "archiveSizeBytes": candidate.TOOLCHAIN_SIZE},
            "build": {"workroots": 2, "sameHost": True, "network": "go-module-resolution-off",
                      "flags": ["-mod=vendor", "-trimpath", "-buildvcs=false",
                                "-ldflags=-s -w -buildid="]},
            "gateway": {"name": candidate.ARCHIVE, "archiveSha256": candidate.sha256(archive),
                        "archiveSizeBytes": len(archive), "binarySha256": binary["sha256"],
                        "binarySizeBytes": binary["sizeBytes"]},
        }
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        return record

    def verify(self):
        with contextlib.redirect_stdout(io.StringIO()):
            candidate.command_verify(type("Args", (), {"bundle": str(self.root)})())

    def test_two_exact_archives_verify(self):
        self.bundle()
        self.verify()

    def test_archive_replacement_and_extra_member_fail(self):
        self.bundle()
        archive = self.root / "second" / candidate.ARCHIVE
        archive.write_bytes(candidate.make_archive(b"replacement Gateway binary"))
        with self.assertRaisesRegex(candidate.CandidateError, "archive differs"):
            self.verify()
        archive.write_bytes((self.root / "first" / candidate.ARCHIVE).read_bytes())
        (self.root / "second" / "extra").write_bytes(b"unexpected")
        with self.assertRaisesRegex(candidate.CandidateError, "unexpected content"):
            self.verify()

    def test_source_and_toolchain_retargeting_fail(self):
        record = self.bundle()
        record["source"]["commit"] = "f" * 40
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "source differs"):
            self.verify()
        record["source"]["commit"] = candidate.COMMIT
        record["toolchain"]["archiveSha256"] = "f" * 64
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "toolchain differs"):
            self.verify()

    def test_noncanonical_record_and_symlink_fail(self):
        record = self.bundle()
        record_path = self.root / candidate.RECORD
        record_path.write_text(json.dumps(json.loads(record_path.read_bytes()), indent=2))
        with self.assertRaisesRegex(candidate.CandidateError, "not canonical"):
            self.verify()
        record_path.write_bytes(candidate.canonical(record))
        second = self.root / "second" / candidate.ARCHIVE
        second.unlink()
        second.symlink_to(self.root / "first" / candidate.ARCHIVE)
        with self.assertRaisesRegex(candidate.CandidateError, "regular file"):
            self.verify()

    def test_archive_must_have_exact_encoding(self):
        archive = candidate.make_archive(b"binary")
        self.assertEqual(candidate.inspect_archive(archive)["sha256"], candidate.sha256(b"binary"))
        with self.assertRaisesRegex(candidate.CandidateError, "invalid|deterministic"):
            candidate.inspect_archive(archive + b"trailing")

    def test_unreviewed_extension_fields_fail(self):
        record = self.bundle()
        record["runtimeCertified"] = True
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "unexpected fields"):
            self.verify()
        del record["runtimeCertified"]
        record["platform"]["quality"] = "pass"
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "platform differs"):
            self.verify()

    def test_malformed_platform_and_gateway_fail_cleanly(self):
        record = self.bundle()
        record["platform"] = 7
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "platform differs"):
            self.verify()
        record["platform"] = {"os": "darwin", "architecture": "arm64", "macOS": "26.3.1"}
        record["gateway"] = 7
        (self.root / candidate.RECORD).write_bytes(candidate.canonical(record))
        with self.assertRaisesRegex(candidate.CandidateError, "Gateway fields differ"):
            self.verify()


if __name__ == "__main__":
    unittest.main()
