from pathlib import Path
import tempfile
import unittest

from scripts.cli_release_authority import _candidate, _profile, command_qualified, command_qualified_recovery, command_tagged
from scripts.release_authority import argument_parser, parse_record, record_etag


class CLIReleaseAuthorityTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.tag = "v0.3.0"
        self.commit = "a" * 40
        self.tree = "b" * 40
        self.build = self.root / "build"
        self.build.mkdir(mode=0o700)
        for name in _profile(self.tag, False):
            (self.build / name).write_bytes((name + "\n").encode())

    def test_exact_sequence_one_and_two(self):
        initial, second = _candidate(self.tag, self.commit, self.tree, self.build)
        self.assertEqual(initial["id"], "sha256-7415133f54ffc6cb05815c61b954dd09b1692ecc730319bc6a38b58bfc111899")
        self.assertEqual((initial["sequence"], len(initial["assets"])), (1, 7))
        self.assertEqual(initial["source"]["tagObject"], "")
        receipt = {a["name"]: a for a in initial["assets"]}["release-build.json"]["sha256"]
        self.assertEqual(initial["source"]["bundleSha256"], receipt)
        self.assertEqual({a["name"]: a["value"] for a in initial["annotations"]}["build-receipt-sha256"], receipt)
        self.assertEqual((second["sequence"], len(second["assets"]), second["previousETag"]), (2, 7, record_etag(initial)))
        annotations = {item["name"]: item["value"] for item in second["annotations"]}
        self.assertEqual(annotations["cas-probe"], "sequence-2")
        self.assertEqual(annotations["source-tree"], self.tree)

    def test_generic_client_exposes_raw_empirical_stale_request_transport(self):
        args = argument_parser().parse_args([
            "raw", "--ssh-host", "authority@example.invalid", "--ssh-key", "/key",
            "--known-hosts", "/hosts", "--request", "/request",
        ])
        self.assertEqual(args.command, "raw")

    def test_qualified_revision_adds_all_eight_assets_once(self):
        qualified = self.root / "qualified"
        qualified.mkdir(mode=0o700)
        for name in _profile(self.tag, True):
            source = self.build / name
            (qualified / name).write_bytes(source.read_bytes() if source.exists() else (name + "\n").encode())
        _, second = _candidate(self.tag, self.commit, self.tree, qualified, qualified_bundle=True)
        second_path = self.root / "sequence-two.json"
        from scripts.release_authority import canonical_record
        second_path.write_bytes(canonical_record(second))
        output = self.root / "sequence-three.json"
        args = type("Args", (), {
            "tag": self.tag, "commit": self.commit, "source_tree": self.tree,
            "bundle": str(qualified), "sequence_two": str(second_path), "output": str(output),
        })()
        command_qualified(args)
        third = parse_record(output.read_bytes())
        self.assertEqual((third["sequence"], len(third["assets"])), (3, 15))
        self.assertEqual(third["previousETag"], record_etag(second))
        self.assertEqual(third["assets"][:7], sorted(third["assets"][:7], key=lambda item: item["name"]))

        recovery_one = self.root / "recovery-one.json"
        recovery_two = self.root / "recovery-two.json"
        recovery_three = self.root / "recovery-three.json"
        recovery_args = type("Args", (), {
            "tag": self.tag, "commit": self.commit, "source_tree": self.tree,
            "bundle": str(qualified), "sequence_one_output": str(recovery_one),
            "sequence_two_output": str(recovery_two),
            "sequence_three_output": str(recovery_three),
        })()
        command_qualified_recovery(recovery_args)
        initial, _ = _candidate(self.tag, self.commit, self.tree, qualified, qualified_bundle=True)
        self.assertEqual(parse_record(recovery_one.read_bytes()), initial)
        self.assertEqual(recovery_two.read_bytes(), second_path.read_bytes())
        self.assertEqual(recovery_three.read_bytes(), output.read_bytes())

        recovery_four = self.root / "recovery-four.json"
        tagged_args = type("Args", (), {
            "sequence_three": str(recovery_three), "tag_object": "c" * 40,
            "output": str(recovery_four),
        })()
        command_tagged(tagged_args)
        fourth = parse_record(recovery_four.read_bytes())
        self.assertEqual(fourth["sequence"], 4)
        self.assertEqual(fourth["previousETag"], record_etag(third))
        self.assertEqual(fourth["source"]["tagObject"], "c" * 40)

        invalid_output = self.root / "invalid-successor.json"
        invalid_args = type("Args", (), {
            "tag": self.tag, "commit": self.commit, "source_tree": self.tree,
            "bundle": str(qualified), "sequence_two": str(output), "output": str(invalid_output),
        })()
        with self.assertRaises(SystemExit):
            command_qualified(invalid_args)


if __name__ == "__main__":
    unittest.main()
