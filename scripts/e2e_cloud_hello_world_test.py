import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("e2e-cloud-hello-world.py")
SPEC = importlib.util.spec_from_file_location("e2e_cloud_hello_world", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ProductionEnvironmentTest(unittest.TestCase):
    def test_provider_overrides_do_not_reach_child(self):
        hostile = {
            "ANTHROPIC_API_KEY": "hostile-secret",
            "ANTHROPIC_BASE_URL": "https://elsewhere.invalid",
            "OPENAI_API_KEY": "hostile-secret",
            "OPENAI_BASE_URL": "https://elsewhere.invalid",
            "PI_API_KEY": "hostile-secret",
            "CODEX_HOME": "/tmp/hostile-codex",
            "S46_API_BASE_URL": "https://elsewhere.invalid",
            "NODE_OPTIONS": "--require=/tmp/hostile.js",
        }
        with tempfile.TemporaryDirectory() as root, patch.dict(os.environ, hostile):
            base = Path(root)
            env = MODULE.production_env(base / "home", base / "bin", base / "tmp")
            output = subprocess.check_output(
                [sys.executable, "-c", "import json,os; print(json.dumps(sorted(os.environ)))"],
                env=env, text=True,
            )
            names = set(json.loads(output))
            blocked = set(hostile) - {"CODEX_HOME"}
            self.assertFalse(names.intersection(blocked), names.intersection(blocked))
            self.assertEqual(env["HOME"], str(base / "home"))
            self.assertEqual(env["CODEX_HOME"], str(base / "home" / ".codex"))
            self.assertTrue(env["PATH"].startswith(str(base / "bin") + os.pathsep))

    def test_explicit_file_keyring_stays_in_isolated_home(self):
        with tempfile.TemporaryDirectory() as root:
            home = Path(root) / "home"
            home.mkdir()
            keyring = home / ".local" / "share" / "s46" / "keyring.json"
            with patch.dict(os.environ, {
                "S46_KEYRING_BACKEND": "file", "S46_KEYRING_FILE": str(keyring),
            }):
                env = MODULE.production_env(home, Path(root) / "bin", Path(root) / "tmp")
            self.assertEqual(env["S46_KEYRING_BACKEND"], "file")
            self.assertEqual(env["S46_KEYRING_FILE"], str(keyring))

    def test_file_keyring_outside_home_or_missing_pair_is_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            base = Path(root)
            home = base / "home"
            home.mkdir()
            (home / "escape").symlink_to(base, target_is_directory=True)
            for values in (
                {"S46_KEYRING_BACKEND": "file", "S46_KEYRING_FILE": str(base / "keyring.json")},
                {"S46_KEYRING_BACKEND": "file", "S46_KEYRING_FILE": str(home / "escape" / "keyring.json")},
                {"S46_KEYRING_BACKEND": "file", "S46_KEYRING_FILE": "keyring.json"},
                {"S46_KEYRING_BACKEND": "file"},
                {"S46_KEYRING_FILE": str(home / "keyring.json")},
            ):
                with self.subTest(values=values), patch.dict(os.environ, values, clear=True):
                    with self.assertRaises(RuntimeError):
                        MODULE.production_env(home, base / "bin", base / "tmp")


if __name__ == "__main__":
    unittest.main()
