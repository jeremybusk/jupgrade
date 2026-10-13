"""Pinned checkout and application smoke-test support for the corpus runner."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import tempfile
import time
from urllib.error import URLError, HTTPError
from urllib.parse import urlsplit
from urllib.request import urlopen

PLAN_FLAGS = {"--dependency", "--plugin", "--parent", "--spring-boot", "--gradle", "--gradle-sha256", "--maven", "--maven-sha256", "--recipe"}


def relative_path(value, allow_root=False):
    if not isinstance(value, str):
        raise ValueError("corpus paths must be strings")
    path = Path(value)
    if not isinstance(value, str) or not value or path.is_absolute() or ".." in path.parts or str(path) != value or (value == "." and not allow_root):
        raise ValueError(f"unsafe corpus path: {value!r}")
    return path


def load_manifest(path, names=()):
    content = json.loads(Path(path).read_text())
    if content.get("schema") != 1 or not isinstance(content.get("cases"), list):
        raise ValueError("corpus manifest requires schema 1 and cases")
    cases, seen = [], set()
    for case in content["cases"]:
        if not isinstance(case, dict):
            raise ValueError("corpus cases must be objects")
        name = case.get("name", "")
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", name) or name in seen:
            raise ValueError(f"invalid or duplicate corpus case: {name}")
        seen.add(name)
        if not re.fullmatch(r"[a-f0-9]{40}", case.get("commit", "")):
            raise ValueError(f"{name}: commit must be a complete immutable SHA")
        url = urlsplit(case.get("url", ""))
        if url.scheme != "https" or not url.hostname or url.username or url.password:
            raise ValueError(f"{name}: source must be a public HTTPS Git URL")
        relative_path(case.get("root", "."), allow_root=True)
        for item in case.get("exclude", []):
            relative_path(item)
            if item == ".git" or item.startswith(".git/"):
                raise ValueError("Git metadata cannot be a corpus projection exclusion")
        if case.get("java", 21) not in (17, 21, 25) or case.get("preset", "common") not in ("common", "build"):
            raise ValueError(f"{name}: unsupported target/preset")
        flags = case.get("plan_args", [])
        if not isinstance(flags, list) or len(flags) % 2:
            raise ValueError(f"{name}: plan_args must contain option/value pairs")
        for i in range(0, len(flags), 2):
            if flags[i] not in PLAN_FLAGS or not isinstance(flags[i + 1], str) or not flags[i + 1] or flags[i + 1].startswith("-"):
                raise ValueError(f"{name}: unsupported corpus plan option")
        startup = case.get("startup")
        if startup:
            if not isinstance(startup, dict):
                raise ValueError(f"{name}: startup must be an object")
            if not isinstance(startup.get("command"), list) or not startup["command"] or any(not isinstance(x, str) or not x for x in startup["command"]):
                raise ValueError(f"{name}: startup command must be an argv array")
            relative_path(startup.get("cwd", "."), allow_root=True)
            if startup.get("artifact"):
                relative_path(startup["artifact"])
            if startup.get("prepare") and (not isinstance(startup["prepare"], list) or any(not isinstance(x,str) or not x for x in startup["prepare"])):
                raise ValueError(f"{name}: prepare command must be an argv array")
            url = urlsplit(startup.get("url", "").replace("{port}", "8080"))
            if url.scheme != "http" or url.hostname not in ("localhost", "127.0.0.1", "::1"):
                raise ValueError(f"{name}: startup probe must address the local application")
            if "port" in startup and not 1 <= startup["port"] <= 65535:
                raise ValueError(f"{name}: invalid startup port")
        checksums=case.get("gradle_checksums",{})
        if not isinstance(checksums,dict) or any(key not in ("bin","all") or not re.fullmatch(r"[a-f0-9]{64}",value) for key,value in checksums.items()):
            raise ValueError(f"{name}: invalid Gradle distribution checksums")
        if not names or name in names:
            cases.append(case)
    if set(names) - seen:
        raise ValueError(f"unknown corpus cases: {sorted(set(names) - seen)}")
    return cases


def git(*args, cwd=None, timeout=120):
    result = subprocess.run(["git", *map(str, args)], cwd=cwd, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(result.stderr.strip()[-20000:])
    return result.stdout.strip()


def fetch_checkout(case, destination, local_source=None):
    destination = Path(destination).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    checkout = destination / case["name"]
    if checkout.is_symlink():
        raise ValueError(f"checkout is a symlink: {checkout}")
    if checkout.exists():
        if git("rev-parse", "HEAD", cwd=checkout) != case["commit"] or git("status", "--porcelain", cwd=checkout):
            raise ValueError(f"existing checkout is dirty or at a different revision: {checkout}")
        return checkout
    source = str(Path(local_source).resolve()) if local_source else case["url"]
    with tempfile.TemporaryDirectory(prefix=f".fetch-{case['name']}-", dir=destination) as work:
        staging = Path(work) / "repo"
        staging.mkdir()
        git("init", cwd=staging)
        git("remote", "add", "origin", source, cwd=staging)
        git("fetch", "--depth=1", "origin", case["commit"], cwd=staging)
        git("checkout", "--detach", "FETCH_HEAD", cwd=staging)
        if git("rev-parse", "HEAD", cwd=staging) != case["commit"]:
            raise RuntimeError("fetched checkout does not match pinned commit")
        staging.rename(checkout)
    return checkout


def application_environment():
    env = os.environ.copy()
    if env.get("JUPGRADE_JAVA_HOME"):
        home = str(Path(env["JUPGRADE_JAVA_HOME"]).resolve())
        env["JAVA_HOME"] = home
        env["PATH"] = str(Path(home) / "bin") + os.pathsep + env.get("PATH", "")
    if env.get("JUPGRADE_GRADLE_USER_HOME"):
        env["GRADLE_USER_HOME"] = str(Path(env["JUPGRADE_GRADLE_USER_HOME"]).resolve())
    return env


def smoke_test(repo, config, timeout):
    # Reserve/check a free local port before launching the isolated application.
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", config.get("port", 0)))
        port = probe.getsockname()[1]
    env = application_environment()
    cache = str(Path(env.get("JUPGRADE_MAVEN_REPO", "~/.m2/repository")).expanduser().resolve())
    command = [x.replace("{port}", str(port)).replace("{maven_repo}", cache) for x in config["command"]]
    url = config["url"].replace("{port}", str(port))
    cwd = (repo / relative_path(config.get("cwd", "."), allow_root=True)).resolve()
    if not cwd.is_relative_to(repo.resolve()):
        raise ValueError("startup working directory escapes the repository")
    if config.get("prepare"):
        result=subprocess.run(config["prepare"],cwd=cwd,env=env,capture_output=True,text=True,timeout=timeout)
        if result.returncode:
            raise RuntimeError(f"startup packaging failed:\n{result.stdout[-10000:]}\n{result.stderr[-10000:]}")
    if config.get("artifact"):
        artifacts=[path for path in repo.glob(config["artifact"]) if path.is_file() and not path.name.endswith("-plain.jar")]
        if len(artifacts)!=1:
            raise ValueError("startup requires exactly one executable application artifact")
        artifact=artifacts[0].resolve()
        if not artifact.is_relative_to(repo.resolve()):
            raise ValueError("startup artifact escapes repository")
        command=[x.replace("{artifact}",str(artifact)) for x in command]
    with tempfile.TemporaryFile(mode="w+b") as log:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdout=log, stderr=log, start_new_session=True)
        try:
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError("application exited before its startup probe passed")
                try:
                    with urlopen(url, timeout=2) as response:
                        body = response.read(1024 * 1024).decode("utf-8", errors="replace")
                        if response.status == config.get("status", 200) and config.get("body_contains", "") in body:
                            if process.poll() is not None:
                                raise RuntimeError("application exited during startup probe")
                            return {"url": url, "status": response.status}
                except (URLError, HTTPError, TimeoutError):
                    pass
                time.sleep(0.2)
            raise RuntimeError("application startup probe timed out")
        except Exception as error:
            log.seek(0)
            raise RuntimeError(f"{error}\n{log.read().decode('utf-8', errors='replace')[-20000:]}") from error
        finally:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
