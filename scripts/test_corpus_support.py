import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import MagicMock, Mock, patch

from corpus_support import fetch_checkout, git, load_manifest, smoke_test


class CorpusTests(unittest.TestCase):
    def test_fetch_pins_commits_and_preserves_existing_checkouts(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            source = root / "source"
            source.mkdir()
            git("init", cwd=source)
            git("config", "user.name", "Corpus test", cwd=source)
            git("config", "user.email", "corpus@example.invalid", cwd=source)
            (source / "pom.xml").write_text("<project/>")
            git("add", "pom.xml", cwd=source)
            git("commit", "-m", "fixture", cwd=source)
            commit = git("rev-parse", "HEAD", cwd=source)
            case = {"name": "fixture", "commit": commit, "url": "https://example.invalid/fixture.git"}
            checkout = fetch_checkout(case, root / "corpus", source)
            self.assertEqual(commit, git("rev-parse", "HEAD", cwd=checkout))
            (checkout / "pom.xml").write_text("user edit")
            with self.assertRaises(ValueError):
                fetch_checkout(case, root / "corpus", source)
            self.assertEqual("user edit", (checkout / "pom.xml").read_text())
            self.assertEqual("<project/>", (source / "pom.xml").read_text())

    def test_manifest_rejects_mutable_revisions_and_root_overrides(self):
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "manifest.json"
            case = {"name": "fixture", "commit": "a" * 40, "url": "https://example.invalid/fixture.git"}
            for fields in ({"commit": "main"}, {"root": "../outside"}, {"plan_args": ["--repo", "/outside"]}, {"startup": {"command": ["java"], "url": "https://remote.invalid/"}}):
                path.write_text(json.dumps({"schema": 1, "cases": [{**case, **fields}]}))
                with self.assertRaises(ValueError):
                    load_manifest(path)
            cases = load_manifest(Path(__file__).with_name("corpus.json"))
            self.assertEqual(4, len(cases))

    def test_startup_success_requires_http_response_and_cleans_process_group(self):
        with tempfile.TemporaryDirectory() as work:
            process = Mock()
            process.pid = 1234
            process.poll.return_value = None
            response = MagicMock()
            response.__enter__.return_value = response
            response.status = 200
            response.read.return_value = b"healthy app"
            with patch("corpus_support.socket.socket") as socket, patch("corpus_support.subprocess.Popen", return_value=process), patch("corpus_support.urlopen", return_value=response), patch("corpus_support.os.killpg") as kill:
                socket.return_value.__enter__.return_value.getsockname.return_value = ("127.0.0.1", 12345)
                result = smoke_test(Path(work), {"command": ["java"], "url": "http://127.0.0.1:{port}/", "body_contains": "healthy"}, 1)
                self.assertEqual(200, result["status"])
                self.assertIn("12345", result["url"])
                kill.assert_called_once()
                process.wait.assert_called_once()

    def test_corpus_restores_sources_and_does_not_claim_startup(self):
        script = Path(__file__).with_name("test-corpus.py")
        spec = importlib.util.spec_from_file_location("runner", script)
        runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runner)
        # Exercise the public CLI runner against a repository fixture.
        import subprocess
        binary = script.resolve().parents[1] / "bin/jupgrade"
        if not binary.exists():
            self.skipTest("build bin/jupgrade to exercise the corpus runner")
        result = subprocess.run(["python3", str(script), str(script.resolve().parents[1] / "testdata/gradle-kotlin"), "--binary", str(binary)], capture_output=True, text=True)
        self.assertEqual(0, result.returncode, result.stderr + result.stdout)
        report = json.loads(result.stdout)[0]
        self.assertEqual("passed", report["status"])
        self.assertFalse(report["build_verified"])
        self.assertFalse(report["startup_verified"])


if __name__ == "__main__":
    unittest.main()
