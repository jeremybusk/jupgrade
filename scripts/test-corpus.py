#!/usr/bin/env python3
"""Exercise jupgrade on isolated copies of Maven/Gradle repos; never edit the originals."""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from corpus_support import application_environment, fetch_checkout, git, load_manifest, relative_path, smoke_test

IGNORED = {".git", "target", "build", ".gradle", "node_modules", ".jupgrade", ".idea"}


def snapshot(root):
    result = {}
    for directory, children, files in os.walk(root, followlinks=False):
        children[:] = sorted(c for c in children if c not in IGNORED)
        for name in sorted(files):
            path = Path(directory) / name
            if name.startswith(".jupgrade-write-"):
                continue
            if path.is_symlink():
                result[str(path.relative_to(root))] = "symlink:" + os.readlink(path)
            else:
                result[str(path.relative_to(root))] = f"{hashlib.sha256(path.read_bytes()).hexdigest()}:{path.stat().st_mode & 0o777:o}"
    return result


def exercise(binary, source, args, case=None):
    original = snapshot(source)
    try:
        report = {"repo": str(source), "java": args.java, "preset": args.preset}
        with tempfile.TemporaryDirectory(prefix="jupgrade-corpus-") as directory:
            work = Path(directory)
            repo = work / "repo"
            ignored = IGNORED - {".git"} if args.keep_git_metadata else IGNORED
            if args.keep_git_metadata and not (source / ".git").is_dir():
                raise ValueError("keeping Git metadata requires a standalone checkout; use fetch-corpus")
            shutil.copytree(source, repo, symlinks=True, ignore=shutil.ignore_patterns(*ignored))
            for relative in (case or {}).get("exclude", []):
                target = repo / relative_path(relative)
                if target.is_symlink():
                    raise ValueError("corpus exclusion is a symlink")
                if target.is_dir():
                    shutil.rmtree(target)
                elif target.exists():
                    target.unlink()
            extra = list((case or {}).get("plan_args", []))
            checksums = (case or {}).get("gradle_checksums", {})
            properties = repo / "gradle/wrapper/gradle-wrapper.properties"
            if checksums and properties.is_file():
                archive = re.search(r"(?m)^distributionUrl=.*-(bin|all)\.zip\s*$", properties.read_text())
                if archive and archive[1] in checksums:
                    extra.extend(["--gradle-sha256", checksums[archive[1]]])
            if case:
                report.update(name=case["name"], commit=case["commit"], source_url=case["url"], projection=case.get("exclude", []))

            def command(*parts, allow_blockers=False, replay=False):
                env = os.environ.copy()
                if replay:
                    for key in ("BUILD", "JAVA", "PRESET", "POLICY", "DEPENDENCIES", "PLUGINS", "PARENTS", "SPRING_BOOT", "MAVEN", "MAVEN_SHA256", "GRADLE", "GRADLE_SHA256", "GRADLE_PLUGINS", "RECIPES", "RESOLVE_MAVEN"):
                        env.pop("JUPGRADE_" + key, None)
                result = subprocess.run(
                    [str(binary), *map(str, parts)], capture_output=True, text=True,
                    timeout=args.timeout + 15, env=env,
                )
                if result.returncode and not allow_blockers:
                    output = "\n".join(part.strip() for part in (result.stdout, result.stderr) if part.strip())
                    raise RuntimeError(output[-20000:])
                return result

            def plan(path, policy=None):
                options = ["--policy", policy] if policy else ["--java", args.java, "--preset", args.preset]
                if not policy:
                    if args.resolve_maven:
                        options.append("--resolve-maven")
                    for recipe in args.recipe:
                        options.extend(["--recipe", recipe])
                    if args.spring_boot:
                        options.extend(["--spring-boot", args.spring_boot])
                if not policy:
                    options.extend(extra)
                options.extend(["--timeout", f"{args.timeout}s"])
                result = command(
                    "plan", "--repo", repo, "--out", path, *options,
                    allow_blockers=True, replay=policy is not None,
                )
                if not path.exists():
                    raise RuntimeError(result.stderr.strip())
                content = json.loads(path.read_text())
                blockers = [f for f in content["findings"] or [] if f["severity"] == "blocker"]
                if result.returncode and not blockers:
                    raise RuntimeError(result.stderr.strip())
                return content, blockers

            if args.warm_cache:
                build_options=[]
                for i in range(0,len(extra),2):
                    if extra[i]=="--build":
                        build_options=["--build",extra[i+1]]
                command("discover", "--repo", repo, "--out", work / "effective-model.json", "--timeout", f"{args.timeout}s", *build_options)
                if args.resolve_maven:
                    pins = json.loads(command("policy", "--java", args.java, "--preset", args.preset, *extra).stdout)
                    targets = dict(pins.get("parents") or {})
                    for coordinate, version in (pins.get("dependencies") or {}).items():
                        if coordinate.endswith("-bom") or coordinate.endswith(":spring-boot-dependencies"):
                            targets[coordinate] = version
                    for coordinate, version in targets.items():
                        maven = repo / "mvnw"
                        executable = str(maven) if maven.is_file() else "mvn"
                        parts = [executable, "-B", "-ntp", "org.apache.maven.plugins:maven-help-plugin:3.5.1:effective-pom", "-Dartifact=" + coordinate + ":" + version, "-Doutput=" + str(work / "target-model.xml")]
                        if os.environ.get("JUPGRADE_MAVEN_REPO"):
                            parts.append("-Dmaven.repo.local=" + str(Path(os.environ["JUPGRADE_MAVEN_REPO"]).resolve()))
                        if os.environ.get("JUPGRADE_MAVEN_SETTINGS"):
                            parts.extend(["--settings", str(Path(os.environ["JUPGRADE_MAVEN_SETTINGS"]).resolve())])
                        if os.environ.get("JUPGRADE_OFFLINE", "").lower() in ("1", "true"):
                            parts.append("-o")
                        result = subprocess.run(parts, cwd=repo, env=application_environment(), capture_output=True, text=True, timeout=args.timeout)
                        if result.returncode:
                            raise RuntimeError((result.stdout + result.stderr)[-20000:])
            before = snapshot(repo)
            first_path, second_path = work / "first.json", work / "second.json"
            first, blockers = plan(first_path)
            saved_policy = work / "policy.json"
            saved_policy.write_text(json.dumps(first["policy"]))
            second, _ = plan(second_path, saved_policy)
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
                repeat, blockers = plan(work / "repeat.json", saved_policy)
                if repeat["files"] or blockers:
                    raise RuntimeError("migration did not converge")
                if args.verify:
                    command("verify", "--plan", first_path, "--timeout", f"{args.timeout}s")
                report["startup_verified"] = False
                if args.startup and (case or {}).get("startup"):
                    report["startup_probe"] = smoke_test(repo, case["startup"], args.timeout)
                    report["startup_verified"] = True
                elif args.startup:
                    report["startup_not_applicable"] = True
                if snapshot(repo) != applied:
                    raise RuntimeError("verification/startup changed repository inputs")
                command("rollback", "--plan", first_path)
                if snapshot(repo) != before:
                    raise RuntimeError("rollback did not restore the original files")
                report["status"] = "passed"
                report["build_verified"] = args.verify
    finally:
        if snapshot(source) != original:
            raise RuntimeError("original test repository changed")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("repos", nargs="*", type=Path, help="Maven or Gradle repository roots")
    parser.add_argument("--binary", type=Path, default=Path(__file__).resolve().parents[1] / "bin/jupgrade")
    parser.add_argument("--java", type=int, choices=(17, 21, 25), default=21)
    parser.add_argument("--preset", choices=("build", "common"), default="common")
    parser.add_argument("--verify", action="store_true", help="also run each repository's build and tests")
    parser.add_argument("--resolve-maven", action="store_true", help="capture cached parent/BOM models")
    parser.add_argument("--recipe", action="append", default=[], help="pinned source recipe alias; repeatable")
    parser.add_argument("--spring-boot", help="exact Spring Boot target for framework recipes")
    parser.add_argument("--timeout", type=int, default=300, help="timeout in seconds per CLI command")
    parser.add_argument("--manifest", type=Path, help="pinned real-project corpus manifest")
    parser.add_argument("--case", action="append", default=[], help="manifest case name; repeatable")
    parser.add_argument("--checkout-dir", type=Path, default=Path(__file__).resolve().parents[2] / "testrepos/pinned")
    parser.add_argument("--fetch", action="store_true", help="fetch missing immutable checkouts")
    parser.add_argument("--keep-git-metadata", action="store_true", help="copy standalone Git metadata for build plugins")
    parser.add_argument("--warm-cache", action="store_true", help="evaluate original models and resolve selected target BOMs/parents")
    parser.add_argument("--startup", action="store_true", help="run manifest HTTP startup checks after verification")
    args = parser.parse_args()
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error("build the CLI first: go build -buildvcs=false -o bin/jupgrade ./cmd/jupgrade")
    if not args.repos and not args.manifest:
        parser.error("provide repository roots or --manifest")
    if args.startup and not args.verify:
        parser.error("--startup requires --verify")
    if args.case and not args.manifest:
        parser.error("--case requires --manifest")
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    results = []
    tasks = [(source, args, None) for source in args.repos]
    if args.manifest:
        try:
            cases = load_manifest(args.manifest, args.case)
        except (ValueError, OSError) as error:
            parser.error(str(error))
        for case in cases:
            try:
                checkout = args.checkout_dir.resolve() / case["name"]
                if args.fetch:
                    checkout = fetch_checkout(case, args.checkout_dir)
                if checkout.is_symlink() or git("rev-parse", "HEAD", cwd=checkout) != case["commit"] or git("status", "--porcelain", cwd=checkout):
                    raise ValueError("checkout must be clean at the pinned commit")
                selected = argparse.Namespace(**vars(args))
                selected.java = case.get("java", 21)
                selected.preset = case.get("preset", "common")
                selected.resolve_maven = case.get("resolve_maven", False)
                selected.recipe = []
                selected.spring_boot = None
                source = (checkout / relative_path(case.get("root", "."), allow_root=True)).resolve()
                if not source.is_relative_to(checkout):
                    raise ValueError("corpus root escapes checkout")
                selected.keep_git_metadata = (source / ".git").is_dir()
                tasks.append((source, selected, case))
            except (ValueError, OSError, RuntimeError, subprocess.SubprocessError) as error:
                results.append({"name": case["name"], "status": "failed", "error": str(error)})
    for source, selected, case in tasks:
        source = source.resolve()
        try:
            results.append(exercise(binary, source, selected, case))
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            results.append({"repo": str(source), "status": "failed", "error": str(error)})
    print(json.dumps(results, indent=2))
    return 0 if all(r["status"] == "passed" for r in results) else 1


if __name__ == "__main__":
    sys.exit(main())
