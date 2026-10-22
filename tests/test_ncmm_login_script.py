import contextlib
import importlib.util
import io
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "ncmm-login.py"


def load_script():
    spec = importlib.util.spec_from_file_location("ncmm_login_script", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class LoginScriptTests(unittest.TestCase):
    def setUp(self):
        self.output = io.StringIO()
        self.errors = io.StringIO()
        self.enterContext(contextlib.redirect_stdout(self.output))
        self.enterContext(contextlib.redirect_stderr(self.errors))
        self.enterContext(mock.patch.dict(os.environ, {}, clear=True))
        self.script = load_script()
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.binary = str(self.root / "ncmm")
        self.enterContext(mock.patch.object(self.script, "current_dir", str(self.root)))
        self.real_run = subprocess.run
        self.process = self.enterContext(mock.patch.object(self.script.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)))

    def assert_private(self, *values):
        logged = self.output.getvalue() + self.errors.getvalue()
        for value in values:
            self.assertNotIn(value, logged)

    def assert_login_calls(self, commands):
        self.assertEqual(self.process.call_args_list, [mock.call(
            command, cwd=str(self.root), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        ) for command in commands])

    def test_cookiecloud_preserves_arguments_without_logging_any_credentials_or_url(self):
        uuid = "private-cloud-uuid"
        password = "private-cloud-password"
        server = "https://private-user:private-server-pass@cloud.example.invalid/path?token=private-query-token"
        with mock.patch.dict(os.environ, {"NCMM_COOKIECLOUD_UUID": uuid, "NCMM_COOKIECLOUD_PASSWORD": password, "NCMM_COOKIECLOUD_SERVER": server}):
            self.assertTrue(self.script.login_by_cookiecloud(self.binary))
        self.assert_private(uuid, password, server, "private-user", "private-server-pass", "private-query-token")
        self.assert_login_calls([[self.binary, "login", "cookiecloud", "-s", server, "-u", uuid, "-p", password]])

    def test_cookiecloud_aliases_still_work_without_credentials_in_logs(self):
        with mock.patch.dict(os.environ, {"COOKIECLOUD_UUID": "private-alias-uuid", "COOKIECLOUD_PASS": "private-alias-pass"}):
            self.assertTrue(self.script.login_by_cookiecloud(self.binary))
        self.assert_private("private-alias-uuid", "private-alias-pass")
        self.assert_login_calls([[self.binary, "login", "cookiecloud", "-s", "http://127.0.0.1:8088", "-u", "private-alias-uuid", "-p", "private-alias-pass"]])

    def test_explicit_main_and_secondary_environment_cookies_keep_roles(self):
        main = "MUSIC_U=private-main-cookie; __csrf=private-main-csrf"
        first = "private-secondary-one"
        second = "MUSIC_U=private-secondary-two"
        with mock.patch.dict(os.environ, {"NCMM_MAIN_COOKIE": main, "NCMM_SECONDARY_COOKIE": first + "&" + second}):
            self.assertTrue(self.script.login_by_env_cookie(self.binary))
        self.assert_private("private-main-cookie", "private-main-csrf", first, "private-secondary-two")
        self.assert_login_calls([
            [self.binary, "login", "cookie", "-m", main],
            [self.binary, "login", "cookie", "MUSIC_U=" + first],
            [self.binary, "login", "cookie", second],
        ])

    def test_general_environment_cookies_keep_first_main_and_later_secondary(self):
        with mock.patch.dict(os.environ, {"NCMM_COOKIE": "private-first\nMUSIC_U=private-second"}):
            self.assertTrue(self.script.login_by_env_cookie(self.binary))
        self.assert_private("private-first", "private-second")
        self.assert_login_calls([
            [self.binary, "login", "cookie", "-m", "MUSIC_U=private-first"],
            [self.binary, "login", "cookie", "MUSIC_U=private-second"],
        ])

    def test_music_u_alias_and_secondary_only_keep_their_roles(self):
        for env, expected in (
            ({"MUSIC_U": "private-alias-cookie"}, [self.binary, "login", "cookie", "-m", "MUSIC_U=private-alias-cookie"]),
            ({"NCMM_SECONDARY_COOKIE": "private-secondary-cookie"}, [self.binary, "login", "cookie", "MUSIC_U=private-secondary-cookie"]),
        ):
            with self.subTest(env=list(env)), mock.patch.dict(os.environ, env, clear=True):
                self.process.reset_mock()
                self.assertTrue(self.script.login_by_env_cookie(self.binary))
                self.assert_login_calls([expected])
                self.assert_private(*env.values())

    def test_file_sources_keep_paths_and_roles_without_printing_full_commands(self):
        main = self.root / "private-main-file.txt"
        secondary = self.root / "private-secondary-file.txt"
        for path in (main, secondary):
            path.write_text("MUSIC_U=private-file-cookie", encoding="utf-8")
        cases = (
            ({"NCMM_MAIN_COOKIE_FILE": str(main), "NCMM_SECONDARY_COOKIE_FILE": str(secondary)}, [
                [self.binary, "login", "cookie", "-m", "-f", str(main), "-o", "private-main-file.json"],
                [self.binary, "login", "cookie", "-f", str(secondary), "-o", "private-secondary-file.json"],
            ]),
            ({"NCMM_COOKIE_FILE": str(main) + "," + str(secondary)}, [
                [self.binary, "login", "cookie", "-m", "-f", str(main), "-o", "private-main-file.json"],
                [self.binary, "login", "cookie", "-f", str(secondary), "-o", "private-secondary-file.json"],
            ]),
        )
        for env, commands in cases:
            with self.subTest(env=list(env)), mock.patch.dict(os.environ, env, clear=True):
                self.process.reset_mock()
                self.assertTrue(self.script.login_by_file(self.binary))
                self.assert_login_calls(commands)
                self.assert_private(str(main), str(secondary), "private-file-cookie")

    def test_default_file_import_stays_main(self):
        source = self.root / "cookie.txt"
        source.write_text("MUSIC_U=private-file-cookie", encoding="utf-8")
        self.assertTrue(self.script.login_by_file(self.binary))
        self.assert_login_calls([[self.binary, "login", "cookie", "-m", "-f", str(source), "-o", "cookie.json"]])
        self.assert_private("private-file-cookie", str(source))

    def test_failed_process_and_partial_import_preserve_success_rules(self):
        with mock.patch.dict(os.environ, {"NCMM_MAIN_COOKIE": "private-main", "NCMM_SECONDARY_COOKIE": "private-secondary"}):
            self.process.side_effect = [subprocess.CompletedProcess([], 7), subprocess.CompletedProcess([], 0)]
            self.assertTrue(self.script.login_by_env_cookie(self.binary))
            self.assertEqual(self.process.call_count, 2)
            self.process.reset_mock()
            self.process.side_effect = [subprocess.CompletedProcess([], 7), subprocess.CompletedProcess([], 9)]
            self.assertFalse(self.script.login_by_env_cookie(self.binary))
            self.assertEqual(self.process.call_count, 2)
        self.assert_private("private-main", "private-secondary")
        self.assertIn("7", self.output.getvalue())

    def test_spawn_failure_does_not_print_exception_arguments_or_traceback(self):
        for error in (OSError("private-spawn-secret"), subprocess.CalledProcessError(7, ["private-command-secret"])):
            with self.subTest(error=type(error).__name__), mock.patch.dict(os.environ, {"NCMM_COOKIE": "private-input-cookie"}):
                self.process.side_effect = error
                self.assertFalse(self.script.login_by_env_cookie(self.binary))
        self.assert_private("private-spawn-secret", "private-command-secret", "private-input-cookie", "Traceback")

    def test_real_child_output_is_hidden_on_failure(self):
        child = "import sys; print('private-child-stdout'); print('private-child-stderr', file=sys.stderr); sys.exit(7)"
        wrapper = (
            "import importlib.util, sys\n"
            f"spec = importlib.util.spec_from_file_location('login_test', {str(SCRIPT)!r})\n"
            "module = importlib.util.module_from_spec(spec)\n"
            "spec.loader.exec_module(module)\n"
            f"ok = module.run_login([sys.executable, '-c', {child!r}])\n"
            "sys.exit(1 if ok else 0)\n"
        )
        result = self.real_run([sys.executable, "-c", wrapper], cwd=str(self.root), capture_output=True,
                               text=True, encoding="utf-8", timeout=10, env={"PYTHONIOENCODING": "utf-8"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("7", result.stdout)
        self.assertNotIn("private-child-stdout", result.stdout + result.stderr)
        self.assertNotIn("private-child-stderr", result.stdout + result.stderr)

    def test_cookiecloud_failure_stays_false_without_logging_error_details(self):
        with mock.patch.dict(os.environ, {"NCMM_COOKIECLOUD_UUID": "private-cloud-uuid", "NCMM_COOKIECLOUD_PASSWORD": "private-cloud-password"}):
            for result in (subprocess.CompletedProcess([], 7), OSError("private-cloud-error")):
                with self.subTest(result=type(result).__name__):
                    self.process.side_effect = result if isinstance(result, Exception) else None
                    self.process.return_value = result
                    self.assertFalse(self.script.login_by_cookiecloud(self.binary))
        self.assert_private("private-cloud-uuid", "private-cloud-password", "private-cloud-error")

    def test_file_failure_and_partial_success_keep_existing_result_rules(self):
        main = self.root / "private-main-file.txt"
        secondary = self.root / "private-secondary-file.txt"
        for path in (main, secondary):
            path.write_text("MUSIC_U=private-file-cookie", encoding="utf-8")
        with mock.patch.dict(os.environ, {"NCMM_MAIN_COOKIE_FILE": str(main), "NCMM_SECONDARY_COOKIE_FILE": str(secondary)}):
            self.process.side_effect = [subprocess.CompletedProcess([], 7), subprocess.CompletedProcess([], 0)]
            self.assertTrue(self.script.login_by_file(self.binary))
            self.process.reset_mock()
            self.process.side_effect = [subprocess.CompletedProcess([], 7), subprocess.CompletedProcess([], 9)]
            self.assertFalse(self.script.login_by_file(self.binary))
        self.assert_private(str(main), str(secondary), "private-file-cookie")

    def test_missing_file_warning_does_not_echo_user_input(self):
        with mock.patch.dict(os.environ, {"NCMM_COOKIE_FILE": "private-missing-path.txt"}):
            self.assertFalse(self.script.login_by_file(self.binary))
        self.process.assert_not_called()
        self.assert_private("private-missing-path")

    def test_main_falls_back_after_failure_and_exits_on_success(self):
        with mock.patch.object(self.script, "get_binary_path", return_value=self.binary), mock.patch.object(self.script, "login_by_cookiecloud", return_value=False) as cloud, mock.patch.object(self.script, "login_by_env_cookie", return_value=True) as env_cookie, mock.patch.object(self.script, "login_by_file") as file_cookie:
            with self.assertRaises(SystemExit) as raised:
                self.script.main()
        self.assertEqual(raised.exception.code, 0)
        cloud.assert_called_once_with(self.binary)
        env_cookie.assert_called_once_with(self.binary)
        file_cookie.assert_not_called()

    def test_main_without_login_information_still_exits_one(self):
        with mock.patch.object(self.script, "get_binary_path", return_value=self.binary):
            with self.assertRaises(SystemExit) as raised:
                self.script.main()
        self.assertEqual(raised.exception.code, 1)
        self.process.assert_not_called()


if __name__ == "__main__":
    unittest.main()
