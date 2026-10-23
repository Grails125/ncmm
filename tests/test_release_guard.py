import json
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path


GUARD = Path(__file__).resolve().parents[1] / ".github" / "scripts" / "release_guard.py"
SPEC = importlib.util.spec_from_file_location("release_guard", GUARD)
release_guard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release_guard)


class ReleaseGuardTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.remote = self.root / "remote.git"
        self.repo = self.root / "source"
        self.repo.mkdir()
        self.git("init", "--bare", str(self.remote), cwd=self.root)
        self.git("init")
        self.git("config", "user.name", "Release test")
        self.git("config", "user.email", "release-test@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "tag.gpgsign", "false")
        (self.repo / "VERSION").write_text("1.2.2-grails.1\n", encoding="utf-8")
        self.git("add", "VERSION")
        self.git("commit", "-m", "first")
        self.previous = self.git("rev-parse", "HEAD")
        (self.repo / "proof.txt").write_text("second source\n", encoding="utf-8")
        self.git("add", "proof.txt")
        self.git("commit", "-m", "second")
        self.current = self.git("rev-parse", "HEAD")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("push", "origin", "HEAD:refs/heads/main")

    def git(self, *args, cwd=None):
        result = subprocess.run(["git", *args], cwd=cwd or self.repo, capture_output=True, text=True, check=True)
        return result.stdout.strip()

    def run_guard(self, version="auto", ref="refs/heads/main", source=None):
        environment = dict(os.environ, PYTHONIOENCODING="utf-8")
        return subprocess.run([sys.executable, str(GUARD), "--source-sha", source or self.current,
                               "--version", version, "--ref", ref], cwd=self.repo, capture_output=True, text=True, encoding="utf-8", env=environment)

    def push_tag(self, source, annotated=False):
        args = ["tag"]
        if annotated:
            args += ["-a", "-m", "existing release"]
        self.git(*args, "v1.2.2-grails.1", source)
        self.git("push", "origin", "refs/tags/v1.2.2-grails.1")

    def test_new_native_version_uses_source_version(self):
        result = self.run_guard()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"version": "1.2.2-grails.1", "commitSha": self.current, "tagExists": False})

    def test_existing_lightweight_or_annotated_tag_must_match_source(self):
        for annotated in (False, True):
            with self.subTest(annotated=annotated):
                self.push_tag(self.current, annotated)
                result = self.run_guard("v1.2.2-grails.1")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertTrue(json.loads(result.stdout)["tagExists"])
                self.git("push", "origin", ":refs/tags/v1.2.2-grails.1")
                self.git("tag", "-d", "v1.2.2-grails.1")

    def test_same_version_with_different_source_is_rejected_without_changing_tag(self):
        for annotated in (False, True):
            with self.subTest(annotated=annotated):
                self.push_tag(self.previous, annotated)
                before = self.git("ls-remote", "origin", "refs/tags/v1.2.2-grails.1", "refs/tags/v1.2.2-grails.1^{}")
                result = self.run_guard("1.2.2-grails.1")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("已存在", result.stderr)
                self.assertEqual(self.git("ls-remote", "origin", "refs/tags/v1.2.2-grails.1", "refs/tags/v1.2.2-grails.1^{}"), before)
                self.git("push", "origin", ":refs/tags/v1.2.2-grails.1")
                self.git("tag", "-d", "v1.2.2-grails.1")

    def test_tag_event_and_manual_version_are_validated(self):
        result = self.run_guard(ref="refs/tags/v1.2.2-grails.2")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["version"], "1.2.2-grails.2")
        for version in ("1.2.9", "1.2.2-grails.1/other", "$(echo unsafe)"):
            with self.subTest(version=version):
                self.assertNotEqual(self.run_guard(version).returncode, 0)
        self.assertNotEqual(self.run_guard(source=self.previous).returncode, 0)

    def test_remote_failure_does_not_allow_publication(self):
        self.git("remote", "set-url", "origin", str(self.root / "missing.git"))
        self.assertNotEqual(self.run_guard().returncode, 0)

    def test_native_version_without_revision_supports_manual_tag_and_source_defaults(self):
        for version in ("1.2.3-grails", "v1.2.3-grails"):
            with self.subTest(version=version):
                result = self.run_guard(version)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(result.stdout)["version"], "1.2.3-grails")
        result = self.run_guard(ref="refs/tags/v1.2.3-grails")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["version"], "1.2.3-grails")
        (self.repo / "VERSION").write_text("1.2.3-grails\n", encoding="utf-8")
        result = self.run_guard()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["version"], "1.2.3-grails")
        for version in ("1.2.3-grals", "1.2.3-grails/other", "1.2.3-grails+unknown", "1.2.3-grails."):
            with self.subTest(rejected=version):
                self.assertNotEqual(self.run_guard(version).returncode, 0)

    def reserve_tag(self, create, source=None):
        actual_run = subprocess.run
        requests = []

        def run(args, **kwargs):
            kwargs.setdefault("cwd", self.repo)
            if args[0] != "gh":
                return actual_run(args, **kwargs)
            requests.append(args)
            return create(args, kwargs, actual_run)

        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": "owner/ncmm", "GH_TOKEN": "private-test-token"}), mock.patch.object(release_guard.subprocess, "run", side_effect=run):
            # 预留测试读取隔离仓库的版本，不依赖运行测试的源码目录 VERSION。
            result = release_guard.validate_release(source or self.current, (self.repo / "VERSION").read_text(encoding="utf-8").strip(), "")
            reserved = release_guard.reserve_release_tag(result)
        self.assertTrue(reserved["tagExists"])
        self.assertNotIn("private-test-token", repr(requests))
        return requests

    def test_first_docker_only_publication_reserves_version_before_push(self):
        def create(args, kwargs, real_run):
            self.assertEqual(args[:6], ["gh", "api", "--hostname", "github.com", "--method", "POST"])
            self.assertIn("repos/owner/ncmm/git/refs", args)
            self.assertIn("ref=refs/tags/v1.2.2-grails.1", args)
            self.assertIn("sha=" + self.current, args)
            real_run(["git", "--git-dir", str(self.remote), "update-ref", "refs/tags/v1.2.2-grails.1", self.current], check=True, capture_output=True)
            return subprocess.CompletedProcess(args, 0)

        self.assertEqual(len(self.reserve_tag(create)), 1)
        # 后续即使只有镜像且 VERSION 没变，也不能拿另一提交覆盖该版本。
        (self.repo / "third.txt").write_text("different source\n", encoding="utf-8")
        self.git("add", "third.txt")
        self.git("commit", "-m", "third")
        self.assertNotEqual(self.run_guard(source=self.git("rev-parse", "HEAD")).returncode, 0)

    def test_same_commit_package_and_image_reservation_is_idempotent(self):
        def concurrent_create(args, kwargs, real_run):
            # 另一个产物在初次读取后先创建了同 SHA 标签，本次 POST 返回已存在。
            real_run(["git", "--git-dir", str(self.remote), "update-ref", "refs/tags/v1.2.2-grails.1", self.current], check=True, capture_output=True)
            return subprocess.CompletedProcess(args, 1, stderr="already exists")

        self.assertEqual(len(self.reserve_tag(concurrent_create)), 1)
        self.assertEqual(self.reserve_tag(lambda *args: self.fail("existing identical tag must not be created again")), [])

    def test_uncertain_creation_response_requires_remote_confirmation(self):
        def uncertain_create(args, kwargs, real_run):
            real_run(["git", "--git-dir", str(self.remote), "update-ref", "refs/tags/v1.2.2-grails.1", self.current], check=True, capture_output=True)
            raise subprocess.TimeoutExpired(args, 60)

        self.assertEqual(len(self.reserve_tag(uncertain_create)), 1)

    def test_reservation_rejects_concurrent_different_source(self):
        def conflicting_create(args, kwargs, real_run):
            real_run(["git", "--git-dir", str(self.remote), "update-ref", "refs/tags/v1.2.2-grails.1", self.previous], check=True, capture_output=True)
            return subprocess.CompletedProcess(args, 1, stderr="already exists")

        with self.assertRaisesRegex(ValueError, "其他源码"):
            self.reserve_tag(conflicting_create)
        self.assertEqual(self.git("--git-dir", str(self.remote), "rev-parse", "refs/tags/v1.2.2-grails.1"), self.previous)

    def test_failed_creation_or_remote_error_prevents_publication(self):
        with self.assertRaisesRegex(ValueError, "失败"):
            self.reserve_tag(lambda args, kwargs, real_run: subprocess.CompletedProcess(args, 1, stderr="permission denied"))

        def remote_failure(args, kwargs, real_run):
            self.git("remote", "set-url", "origin", str(self.root / "missing.git"))
            return subprocess.CompletedProcess(args, 1, stderr="unavailable")

        with self.assertRaises(subprocess.CalledProcessError):
            self.reserve_tag(remote_failure)


if __name__ == "__main__":
    unittest.main()
