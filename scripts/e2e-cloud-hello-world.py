#!/usr/bin/env python3
"""Run six cloud client hello-world checks through a supplied s46 CLI binary.

The dry run reads local executables only. The real run requires an isolated,
already authenticated HOME and final production model grants.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys


MODELS = (
    ("devstral", "s46/devstral-small-2-24b"),
    ("deepseek", "s46/deepseek-v4-flash-0731"),
)
HARNESSES = ("pi", "claude-code", "codex")
EXECUTABLE = {"pi": "pi", "claude-code": "claude", "codex": "codex"}
TEAM = "@s46/engineering"
TIMEOUT_SECONDS = 1800


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def command(args, *, cwd, env, output):
    process = subprocess.Popen(
        args, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        stdout, stderr = process.communicate(timeout=TIMEOUT_SECONDS)
    except subprocess.TimeoutExpired:
        # s46 handles SIGTERM by cancelling and reaping its separate harness group.
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.communicate(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
        raise RuntimeError(f"command timed out: {args[0]}") from None
    output.with_suffix(".stdout").write_bytes(stdout)
    output.with_suffix(".stderr").write_bytes(stderr)
    if process.returncode:
        raise RuntimeError(f"command failed ({process.returncode}): {args[0]}; see {output}.stderr")
    return stdout


def production_env(home, bin_dir, tmp_dir):
    env = {name: os.environ[name] for name in (
        "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "USER", "LOGNAME", "SHELL",
    ) if name in os.environ}
    env.update({
        "HOME": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"),
        "XDG_CACHE_HOME": str(home / ".cache"),
        "CODEX_HOME": str(home / ".codex"),
        "TMPDIR": str(tmp_dir),
        "TERM": "dumb",
        "NO_COLOR": "1",
        "PATH": str(bin_dir) + os.pathsep + env.get("PATH", ""),
        "S46_SKIP_UPDATE_CHECK": "1",
    })
    return env


def run_case(cli, model_name, model_id, harness, home, root, env):
    case = root / model_name / harness
    case.mkdir(parents=True, mode=0o700)
    expected = f"hello from {harness} with {model_name}"
    body = f'print("{expected}")\n'.encode()
    task = (
        "Create hello.py in the current directory with exactly this one line, "
        "including its final newline. Use the file-writing tool, then stop. "
        "Do not edit other files:\n" + body.decode()
    )
    connected = json.loads(command(
        [str(cli), "connect", TEAM, f"--harness={harness}", f"--model={model_id}",
         "--mode=cloud", "--scope=user", "--no-input", "--json"],
        cwd=case, env=env, output=case / "connect",
    ))
    if (connected.get("releaseChannelAtConnect") != "production" or
            connected.get("productionCertifiedAtConnect") is not True or
            connected.get("productionAPIPathAtConnect") is not True or
            connected.get("endpoint") != "https://gateway.s46.dev" or
            connected.get("model") != model_id or connected.get("harness") != harness):
        raise RuntimeError(f"{model_name}/{harness}: production discovery is incomplete")
    raw = command(
        [str(cli), "run", task, f"--model={model_id}", "--non-interactive",
         "--no-input", "--json"],
        cwd=case, env=env, output=case / "run",
    )
    result = json.loads(raw)
    if result.get("model") != model_id or result.get("task") != task or not result.get("id"):
        raise RuntimeError(f"{model_name}/{harness}: s46 session attribution differs")
    generated = case / "hello.py"
    if generated.read_bytes() != body:
        raise RuntimeError(f"{model_name}/{harness}: hello.py differs from requested bytes")
    actual = command([sys.executable, str(generated)], cwd=case, env=env, output=case / "hello")
    if actual != (expected + "\n").encode():
        raise RuntimeError(f"{model_name}/{harness}: hello.py output differs")
    return {
        "model": model_id, "harness": harness, "sessionId": result["id"],
        "helloSha256": sha256(generated), "output": expected,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("dry-run", "run"))
    parser.add_argument("--cli", type=Path, required=True, help="absolute source-built or published s46 executable")
    parser.add_argument("--home", type=Path, required=True, help="absolute isolated HOME; authenticate it before run")
    parser.add_argument("--output", type=Path, required=True, help="absent absolute private result directory")
    args = parser.parse_args()
    if any(not path.is_absolute() for path in (args.cli, args.home, args.output)):
        parser.error("cli, home, and output paths must be absolute")
    if not args.cli.is_file() or not os.access(args.cli, os.X_OK) or args.output.exists():
        parser.error("CLI must be executable and output directory must be absent")
    os.umask(0o077)
    args.output.mkdir(mode=0o700)
    harnesses = {name: shutil.which(EXECUTABLE[name]) for name in HARNESSES}
    if any(path is None for path in harnesses.values()):
        raise RuntimeError(f"missing client executable: {harnesses}")
    plan = {
        "schema": "s46.cloud-six-hello-world.v1", "mode": args.mode,
        "claim": "client diagnostic only; no model certification or CLI release authority",
        "cliSha256": sha256(args.cli), "team": TEAM,
        "harnessExecutables": harnesses,
        "cases": [{"model": model, "harness": harness}
                  for _, model in MODELS for harness in HARNESSES],
    }
    (args.output / "plan.json").write_text(json.dumps(plan, indent=2) + "\n")
    if args.mode == "dry-run":
        print(args.output / "plan.json")
        return
    if not args.home.is_dir():
        raise RuntimeError("isolated HOME does not exist; authenticate it before run")
    bin_dir = args.output / "bin"
    bin_dir.mkdir(mode=0o700)
    cli = bin_dir / "s46"
    shutil.copyfile(args.cli, cli)
    cli.chmod(0o700)
    if sha256(cli) != plan["cliSha256"]:
        raise RuntimeError("CLI changed while being staged")
    tmp_dir = args.output / "tmp"
    tmp_dir.mkdir(mode=0o700)
    env = production_env(args.home, bin_dir, tmp_dir)
    help_text = command([str(cli), "run", "--help"], cwd=args.output, env=env, output=args.output / "run-help")
    if b"--non-interactive" not in help_text:
        raise RuntimeError("supplied CLI lacks non-interactive harness support")
    command([str(cli), "whoami", "--no-input", "--json"], cwd=args.output, env=env, output=args.output / "whoami")
    results = [run_case(cli, name, model, harness, args.home, args.output, env)
               for name, model in MODELS for harness in HARNESSES]
    (args.output / "summary.json").write_text(json.dumps({
        "schema": "s46.cloud-six-hello-world-result.v1", "cliSha256": plan["cliSha256"],
        "cases": results, "claim": plan["claim"],
    }, indent=2) + "\n")
    print(args.output / "summary.json")


if __name__ == "__main__":
    main()
