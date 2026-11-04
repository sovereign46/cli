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
        model_path = "/Users/_s46airplane/input/model.gguf"
        llama = self.evidence / "runtime/llama/llama-b9270/llama-server"
        metal = self.evidence / "runtime/llama/llama-b9270/libggml-metal.0.12.0.dylib"
        gateway = self.evidence / "gateway/s46-gateway"
        files = {name: b"retained diagnostic evidence\n" for name in approval.DIAGNOSTIC_REQUIRED}
        files["home/pi/.pi/agent/sessions/test.jsonl"] = b'{"type":"toolCall","name":"write"}\n'
        for name in ("pi", "claude", "codex"):
            files[f"project/{name}_hello.py"] = f'def hello_from_{name}():\n    return "hello from {name}"\n'.encode()
            files[f"out/{name}_app.stdout"] = f"hello from {name}\n".encode()
            files[f"out/{name}.stderr"] = b""
            files[f"out/{name}_app.stderr"] = b""
        files["out/sandbox-probe.stdout"] = candidate.canonical({
            "file": {"type": "PermissionError", "errno": 1},
            "network": {"type": "PermissionError", "errno": 1},
        })
        files["out/openai-stream.sse"] = b"data: {}\n\ndata: [DONE]\n\n"
        files["out/anthropic-stream.sse"] = b"event: message_stop\ndata: {}\n\n"
        files["out/codex-stream.sse"] = b"event: response.completed\ndata: {}\n\n"
        for phase in ("before", "after"):
            files[f"out/llama-{phase}-lsof.txt"] = (
                f"llama-ser 123 user txt REG 1,15 8023664 7 {llama}\n"
                f"llama-ser 123 user txt REG 1,15 {approval.MODEL_SIZE} 9 {model_path}\n").encode()
            files[f"out/llama-{phase}-vmmap.txt"] = f"{model_path}\n{metal}\n".encode()
            files[f"out/llama-{phase}-ps.txt"] = b"123 llama-server\n"
        files["out/gateway-before-lsof.txt"] = f"s46-gatew 124 user txt REG 1,15 12 8 {gateway}\n".encode()
        files["out/gateway-before-vmmap.txt"] = f"{gateway}\n".encode()
        files["out/gateway-before-ps.txt"] = b"124 s46-gateway\n"
        def observed(name):
            return {"exit": 0, "path": str(self.evidence / name),
                    "sha256": candidate.sha256(files[name]), "sizeBytes": len(files[name])}
        def process(pid, executable, digest, phase, model=False):
            prefix = f"out/{phase}"
            return {"process": {"pid": pid, "executable": str(executable), "executableSha256": digest},
                    "lsof": observed(prefix + "-lsof.txt"), "ps": observed(prefix + "-ps.txt"),
                    "vmmap": observed(prefix + "-vmmap.txt"), **({"modelPathObserved": True} if model else {})}
        harnesses = {}
        for name in ("pi", "claude", "codex"):
            harnesses[name] = {
                "client": {"exit": 0, "stdoutSha256": candidate.sha256(files[f"out/{name}.stdout"]),
                           "stderrSha256": candidate.sha256(files[f"out/{name}.stderr"])},
                "app": {"ok": True, "output": f"hello from {name}",
                        "run": {"exit": 0,
                                "stdoutSha256": candidate.sha256(files[f"out/{name}_app.stdout"]),
                                "stderrSha256": candidate.sha256(files[f"out/{name}_app.stderr"])},
                        "sourceSha256": candidate.sha256(files[f"project/{name}_hello.py"])},
            }
        streams = {}
        for name, terminal in (("openai", "data: [DONE]"), ("anthropic", "event: message_stop"),
                               ("codex", "event: response.completed")):
            stream = f"out/{name}-stream.sse"
            streams[name] = {"path": str(self.evidence / stream), "status": 200,
                             "contentType": "text/event-stream", "rawSha256": candidate.sha256(files[stream]),
                             "sizeBytes": len(files[stream]), "terminal": terminal}
        diagnostic = {
            "kind": approval.DIAGNOSTIC_KIND, "projectInitiallyEmpty": True,
            "inputs": {"gatewayArchiveSha256": self.record["gateway"]["archiveSha256"],
                       "gatewayBinarySha256": self.record["gateway"]["binarySha256"],
                       "llamaArchiveSha256": approval.LLAMA_ARCHIVE_SHA256,
                       "llamaBinarySha256": approval.LLAMA_BINARY_SHA256,
                       "model": {"path": model_path, "sha256": approval.MODEL_SHA256,
                                 "sizeBytes": approval.MODEL_SIZE, "inode": 9}},
            "modelSha256After": approval.MODEL_SHA256,
            "sandboxProfileSha256": candidate.sha256(files["control/diagnostic.sb"]),
            "llamaBeforeRequest": process(123, llama, approval.LLAMA_BINARY_SHA256, "llama-before", True),
            "llamaAfterRequest": process(123, llama, approval.LLAMA_BINARY_SHA256, "llama-after", True),
            "gatewayBeforeRequest": process(124, gateway, self.record["gateway"]["binarySha256"],
                                            "gateway-before"),
            "harnesses": harnesses, "streams": streams,
            "inference": {"status": 200, "body": {"model": approval.PUBLIC_MODEL,
                                                   "choices": [{"finish_reason": "stop",
                                                                "message": {"content": "hello from devstral"}}]}},
            "worker": {"state": "ready", "mode": "airplane",
                       "models": [{"id": approval.PUBLIC_MODEL,
                                   "backendModel": approval.BACKEND_MODEL, "state": "ready"}]},
            "cleanupErrors": [], "servicesStopped": True, "portsFree": True,
        }
        files["result.json"] = candidate.canonical(diagnostic)
        for name, body in files.items():
            path = self.evidence / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        inventory = {"schema": approval.DIAGNOSTIC_INVENTORY,
                     "files": [{"path": name, "sha256": candidate.sha256(files[name]),
                                "sizeBytes": len(files[name])} for name in sorted(files)]}
        (self.evidence / "evidence-manifest.json").write_bytes(candidate.canonical(inventory))
        (self.evidence / "isolation-review.json").write_bytes(b"independently reviewed isolation evidence\n")
        (self.evidence / "POST-RUN-LIBRARY-AUDIT.md").write_bytes(b"post-run pinned tree audit\n")
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
                "diagnosticManifest": identity("evidence-manifest.json"),
                "independentIsolationReview": identity("isolation-review.json"),
                "loadedProcessBinding": identity("POST-RUN-LIBRARY-AUDIT.md"),
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

    def diagnostic_files(self):
        return approval.diagnostic_files(self.evidence, self.qualification["evidence"]["diagnosticManifest"])

    def rejected_diagnostic(self, result, message, files=None):
        files = files or self.diagnostic_files()
        with self.assertRaisesRegex(approval.ApprovalError, message):
            approval.verify_diagnostic({**files, "result.json": candidate.canonical(result)},
                                       self.evidence, self.record)

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
        (self.evidence / "POST-RUN-LIBRARY-AUDIT.md").write_bytes(b"changed evidence\n")
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

    def test_boolean_exit_cannot_pass_diagnostic(self):
        files = self.diagnostic_files()
        result = json.loads(files["result.json"])
        result["harnesses"]["pi"]["client"]["exit"] = False
        self.rejected_diagnostic(result, "pi client did not pass", files)
        result["harnesses"]["pi"]["client"]["exit"] = 0
        result["harnesses"]["pi"]["app"]["run"]["exit"] = False
        self.rejected_diagnostic(result, "pi client did not pass", files)

    def test_old_diagnostic_or_stale_project_cannot_qualify(self):
        result = json.loads(self.diagnostic_files()["result.json"])
        result["kind"] = "s46-gateway-031-loopback-diagnostic-not-release-or-qualification"
        self.rejected_diagnostic(result, "fresh project differs")
        result["kind"] = approval.DIAGNOSTIC_KIND
        result["projectInitiallyEmpty"] = False
        self.rejected_diagnostic(result, "fresh project differs")
        result["projectInitiallyEmpty"] = True
        result["error"] = "RuntimeError: diagnostic failed"
        self.rejected_diagnostic(result, "diagnostic result", None)

    def test_stream_and_process_records_must_match_raw_evidence(self):
        files = self.diagnostic_files()
        result = json.loads(files["result.json"])
        files["out/openai-stream.sse"] = b"data: {}\n\n"
        result["streams"]["openai"]["rawSha256"] = candidate.sha256(files["out/openai-stream.sse"])
        result["streams"]["openai"]["sizeBytes"] = len(files["out/openai-stream.sse"])
        self.rejected_diagnostic(result, "openai stream did not terminate", files)
        files = self.diagnostic_files()
        result = json.loads(files["result.json"])
        files["out/openai-stream.sse"] = b'data: {}\n\ndata: {"note":"data: [DONE]"}\n\n'
        result["streams"]["openai"]["rawSha256"] = candidate.sha256(files["out/openai-stream.sse"])
        result["streams"]["openai"]["sizeBytes"] = len(files["out/openai-stream.sse"])
        self.rejected_diagnostic(result, "openai stream did not terminate", files)
        files = self.diagnostic_files()
        result = json.loads(files["result.json"])
        result["llamaAfterRequest"]["process"]["pid"] = 999
        self.rejected_diagnostic(result, "llama-after executable is not in lsof", files)

    def test_inference_and_worker_model_must_match_devstral(self):
        result = json.loads(self.diagnostic_files()["result.json"])
        result["inference"]["body"]["model"] = "other-model"
        self.rejected_diagnostic(result, "inference did not complete")
        result["inference"]["body"]["model"] = approval.PUBLIC_MODEL
        result["worker"]["models"][0]["backendModel"] = "other-model"
        self.rejected_diagnostic(result, "inference did not complete")

    def test_sandbox_policy_denial_and_full_inventory_are_required(self):
        files = self.diagnostic_files()
        result = json.loads(files["result.json"])
        files["out/sandbox-probe.stdout"] = candidate.canonical({
            "file": {"type": "PermissionError", "errno": 1},
            "network": {"type": "TimeoutError", "errno": 110},
        })
        self.rejected_diagnostic(result, "sandbox denial is missing", files)
        manifest = json.loads((self.evidence / "evidence-manifest.json").read_bytes())
        manifest["files"] = [item for item in manifest["files"] if item["path"] != "out/claude.stdout"]
        (self.evidence / "evidence-manifest.json").write_bytes(candidate.canonical(manifest))
        self.qualification["evidence"]["diagnosticManifest"] = {
            "path": "evidence-manifest.json",
            **candidate.file_identity(self.evidence / "evidence-manifest.json", 1 << 20),
        }
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "inventory omits required evidence"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)

    def test_empty_log_is_bound_and_duplicate_json_keys_fail(self):
        (self.evidence / "out/pi.stderr").write_bytes(b"unexpected stderr")
        with self.assertRaisesRegex(approval.ApprovalError, "evidence bytes differ"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        with self.assertRaisesRegex(approval.ApprovalError, "duplicate key"):
            approval.parse_json(b'{"kind":1,"kind":2}', "result")

    def test_malformed_nested_qualification_types_fail_cleanly(self):
        self.qualification["controls"] = [{"loadedProcessBindingPassed": True}]
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "qualification lacks"):
            approval.checked_subject(self.bundle, self.qualification_path, self.evidence)
        self.qualification["controls"] = {name: True for name in approval.CONTROLS}
        self.qualification["evidence"]["diagnosticManifest"]["path"] = ["evidence-manifest.json"]
        self.write_qualification()
        with self.assertRaisesRegex(approval.ApprovalError, "diagnostic manifest"):
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

        path = self.evidence / "POST-RUN-LIBRARY-AUDIT.md"
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
