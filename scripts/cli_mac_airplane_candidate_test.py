import hashlib
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts import gateway_direct_mac_approval as approval
from scripts import cli_mac_airplane_candidate as mac
from scripts.cli_release_authority import _profile
from scripts.release_authority import parse_record, record_etag


class MacAirplaneCandidateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        (self.bundle / mac.ARCHIVE).write_bytes(b"pinned Mac CLI archive")
        self.archive_sha256 = hashlib.sha256((self.bundle / mac.ARCHIVE).read_bytes()).hexdigest()
        self.subject = (Path(__file__).with_name("testdata") /
                        "gateway-approval-subject-ef9d322.json").read_bytes()
        self.assertEqual(hashlib.sha256(self.subject).hexdigest(), mac.GATEWAY_APPROVAL_SUBJECT_SHA256)
        (self.bundle / "gateway-approval-subject.json").write_bytes(self.subject)
        reviewers = {}
        for name in approval.REVIEWERS:
            key = self.root / name
            subprocess.run(["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "",
                            "-f", str(key)], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            reviewers[name] = key.with_suffix(".pub").read_text().split(" ", 2)[:2]
            signature = subprocess.run(["/usr/bin/ssh-keygen", "-Y", "sign", "-f", str(key),
                                        "-n", approval.PURPOSE], input=self.subject, check=True,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout
            (self.bundle / f"{name}.sig").write_bytes(signature)
        self.trust = self.root / "trust.json"
        self.trust.write_bytes(approval.canonical({
            "schema": approval.TRUST_SCHEMA, "purpose": approval.PURPOSE,
            "reviewers": {name: " ".join(parts) for name, parts in reviewers.items()},
        }))
        self.gateway_patch = patch.object(mac, "checked_subject", return_value=self.subject)
        self.trust_patch = patch.object(approval, "TRUST", self.trust)
        self.gateway_patch.start()
        self.trust_patch.start()
        self.addCleanup(self.gateway_patch.stop)
        self.addCleanup(self.trust_patch.stop)

    def args(self, output="records", digest=None):
        return SimpleNamespace(
            commit="a" * 40, source_tree="b" * 40, bundle=str(self.bundle),
            archive_sha256=self.archive_sha256 if digest is None else digest,
            gateway_bundle="/gateway/build", gateway_qualification="/gateway/qualification.json",
            gateway_evidence_root="/gateway/evidence", output=str(self.root / output),
        )

    def test_signed_exact_mac_archive_creates_separate_candidate(self):
        mac.command_stage(self.args())
        first = parse_record((self.root / "records/candidate-sequence-1.json").read_bytes())
        second = parse_record((self.root / "records/candidate-sequence-2.json").read_bytes())
        self.assertEqual(first["source"]["tag"], mac.TAG)
        self.assertNotEqual(first["id"], mac.release_id(mac.REPOSITORY, "v0.3.1"))
        self.assertNotEqual(first["source"]["bundleSha256"], self.archive_sha256)
        self.assertEqual({asset["name"] for asset in first["assets"]}, set(mac.PROFILE))
        self.assertEqual(mac.assets_by_name(first["assets"])[mac.ARCHIVE]["sha256"], self.archive_sha256)
        self.assertEqual({item["name"]: item["value"] for item in first["annotations"]}
                         ["mac-cli-archive-sha256"], self.archive_sha256)
        self.assertEqual((second["sequence"], second["previousETag"]), (2, record_etag(first)))
        self.assertEqual(len(_profile("v0.3.1", True)), 15)

    def test_wrong_archive_pin_fails_before_creating_records(self):
        with self.assertRaisesRegex(SystemExit, "independent SHA-256 pin"):
            mac.command_stage(self.args(digest="0" * 64))
        self.assertFalse((self.root / "records").exists())

    def test_wrong_gateway_subject_fails_before_creating_records(self):
        (self.bundle / "gateway-approval-subject.json").write_bytes(b"{}\n")
        with self.assertRaisesRegex(SystemExit, "subject differs"):
            mac.command_stage(self.args())
        self.assertFalse((self.root / "records").exists())

    def test_missing_or_wrong_reviewer_signature_fails_closed(self):
        (self.bundle / "fredoliveira.sig").write_bytes((self.bundle / "goncalossilva.sig").read_bytes())
        with self.assertRaises(approval.ApprovalError):
            mac.command_stage(self.args())
        self.assertFalse((self.root / "records").exists())

    def test_gateway_digest_is_fixed_independent_of_signed_subject(self):
        different = self.subject.replace(mac.GATEWAY_ARCHIVE_SHA256.encode(), b"0" * 64)
        with patch.object(mac, "checked_subject", return_value=different):
            with self.assertRaisesRegex(SystemExit, "exact reviewed packet"):
                mac.command_stage(self.args())
        self.assertFalse((self.root / "records").exists())

    def test_changed_qualification_with_same_gateway_bytes_is_rejected(self):
        different = self.subject.replace(b"7ece1191378e1ae3d660ae18465ce8a2e5faaf4ec67c89927f18f7cadd76f336",
                                         b"0" * 64)
        with patch.object(mac, "checked_subject", return_value=different):
            with self.assertRaisesRegex(SystemExit, "exact reviewed packet"):
                mac.command_stage(self.args())
        self.assertFalse((self.root / "records").exists())


if __name__ == "__main__":
    unittest.main()
