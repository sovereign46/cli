import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from scripts import cli_mac_airplane_candidate as mac
from scripts import cli_mac_airplane_release as release
from scripts import gateway_direct_mac_approval as approval
from scripts.release_authority import canonical_record, record_etag


class MacAirplaneReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        self.binary = b"synthetic CLI executable for record builder tests"
        with tarfile.open(self.bundle / mac.ARCHIVE, "w:gz") as archive:
            info = tarfile.TarInfo("s46")
            info.mode = 0o755
            info.size = len(self.binary)
            archive.addfile(info, io.BytesIO(self.binary))
        self.archive_sha = hashlib.sha256((self.bundle / mac.ARCHIVE).read_bytes()).hexdigest()
        self.subject = (Path(__file__).with_name("testdata") /
                        "gateway-approval-subject-ef9d322.json").read_bytes()
        (self.bundle / "gateway-approval-subject.json").write_bytes(self.subject)
        self.keys = {}
        for reviewer in approval.REVIEWERS:
            key = self.root / reviewer
            subprocess.run(["/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "",
                            "-f", str(key)], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.keys[reviewer] = key
            (self.bundle / f"{reviewer}.sig").write_bytes(self.sign(key, approval.PURPOSE, self.subject))
        self.gateway_trust = self.root / "gateway-trust.json"
        self.cli_trust = self.root / "cli-trust.json"
        for path, schema, purpose in ((self.gateway_trust, approval.TRUST_SCHEMA, approval.PURPOSE),
                                      (self.cli_trust, release.TRUST_SCHEMA, release.PURPOSE)):
            path.write_bytes(approval.canonical({
                "schema": schema, "purpose": purpose,
                "reviewers": {name: " ".join(self.keys[name].with_suffix(".pub").read_text().split(" ", 2)[:2])
                              for name in approval.REVIEWERS},
            }))
        self.gateway_patch = patch.object(mac, "checked_subject", return_value=self.subject)
        self.gateway_trust_patch = patch.object(approval, "TRUST", self.gateway_trust)
        self.cli_trust_patch = patch.object(release, "TRUST", self.cli_trust)
        self.real_checked_manifest = release.checked_manifest
        self.real_remote_check = release.require_remote_main
        self.manifest_patch = patch.object(release, "checked_manifest", return_value=("c" * 64, 1234))
        self.remote_patch = patch.object(release, "require_remote_main", return_value=None)
        for active in (self.gateway_patch, self.gateway_trust_patch, self.cli_trust_patch,
                       self.manifest_patch, self.remote_patch):
            active.start()
            self.addCleanup(active.stop)
        self.commit = "a" * 40
        self.tree = "b" * 40
        self.seq2 = self.sequence_two()
        self.seq2_path = self.root / "sequence-two.json"
        self.seq2_path.write_bytes(canonical_record(self.seq2))
        self.write_qualification(self.evidence())

    @staticmethod
    def sign(key, purpose, subject):
        return subprocess.run(["/usr/bin/ssh-keygen", "-Y", "sign", "-f", str(key), "-n", purpose],
                              input=subject, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout

    def sequence_two(self):
        assets = []
        for name, (media, role) in sorted(mac.PROFILE.items()):
            raw = (self.bundle / name).read_bytes()
            assets.append({"name": name, "sha256": hashlib.sha256(raw).hexdigest(),
                           "size": len(raw), "mediaType": media, "role": role})
        return mac.candidate_records(self.commit, self.tree, assets,
                                     hashlib.sha256(self.subject).hexdigest())[1]

    def evidence(self):
        archive = (self.bundle / mac.ARCHIVE).read_bytes()
        return {
            "schema": "s46.release-qualification.v1", "result": "passed",
            "qualifiedAt": "2026-11-05T12:00:00Z",
            "artifact": {"name": mac.ARCHIVE, "sha256": hashlib.sha256(archive).hexdigest(),
                         "size": len(archive), "kind": "archive"},
            "binary": {"sha256": hashlib.sha256(self.binary).hexdigest(),
                       "size": len(self.binary), "sourceMode": "0755"},
            "build": {"version": "0.3.1", "commit": self.commit, "date": "2026-11-05T11:00:00Z",
                      "goVersion": "go1.25.0", "release": True, "sourceTree": self.tree},
            "qualification": {
                "expectedVersion": "0.3.1", "expectedCommit": self.commit,
                "networkIsolation": "external", "filesystemIsolation": "external",
                "installation": {"command": "s46 airplane setup --mode=on --harness=pi --yes",
                                 "harnessConfiguration": ["s46 connect local --harness=claude-code",
                                                          "s46 connect local --harness=codex"],
                                 "home": "runner-created", "tmpdir": "runner-created",
                                 "installedRuntimeVerified": True},
                "capabilities": ["cloud-client", "airplane"],
                "airplaneHarnesses": ["pi", "claude-code", "codex"],
                "runtime": {"model": {"name": "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf",
                                      "sha256": approval.MODEL_SHA256, "size": approval.MODEL_SIZE},
                            "llamaServer": {"name": "llama-server", "sha256": approval.LLAMA_BINARY_SHA256,
                                            "size": 8023664},
                            "llamaRuntimeInventory": {"schema": "s46.llamacpp-runtime-inventory.v1",
                                                      "sha256": "5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167",
                                                      "entries": 12},
                            "gateway": {"name": "s46-gateway", "sha256": mac.GATEWAY_BINARY_SHA256,
                                        "size": 123456}},
                "runner": {"script": {"name": "e2e-airplane-harnesses", "sha256": "c" * 64,
                                      "size": 1234}, "id": "42-0123456789abcdef0123456789abcdef",
                           "processCohortSha256": "f" * 64, "processCount": 6,
                           "harnessProcessCount": 3, "serviceProcessCount": 2,
                           "verifiedNoSurvivors": True, "cleanupVerifiedAt": "2026-11-05T12:00:00Z"},
                "execution": {"provenance": "direct-mac", "runnerOs": "Darwin",
                              "runnerArch": "arm64", "osVersion": "26.0", "runnerName": "test-mac",
                              "releaseAuthorityOrigin": release.AUTHORITY_ORIGIN,
                              "releaseAuthorityId": self.seq2["id"], "candidateSequence": 2,
                              "candidateEtag": record_etag(self.seq2),
                              "candidateRecordSha256": hashlib.sha256(canonical_record(self.seq2)).hexdigest(),
                              "candidateStaleIfMatchStatus": 412,
                              "isolationProfileSha256": "1" * 64},
            },
        }

    def write_qualification(self, evidence):
        raw = (json.dumps(evidence, indent=2, sort_keys=True) + "\n").encode()
        (self.bundle / release.QUALIFICATION).write_bytes(raw)
        for name in approval.REVIEWERS:
            (self.bundle / f"{name}.cli-qualification.sig").write_bytes(
                self.sign(self.keys[name], release.PURPOSE, raw))

    def args(self):
        return SimpleNamespace(bundle=str(self.bundle), archive_sha256=self.archive_sha,
                               gateway_bundle="/gateway/build", gateway_qualification="/gateway/qualification.json",
                               gateway_evidence_root="/gateway/evidence", commit=self.commit,
                               source_tree=self.tree, source_repo=str(self.root),
                               sequence_two=str(self.seq2_path), output=str(self.root / "qualified.json"))

    def test_full_signed_installer_evidence_appends_three_assets(self):
        record = release.qualified_record(self.args())
        self.assertEqual((record["sequence"], record["previousETag"]), (3, record_etag(self.seq2)))
        self.assertEqual({asset["name"] for asset in record["assets"]}, set(release.QUALIFIED_PROFILE))

    def test_adapter_only_evidence_cannot_qualify(self):
        evidence = self.evidence()
        del evidence["qualification"]["installation"]
        self.write_qualification(evidence)
        with self.assertRaisesRegex(SystemExit, "full airplane installer"):
            release.qualified_record(self.args())

    def test_wrong_source_or_runtime_cannot_qualify(self):
        for field, value in (("expectedCommit", "0" * 40), ("networkIsolation", "proxy")):
            evidence = self.evidence()
            evidence["qualification"][field] = value
            self.write_qualification(evidence)
            with self.subTest(field=field), self.assertRaises(SystemExit):
                release.qualified_record(self.args())
        evidence = self.evidence()
        evidence["qualification"]["runtime"]["model"]["sha256"] = "0" * 64
        self.write_qualification(evidence)
        with self.assertRaisesRegex(SystemExit, "runtime differs"):
            release.qualified_record(self.args())

    def test_wrong_authority_or_signature_cannot_qualify(self):
        evidence = self.evidence()
        evidence["qualification"]["execution"]["candidateEtag"] = '"sha256:' + "0" * 64 + '"'
        self.write_qualification(evidence)
        with self.assertRaisesRegex(SystemExit, "authority candidate"):
            release.qualified_record(self.args())
        self.write_qualification(self.evidence())
        (self.bundle / "fredoliveira.cli-qualification.sig").write_bytes(
            (self.bundle / "goncalossilva.cli-qualification.sig").read_bytes())
        with self.assertRaises(approval.ApprovalError):
            release.qualified_record(self.args())

    def test_default_unenrolled_trust_fails_closed(self):
        with patch.object(release, "TRUST", Path(release.__file__).with_name(
                "cli-direct-mac-qualification-trust.json")):
            with self.assertRaises(approval.ApprovalError):
                release.qualified_record(self.args())

    def test_unqualified_source_cannot_qualify(self):
        manifest = json.dumps({"schema": "s46.airplane-release.v3", "qualified": False}).encode()
        with patch.object(release, "source_tree", return_value=self.tree), \
                patch.object(release, "source_bytes", return_value=manifest):
            with self.assertRaisesRegex(SystemExit, "not qualified"):
                self.real_checked_manifest(self.root, self.commit, self.tree)

    @patch.dict(os.environ, release.GIT_ENV, clear=True)
    def test_branch_commit_is_not_release_authority(self):
        repo = self.root / "source-check"
        remote = self.root / "origin-check.git"
        repo.mkdir()
        subprocess.run(["git", "init", "-q", "-b", "main", str(repo)], check=True)
        subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.name", "Test Reviewer"], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.email", "test@s46.invalid"], check=True)
        subprocess.run(["git", "-C", str(repo), "remote", "add", "origin", str(remote)], check=True)
        (repo / "README").write_text("main\n")
        subprocess.run(["git", "-C", str(repo), "add", "README"], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "Main fixture"], check=True)
        subprocess.run(["git", "-C", str(repo), "push", "-q", "origin", "main"], check=True)
        main_commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"]).decode().strip()
        subprocess.run(["git", "-C", str(repo), "switch", "-qc", "unreviewed"], check=True)
        (repo / "README").write_text("branch\n")
        subprocess.run(["git", "-C", str(repo), "commit", "-qam", "Branch fixture"], check=True)
        branch_commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"]).decode().strip()
        with patch.object(release, "OFFICIAL_ORIGINS", {str(remote)}):
            self.real_remote_check(repo, main_commit)
            with self.assertRaisesRegex(SystemExit, "exact remote main head"):
                self.real_remote_check(repo, branch_commit)

    @patch.dict(os.environ, release.GIT_ENV, clear=True)
    def test_publication_plan_binds_exact_annotated_tag(self):
        repo = self.root / "source"
        repo.mkdir()
        subprocess.run(["git", "-C", str(repo), "init", "-q"], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.name", "Test Reviewer"], check=True)
        subprocess.run(["git", "-C", str(repo), "config", "user.email", "test@s46.invalid"], check=True)
        (repo / "README").write_text("source fixture\n")
        subprocess.run(["git", "-C", str(repo), "add", "README"], check=True)
        subprocess.run(["git", "-C", str(repo), "commit", "-qm", "Add fixture"], check=True)
        self.commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"]).decode().strip()
        self.tree = release.source_tree(repo, self.commit)
        self.seq2 = self.sequence_two()
        self.seq2_path.write_bytes(canonical_record(self.seq2))
        self.write_qualification(self.evidence())
        third = release.qualified_record(self.args())
        third_path = self.root / "sequence-three.json"
        third_path.write_bytes(canonical_record(third))
        subprocess.run(["git", "-C", str(repo), "tag", "-a", mac.TAG, "-m", "Test Mac tag"], check=True)
        remote = self.root / "origin.git"
        subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
        subprocess.run(["git", "-C", str(repo), "remote", "add", "origin", str(remote)], check=True)
        subprocess.run(["git", "-C", str(repo), "push", "-q", "origin", f"refs/tags/{mac.TAG}"], check=True)
        tag_object = subprocess.check_output(["git", "-C", str(repo), "rev-parse",
                                              f"refs/tags/{mac.TAG}"]).decode().strip()
        args = self.args()
        args.source_repo = str(repo)
        args.sequence_three = str(third_path)
        args.tag_object = tag_object
        args.output = str(self.root / "publication")
        release.command_publication(args)
        fourth = release.parse_record((self.root / "publication/candidate-sequence-4.json").read_bytes())
        fifth = release.parse_record((self.root / "publication/expected-published-sequence-5.json").read_bytes())
        self.assertEqual((fourth["sequence"], fourth["source"]["tagObject"]), (4, tag_object))
        self.assertEqual((fifth["sequence"], fifth["state"]), (5, "published"))


if __name__ == "__main__":
    unittest.main()
