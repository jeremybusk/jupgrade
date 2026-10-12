#!/usr/bin/env python3
"""Exercise jupgrade on isolated copies of Maven repos; never edit the originals."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

IGNORED = {".git", "target", "build", ".gradle", "node_modules", ".jupgrade", ".idea"}


def snapshot(root):
    result = {}
    for directory, children, files in os.walk(root, followlinks=False):
        children[:] = sorted(c for c in children if c not in IGNORED)
        for name in sorted(files):
            path = Path(directory) / name
            if path.is_symlink():
                result[str(path.relative_to(root))] = "symlink:" + os.readlink(path)
            else:
                result[str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def exercise(binary, source, args):
    original = snapshot(source)
    report = {"repo": str(source), "java": args.java, "preset": args.preset}
    with tempfile.TemporaryDirectory(prefix="jupgrade-corpus-") as directory:
        work = Path(directory)
        repo = work / "repo"
        shutil.copytree(source, repo, symlinks=True, ignore=shutil.ignore_patterns(*IGNORED))

        def command(*parts, allow_blockers=False):
            result = subprocess.run(
                [str(binary), *map(str, parts)], capture_output=True, text=True,
                timeout=args.timeout,
            )
            if result.returncode and not allow_blockers:
                output = "\n".join(part.strip() for part in (result.stdout, result.stderr) if part.strip())
                raise RuntimeError(output[-20000:])
            return result

        def plan(path):
            result = command(
                "plan", "--repo", repo, "--out", path,
                "--java", args.java, "--preset", args.preset, allow_blockers=True,
            )
            if not path.exists():
                raise RuntimeError(result.stderr.strip())
            content = json.loads(path.read_text())
            blockers = [f for f in content["findings"] or [] if f["severity"] == "blocker"]
            if result.returncode and not blockers:
                raise RuntimeError(result.stderr.strip())
            return content, blockers

        before = snapshot(repo)
        first_path, second_path = work / "first.json", work / "second.json"
        first, blockers = plan(first_path)
        second, _ = plan(second_path)
        if first != second or snapshot(repo) != before:
            raise RuntimeError("planning was nondeterministic or changed inputs")
        report["files"] = len(first["files"])
        report["findings"] = first["findings"] or []
        if blockers:
            report["status"] = "blocked"
        else:
            command("apply", "--plan", first_path)
            applied = snapshot(repo)
            command("apply", "--plan", first_path)
            if snapshot(repo) != applied:
                raise RuntimeError("reapply changed files")
            repeat, blockers = plan(work / "repeat.json")
            if repeat["files"] or blockers:
                raise RuntimeError("migration did not converge")
            if args.verify:
                command("verify", "--plan", first_path, "--timeout", f"{args.timeout}s")
            command("rollback", "--plan", first_path)
            if snapshot(repo) != before:
                raise RuntimeError("rollback did not restore the original files")
            report["status"] = "passed"
            report["build_verified"] = args.verify
    if snapshot(source) != original:
        raise RuntimeError("original test repository changed")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repos", nargs="+", type=Path, help="repository roots containing pom.xml")
    parser.add_argument("--binary", type=Path, default=Path(__file__).resolve().parents[1] / "bin/jupgrade")
    parser.add_argument("--java", type=int, choices=(17, 21, 25), default=21)
    parser.add_argument("--preset", choices=("build", "common"), default="common")
    parser.add_argument("--verify", action="store_true", help="also run each repository's Maven build")
    parser.add_argument("--timeout", type=int, default=300, help="timeout in seconds per CLI command")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error("build the CLI first: go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    results = []
    for source in args.repos:
        source = source.resolve()
        try:
            results.append(exercise(binary, source, args))
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            results.append({"repo": str(source), "status": "failed", "error": str(error)})
    print(json.dumps(results, indent=2))
    return 0 if all(r["status"] == "passed" for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
