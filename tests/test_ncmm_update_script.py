import contextlib
import importlib.util
import io
import subprocess
import tempfile
import unittest
import urllib.error
import zipfile
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "ncmm-update.py"


def load_script():
    spec = importlib.util.spec_from_file_location("ncmm_update_script", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class UpdateScriptTests(unittest.TestCase):
    def setUp(self):
        with contextlib.redirect_stdout(io.StringIO()):
            self.script = load_script()
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.binary = self.root / "ncmm.exe"
        self.backup = self.root / "ncmm.exe.old"
        self.version = self.root / "VERSION"
        self.version.write_text("1.2.2-grails.1\n", encoding="utf-8")
        self.output = io.StringIO()
        self.enterContext(contextlib.redirect_stdout(self.output))
        self.enterContext(mock.patch.multiple(self.script, current_dir=str(self.root), target_dir=str(self.root)))
        self.enterContext(mock.patch.object(self.script.platform, "system", return_value="Windows"))
        self.kill = self.enterContext(mock.patch.object(self.script.os, "kill", side_effect=AssertionError("must not kill processes")))
        self.network = self.enterContext(mock.patch.object(self.script.urllib.request, "urlopen", side_effect=AssertionError("unexpected network access")))

    def completed(self, code=0, output=""):
        return subprocess.CompletedProcess([], code, stdout=output)

    def run_installed(self, update_code=0, final_version="1.2.2-grails.2"):
        self.binary.write_bytes(b"existing executable")
        self.backup.write_bytes(b"recovery copy")
        responses = [self.completed(output="Version: 1.2.2-grails.1\n"), self.completed(update_code)]
        if update_code == 0:
            responses.append(self.completed(output=f"Version: {final_version}\n"))
        process = self.enterContext(mock.patch.object(self.script.subprocess, "run", side_effect=responses))
        return process

    def test_import_has_no_output_process_network_or_file_mutations(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(subprocess, "run") as process, mock.patch("os.remove") as remove:
            load_script()
        self.assertEqual(output.getvalue(), "")
        process.assert_not_called()
        self.network.assert_not_called()
        self.kill.assert_not_called()
        remove.assert_not_called()

    def test_cli_failure_is_not_retried_or_reinterpreted_as_file_lock(self):
        for code in (1, 2, 7, 124):
            with self.subTest(code=code), mock.patch.object(self.script.subprocess, "run", return_value=self.completed(code)) as process:
                self.assertEqual(self.script.update_with_ncmm(str(self.binary), self.binary.name), code)
                process.assert_called_once_with([str(self.binary), "update", "--apply"], cwd=str(self.root), timeout=600, stdout=None, stderr=None, text=True)
        self.assertIn("状态码 7", self.output.getvalue())
        self.assertNotIn("可能是运行中的", self.output.getvalue())
        self.kill.assert_not_called()

    def test_timeout_and_spawn_error_preserve_failure_without_retry(self):
        cases = [(subprocess.TimeoutExpired("ncmm", 600), 124), (PermissionError("denied"), 1)]
        for error, expected in cases:
            with self.subTest(error=type(error).__name__), mock.patch.object(self.script.subprocess, "run", side_effect=error) as process:
                self.assertEqual(self.script.update_with_ncmm(str(self.binary), self.binary.name), expected)
                process.assert_called_once()
        self.assertIn("denied", self.output.getvalue())

    def test_binary_version_does_not_trim_an_untrusted_suffix(self):
        for version in ("1.2.2-grails.2+unknown", "1.2.2-grails.2/other"):
            with self.subTest(version=version), mock.patch.object(self.script.subprocess, "run", return_value=self.completed(output=f"Version: {version}\n")):
                self.assertEqual(self.script.query_binary_version(str(self.binary)), version)

    def test_main_propagates_cli_exit_and_keeps_version_and_recovery_backup(self):
        process = self.run_installed(update_code=7)
        with self.assertRaises(SystemExit) as result:
            self.script.main()
        self.assertEqual(result.exception.code, 7)
        self.assertEqual(process.call_count, 2)
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")
        self.assertEqual(self.backup.read_bytes(), b"recovery copy")
        self.network.assert_not_called()

    def test_success_and_already_latest_keep_recovery_backup(self):
        for version in ("1.2.2-grails.1", "1.2.2-grails.2"):
            with self.subTest(version=version):
                process = self.run_installed(final_version=version)
                self.script.main()
                self.assertEqual(process.call_count, 3)
                self.assertEqual(self.version.read_text(encoding="utf-8"), version + "\n")
                self.assertEqual(self.backup.read_bytes(), b"recovery copy")

    def test_unverified_result_does_not_record_success(self):
        for version in ("", "1.2.3"):
            with self.subTest(version=version):
                self.version.write_text("1.2.2-grails.1\n", encoding="utf-8")
                self.run_installed(final_version=version)
                with self.assertRaises(SystemExit) as result:
                    self.script.main()
                self.assertEqual(result.exception.code, 1)
                self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")
                self.assertEqual(self.backup.read_bytes(), b"recovery copy")
        self.assertNotIn("[SUCCESS]", self.output.getvalue())

    def test_missing_binary_with_recovery_artifact_never_installs(self):
        for kind in ("file", "empty", "directory"):
            directory = self.root / kind
            directory.mkdir()
            backup = directory / "ncmm.exe.old"
            if kind == "directory":
                backup.mkdir()
            else:
                backup.write_bytes(b"recovery copy" if kind == "file" else b"")
            with self.subTest(kind=kind), mock.patch.object(self.script, "target_dir", str(directory)), mock.patch.object(self.script, "get_latest_release") as release, mock.patch.object(self.script, "install_from_archive") as install, mock.patch.object(self.script.subprocess, "run") as process:
                with self.assertRaises(SystemExit) as result:
                    self.script.main()
                self.assertEqual(result.exception.code, 1)
                release.assert_not_called()
                install.assert_not_called()
                process.assert_not_called()
                self.assertTrue(backup.exists())
                self.assertFalse((directory / "ncmm.exe").exists())
        self.assertIn(".old", self.output.getvalue())

    def test_dangling_backup_link_does_not_fall_through_to_install(self):
        # 模拟无有效目标的软链接；Windows 测试环境不要求创建软链接的管理员权限。
        self.assertFalse(self.backup.exists())
        with mock.patch.object(self.script.os.path, "lexists", side_effect=lambda path: str(path) == str(self.backup)), mock.patch.object(self.script, "get_latest_release") as release:
            with self.assertRaises(SystemExit) as result:
                self.script.main()
            self.assertEqual(result.exception.code, 1)
            release.assert_not_called()

    def test_first_install_still_uses_native_release(self):
        for ok in (True, False):
            with self.subTest(ok=ok), mock.patch.object(self.script, "get_latest_release", return_value=("v1.2.2-grails.2", [])), mock.patch.object(self.script, "install_from_archive", return_value=ok) as install:
                with self.assertRaises(SystemExit) as result:
                    self.script.main()
                self.assertEqual(result.exception.code, 0 if ok else 1)
                install.assert_called_once_with("v1.2.2-grails.2", [], "ncmm.exe", str(self.binary), str(self.root / "config.yaml"), str(self.version))

    def test_install_archive_keeps_existing_configuration(self):
        config = self.root / "config.yaml"
        config.write_text("user: preserved\n", encoding="utf-8")
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as package:
            package.writestr("ncmm.exe", b"new executable")
            package.writestr("config.yaml", "user: default\n")
        def download(url, destination, **kwargs):
            Path(destination).write_bytes(archive.getvalue())
            return True
        with mock.patch.object(self.script, "get_platform_info", return_value=("Windows", "x86_64", ".zip")), mock.patch.object(self.script, "download_file_with_fallback", side_effect=download), mock.patch.object(self.script, "query_binary_version", return_value="1.2.2-grails.2") as version:
            self.assertTrue(self.script.install_from_archive("v1.2.2-grails.2", [], self.binary.name, str(self.binary), str(config), str(self.version)))
        self.assertNotEqual(version.call_args.args[0], str(self.binary))
        self.assertEqual(self.binary.read_bytes(), b"new executable")
        self.assertEqual(config.read_text(encoding="utf-8"), "user: preserved\n")
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.2\n")
        self.assertFalse((self.root / "_temp_ncmm_update_").exists())

    def install_archive(self, tag="v1.2.2-grails.2", assets=None, reported_version="1.2.2-grails.2", include_binary=True, existing_config=True):
        config = self.root / "config.yaml"
        if existing_config:
            config.write_text("user: preserved\n", encoding="utf-8")
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as package:
            if include_binary:
                package.writestr("bundle/ncmm.exe", b"candidate executable")
            package.writestr("bundle/config.yaml", "user: default\n")

        def download(url, destination, **kwargs):
            Path(destination).write_bytes(archive.getvalue())
            return True

        with mock.patch.object(self.script, "get_platform_info", return_value=("Windows", "x86_64", ".zip")), mock.patch.object(self.script, "download_file_with_fallback", side_effect=download) as fetch, mock.patch.object(self.script, "query_binary_version", return_value=reported_version) as version:
            result = self.script.install_from_archive(tag, assets or [], self.binary.name, str(self.binary), str(config), str(self.version))
        return result, fetch, version

    def test_first_install_rejects_untrusted_asset_urls_before_download(self):
        for url in (
            "https://example.com/ncmm_Windows_x86_64.zip",
            "https://github.com/3899/ncmm/releases/download/v1.2.2-grails.2/ncmm_Windows_x86_64.zip",
            "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.1/ncmm_Windows_x86_64.zip",
            "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.2/ncmm_Windows_x86_64.zip?redirect=1",
        ):
            with self.subTest(url=url):
                ok, fetch, version = self.install_archive(assets=[{"name": "ncmm_Windows_x86_64.zip", "browser_download_url": url}])
                self.assertFalse(ok)
                fetch.assert_not_called()
                version.assert_not_called()
                self.assertFalse(self.binary.exists())
                self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")

    def test_first_install_validates_native_tag_and_candidate_before_writing(self):
        for tag, version, include_binary in (
            ("v1.2.9", "1.2.9", True),
            ("v1.2.2-grails.2/other", "1.2.2-grails.2", True),
            ("v1.2.2-grails.2", None, True),
            ("v1.2.2-grails.2", "1.2.9", True),
            ("v1.2.2-grails.2", "1.2.2-grails.1", True),
            ("v1.2.2-grails.2", "1.2.2-grails.2", False),
        ):
            with self.subTest(tag=tag, version=version, binary=include_binary):
                ok, _, _ = self.install_archive(tag=tag, reported_version=version, include_binary=include_binary)
                self.assertFalse(ok)
                self.assertFalse(self.binary.exists())
                self.assertEqual((self.root / "config.yaml").read_text(encoding="utf-8"), "user: preserved\n")
                self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")
                self.assertEqual(list(self.root.glob(".ncmm-install-*")), [])

    def test_first_install_uses_only_exact_platform_asset_and_canonical_fallback(self):
        for assets in (
            [{"name": "ncmm_Windows_x86_64.zip", "browser_download_url": "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.2/ncmm_Windows_x86_64.zip"}],
            [{"name": "untrusted_ncmm_Windows_x86_64.zip", "browser_download_url": "https://example.com/untrusted.zip"}],
        ):
            with self.subTest(assets=assets):
                ok, fetch, _ = self.install_archive(assets=assets)
                self.assertTrue(ok)
                self.assertEqual(fetch.call_args.args[0], "https://github.com/Grails125/ncmm/releases/download/v1.2.2-grails.2/ncmm_Windows_x86_64.zip")
                self.assertEqual(self.binary.read_bytes(), b"candidate executable")
                self.binary.unlink()

    def test_first_install_never_replaces_existing_program(self):
        self.binary.write_bytes(b"already installed")
        ok, fetch, _ = self.install_archive()
        self.assertFalse(ok)
        fetch.assert_not_called()
        self.assertEqual(self.binary.read_bytes(), b"already installed")

    def test_first_install_rolls_back_new_files_if_version_record_fails(self):
        with mock.patch.object(self.script.os, "replace", side_effect=PermissionError("read-only VERSION")):
            ok, _, _ = self.install_archive()
        self.assertFalse(ok)
        self.assertFalse(self.binary.exists())
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")
        self.assertEqual((self.root / "config.yaml").read_text(encoding="utf-8"), "user: preserved\n")

    def test_first_install_creates_default_config_and_rolls_it_back_on_failure(self):
        with mock.patch.object(self.script.os, "replace", side_effect=PermissionError("read-only VERSION")):
            ok, _, _ = self.install_archive(existing_config=False)
        self.assertFalse(ok)
        self.assertFalse(self.binary.exists())
        self.assertFalse((self.root / "config.yaml").exists())
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")
        ok, _, _ = self.install_archive(existing_config=False)
        self.assertTrue(ok)
        self.assertEqual((self.root / "config.yaml").read_text(encoding="utf-8"), "user: default\n")

    def test_first_install_cleanup_failure_does_not_undo_committed_install(self):
        temporary_directory = self.script.tempfile.TemporaryDirectory

        class CleanupFailure:
            def __init__(self, *args, **kwargs):
                self.directory = temporary_directory(*args, **kwargs)

            def __enter__(self):
                return self.directory.__enter__()

            def __exit__(self, *args):
                self.directory.__exit__(*args)
                raise PermissionError("simulated cleanup failure")

        with mock.patch.object(self.script.tempfile, "TemporaryDirectory", CleanupFailure):
            ok, _, _ = self.install_archive()
        self.assertTrue(ok)
        self.assertEqual(self.binary.read_bytes(), b"candidate executable")
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.2\n")
        self.assertIn("[WARNING]", self.output.getvalue())
        self.assertNotIn("[ERROR]", self.output.getvalue())

    def test_first_install_rollback_preserves_program_replaced_by_another_process(self):
        link, replace = self.script.os.link, self.script.os.replace

        def concurrent_link(source, destination):
            link(source, destination)
            if destination == str(self.binary):
                foreign = self.root / "other-program"
                foreign.write_bytes(b"another process program")
                replace(foreign, destination)

        def replace_with_version_failure(source, destination):
            if destination == str(self.version):
                raise PermissionError("read-only VERSION")
            return replace(source, destination)

        with mock.patch.object(self.script.os, "link", side_effect=concurrent_link), mock.patch.object(self.script.os, "replace", side_effect=replace_with_version_failure):
            ok, _, _ = self.install_archive()
        self.assertFalse(ok)
        self.assertEqual(self.binary.read_bytes(), b"another process program")
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.1\n")

    def test_first_install_extraction_rejects_duplicate_programs_and_links(self):
        for kind in ("duplicate", "symlink", "oversize"):
            with self.subTest(kind=kind):
                archive = self.root / "untrusted.zip"
                with zipfile.ZipFile(archive, "w") as package:
                    package.writestr("../unrelated.txt", "must not escape")
                    if kind == "duplicate":
                        package.writestr("first/ncmm.exe", "one")
                        package.writestr("second/ncmm.exe", "two")
                    elif kind == "symlink":
                        entry = zipfile.ZipInfo("ncmm.exe")
                        entry.external_attr = 0o120777 << 16
                        package.writestr(entry, "../other.exe")
                    else:
                        package.writestr("ncmm.exe", "larger than limit")
                with tempfile.TemporaryDirectory(dir=self.root) as directory, mock.patch.object(self.script, "MAX_INSTALL_FILE_BYTES", 5):
                    with self.assertRaises(ValueError):
                        self.script.extract_install_files(str(archive), ".zip", "ncmm.exe", directory)
                self.assertFalse((self.root / "unrelated.txt").exists())

    def test_first_install_aborts_when_no_native_release_exists(self):
        for tag in (None, "v1.2.9"):
            with self.subTest(tag=tag), mock.patch.object(self.script, "get_latest_release", return_value=(tag, [])), mock.patch.object(self.script, "install_from_archive") as install:
                with self.assertRaises(SystemExit) as result:
                    self.script.main()
                self.assertEqual(result.exception.code, 1)
                install.assert_not_called()
                self.assertFalse(self.binary.exists())

    def test_first_install_download_retains_mirror_fallback(self):
        self.network.side_effect = [urllib.error.URLError("offline"), io.BytesIO(b"archive bytes")]
        destination = self.root / "release.zip"
        self.assertTrue(self.script.download_file_with_fallback("https://github.com/Grails125/ncmm/releases/download/test/test.zip", str(destination)))
        self.assertEqual(self.network.call_count, 2)
        self.assertEqual(destination.read_bytes(), b"archive bytes")


if __name__ == "__main__":
    unittest.main()
