"""Exact Gateway Mac qualification and purpose-scoped reviewer approval checks."""

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from scripts import gateway_direct_mac as candidate
from scripts import gateway_direct_mac_approval as approval


class GatewayDirectMacApprovalTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        self.evidence = self.root / "evidence"
        self.evidence.mkdir()
        (self.evidence / "control").mkdir()
        self.approvals = self.root / "approvals"
        self.approvals.mkdir()
        self.trust = self.root / "trust.json"
        previous = approval.TRUST
        approval.TRUST = self.trust
        self.addCleanup(setattr, approval, "TRUST", previous)
        self.record = self.make_candidate()
        self.qualification = self.make_qualification()
        self.qualification_path = self.root / "qualification.json"
        self.write_qualification()
        self.subject = approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        self.keys = self.make_keys()

    def make_candidate(self):
        archive = candidate.make_archive(b"test Gateway binary")
        binary = candidate.inspect_archive(archive)
        for name in ("first", "second"):
            directory = self.bundle / name
            directory.mkdir()
            (directory / candidate.ARCHIVE).write_bytes(archive)
        record = {
            "schema": candidate.SCHEMA, "state": "unsigned-candidate",
            "repository": "sovereign46/api", "version": candidate.VERSION,
            "platform": {"os": "darwin", "architecture": "arm64", "macOS": "26.3.1"},
            "source": {"commit": candidate.COMMIT, "tree": candidate.TREE,
                       "sourceDateEpoch": candidate.EPOCH, "archiveSha256": candidate.SOURCE_SHA256,
                       "archiveSizeBytes": candidate.SOURCE_SIZE},
            "toolchain": {"module": "golang.org/toolchain", "version": "go1.26.10",
                          "archiveSha256": candidate.TOOLCHAIN_SHA256,
                          "archiveSizeBytes": candidate.TOOLCHAIN_SIZE},
            "build": {"workroots": 2, "sameHost": True, "network": "go-module-resolution-off",
                      "flags": ["-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid="]},
            "gateway": {"name": candidate.ARCHIVE, "archiveSha256": candidate.sha256(archive),
                        "archiveSizeBytes": len(archive), "binarySha256": binary["sha256"],
                        "binarySizeBytes": binary["sizeBytes"]},
        }
        (self.bundle / candidate.RECORD).write_bytes(candidate.canonical(record))
        return record

    def make_qualification(self):
        diagnostic = {
            "kind": "s46-gateway-031-loopback-diagnostic-not-release-or-qualification",
            "apiCommit": candidate.COMMIT, "apiTree": candidate.TREE,
            "gatewayArchiveSha256": self.record["gateway"]["archiveSha256"],
            "modelSha256": approval.MODEL_SHA256,
            "llamaArchiveSha256": approval.LLAMA_ARCHIVE_SHA256,
            "inputs": {name: {"sha256": digest} for name, digest in {
                "gatewayArchive": self.record["gateway"]["archiveSha256"],
                "gatewayBinary": self.record["gateway"]["binarySha256"],
                "model": approval.MODEL_SHA256,
                "llamaArchive": approval.LLAMA_ARCHIVE_SHA256,
                "llamaBinary": approval.LLAMA_BINARY_SHA256,
            }.items()},
            "isolationProbe": "host private file denied; external egress denied; scratch write permitted",
            "cleanupErrors": [], "servicesStopped": True, "portsFree": True,
            "harnesses": {name: {"client": {"exit": 0}, "app": {"ok": True,
                                                               "output": f"hello from {name}",
                                                               "run": {"exit": 0}}}
                          for name in ("pi", "claude", "codex")},
        }
        files = {name: b"retained diagnostic evidence\n" for name in approval.DIAGNOSTIC_FILES}
        files["result.json"] = candidate.canonical(diagnostic)
        files["isolation-review.json"] = b"independently reviewed isolation evidence\n"
        files["loaded-process.json"] = b"loaded-process binding evidence\n"
        for name, body in files.items():
            (self.evidence / name).write_bytes(body)
        identity = lambda name: {"path": name, **candidate.file_identity(self.evidence / name, 16 << 20)}
        return {
            "schema": approval.QUALIFICATION_SCHEMA,
            "candidateRecordSha256": candidate.sha256((self.bundle / candidate.RECORD).read_bytes()),
            "gatewayArchiveSha256": self.record["gateway"]["archiveSha256"],
            "runtime": {"modelSha256": approval.MODEL_SHA256, "modelSizeBytes": approval.MODEL_SIZE,
                        "llamaArchiveSha256": approval.LLAMA_ARCHIVE_SHA256,
                        "llamaBinarySha256": approval.LLAMA_BINARY_SHA256},
            "controls": {name: True for name in approval.CONTROLS},
            "evidence": {
                "diagnostic": [identity(name) for name in sorted(approval.DIAGNOSTIC_FILES)],
                "independentIsolationReview": identity("isolation-review.json"),
                "loadedProcessBinding": identity("loaded-process.json"),
            },
        }

    def write_qualification(self):
        self.qualification_path.write_bytes(candidate.canonical(self.qualification))

    def make_keys(self):
        keys = {}
        public = {}
        for reviewer in approval.REVIEWERS:
            key = self.root / reviewer
            subprocess.run(["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)],
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            public[reviewer] = " ".join((key.with_suffix(".pub")).read_text().split()[:2])
            keys[reviewer] = key
        self.trust.write_bytes(candidate.canonical({"schema": approval.TRUST_SCHEMA, "purpose": approval.PURPOSE,
                                                    "reviewers": public}))
        return keys

    def sign(self, reviewer, key=None, subject=None, namespace=None):
        result = subprocess.run(["/usr/bin/ssh-keygen", "-Y", "sign", "-f", str(key or self.keys[reviewer]),
                                 "-n", namespace or approval.PURPOSE], input=subject or self.subject,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
        (self.approvals / f"{reviewer}.sig").write_bytes(result.stdout)

    def sign_both(self):
        for reviewer in approval.REVIEWERS:
            self.sign(reviewer)

    def test_two_distinct_reviewers_sign_same_subject(self):
        self.sign_both()
        approval.verify_approvals(self.subject, self.approvals)

    def test_forged_key_and_role_mismatch_fail(self):
        self.sign_both()
        self.sign("fredoliveira", key=self.keys["goncalossilva"])
        with self.assertRaisesRegex(approval.ApprovalError, "fredoliveira signature is invalid"):
            approval.verify_approvals(self.subject, self.approvals)
        self.sign("fredoliveira")
        fred = (self.approvals / "fredoliveira.sig").read_bytes()
        goncalo = (self.approvals / "goncalossilva.sig").read_bytes()
        (self.approvals / "fredoliveira.sig").write_bytes(goncalo)
        (self.approvals / "goncalossilva.sig").write_bytes(fred)
        with self.assertRaisesRegex(approval.ApprovalError, "signature is invalid"):
            approval.verify_approvals(self.subject, self.approvals)

    def test_other_signature_purpose_and_changed_evidence_fail(self):
        self.sign_both()
        self.sign("fredoliveira", namespace="s46-model-release-approval-v1")
        with self.assertRaisesRegex(approval.ApprovalError, "fredoliveira signature is invalid"):
            approval.verify_approvals(self.subject, self.approvals)
        (self.evidence / "loaded-process.json").write_bytes(b"changed evidence\n")
        with self.assertRaisesRegex(approval.ApprovalError, "evidence bytes differ"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)

    def test_tampered_subject_and_missing_consent_fail(self):
        self.sign_both()
        changed = json.loads(self.subject)
        changed["publicationConsent"]["approved"] = False
        with self.assertRaisesRegex(approval.ApprovalError, "signature is invalid"):
            approval.verify_approvals(candidate.canonical(changed), self.approvals)
        del changed["publicationConsent"]
        with self.assertRaisesRegex(approval.ApprovalError, "signature is invalid"):
            approval.verify_approvals(candidate.canonical(changed), self.approvals)
        self.qualification["gatewayArchiveSha256"] = "f" * 64
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "different candidate"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)

    def test_diagnostic_alone_or_failed_control_cannot_qualify(self):
        self.qualification["controls"]["loadedProcessBindingPassed"] = False
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "lacks a passed"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        self.qualification["controls"]["loadedProcessBindingPassed"] = True
        self.qualification["evidence"].pop("independentIsolationReview")
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "evidence is incomplete"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)

    def test_malformed_nested_qualification_types_fail_cleanly(self):
        self.qualification["controls"] = [{"loadedProcessBindingPassed": True}]
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "qualification lacks"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        self.qualification["controls"] = {name: True for name in approval.CONTROLS}
        self.qualification["evidence"]["diagnostic"][0]["path"] = ["README.md"]
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "complete private diagnostic"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)

    def test_symlinked_trust_evidence_and_signature_fail_closed(self):
        self.sign_both()
        path = self.trust
        raw = path.read_bytes()
        original = self.root / "trust-original.json"
        path.rename(original)
        path.symlink_to(original)
        with self.assertRaisesRegex(approval.ApprovalError, "regular file"):
            approval.trusted_reviewers()
        path.unlink()
        path.write_bytes(raw)

        path = self.evidence / "loaded-process.json"
        original = self.root / "loaded-process-original.json"
        path.rename(original)
        path.symlink_to(original)
        with self.assertRaisesRegex(approval.ApprovalError, "regular file"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        path.unlink()
        path.write_bytes(original.read_bytes())

        path = self.approvals / "fredoliveira.sig"
        original = self.root / "fred-original.sig"
        path.rename(original)
        path.symlink_to(original)
        with self.assertRaisesRegex(approval.ApprovalError, "regular file"):
            approval.verify_approvals(self.subject, self.approvals)

    def test_unenrolled_or_shared_key_trust_fails(self):
        self.trust.write_bytes(candidate.canonical({"schema": approval.TRUST_SCHEMA, "purpose": approval.PURPOSE,
                                                    "reviewers": dict.fromkeys(approval.REVIEWERS, "")}))
        with self.assertRaisesRegex(approval.ApprovalError, "not enrolled"):
            approval.trusted_reviewers()
        public = " ".join((self.keys["fredoliveira"].with_suffix(".pub")).read_text().split()[:2])
        self.trust.write_bytes(candidate.canonical({"schema": approval.TRUST_SCHEMA, "purpose": approval.PURPOSE,
                                                    "reviewers": dict.fromkeys(approval.REVIEWERS, public)}))
        with self.assertRaisesRegex(approval.ApprovalError, "distinct keys"):
            approval.trusted_reviewers()

    def test_malformed_trust_key_fails_cleanly(self):
        document = json.loads(self.trust.read_bytes())
        document["reviewers"]["fredoliveira"] = "ssh-ed25519 AAA"
        self.trust.write_bytes(candidate.canonical(document))
        with self.assertRaisesRegex(approval.ApprovalError, "invalid SSH Ed25519 key"):
            approval.trusted_reviewers()


if __name__ == "__main__":
    unittest.main()
