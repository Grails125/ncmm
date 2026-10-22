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
        with mock.patch.object(self.script, "get_platform_info", return_value=("Windows", "x86_64", ".zip")), mock.patch.object(self.script, "download_file_with_fallback", side_effect=download):
            self.assertTrue(self.script.install_from_archive("v1.2.2-grails.2", [], self.binary.name, str(self.binary), str(config), str(self.version)))
        self.assertEqual(self.binary.read_bytes(), b"new executable")
        self.assertEqual(config.read_text(encoding="utf-8"), "user: preserved\n")
        self.assertEqual(self.version.read_text(encoding="utf-8"), "1.2.2-grails.2\n")
        self.assertFalse((self.root / "_temp_ncmm_update_").exists())

    def test_first_install_download_retains_mirror_fallback(self):
        self.network.side_effect = [urllib.error.URLError("offline"), io.BytesIO(b"archive bytes")]
        destination = self.root / "release.zip"
        self.assertTrue(self.script.download_file_with_fallback("https://github.com/Grails125/ncmm/releases/download/test/test.zip", str(destination)))
        self.assertEqual(self.network.call_count, 2)
        self.assertEqual(destination.read_bytes(), b"archive bytes")


if __name__ == "__main__":
    unittest.main()
