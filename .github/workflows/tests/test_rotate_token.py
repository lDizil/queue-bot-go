import contextlib
import importlib.util
import io
import json
import os
import stat
import subprocess
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).parents[1] / "scripts" / "rotate_token.py"
SPEC = importlib.util.spec_from_file_location("rotate_token", SCRIPT)
rotation = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(rotation)
OLD_TOKEN = "123456:" + "a" * 35
NEW_TOKEN = "123456:" + "b" * 35
OTHER_TOKEN = "654321:" + "c" * 35
EXPECTED_SHA = "a" * 40
IMAGE = "sha256:" + "1" * 64


class EnvironmentTests(unittest.TestCase):
    def test_updates_only_token_and_preserves_quotes_comments_and_line_endings(self):
        for quote in ("", "'", '"'):
            with self.subTest(quote=quote):
                original = ("# настройки\r\nDB_PASSWORD=keep-this-value\r\n  export BOT_TOKEN = " + quote + OLD_TOKEN + quote + "  # bot\r\nOTHER=untouched").encode()
                result, bot_id = rotation.updated_environment(original, NEW_TOKEN)
                self.assertEqual(bot_id, 123456)
                self.assertEqual(result, original.replace(OLD_TOKEN.encode(), NEW_TOKEN.encode()))

    def test_duplicate_missing_invalid_and_different_bot_assignments_are_rejected(self):
        for content, token in (
            (b"DB_HOST=postgres\n", NEW_TOKEN),
            (("BOT_TOKEN=" + OLD_TOKEN + "\nBOT_TOKEN=" + OLD_TOKEN).encode(), NEW_TOKEN),
            (("BOT_TOKEN=\"" + OLD_TOKEN + '"; execute\n').encode(), NEW_TOKEN),
            (("BOT_TOKEN=" + OLD_TOKEN).encode(), OTHER_TOKEN),
        ):
            with self.subTest(content=content[:20]):
                with self.assertRaises(rotation.RotationError) as raised:
                    rotation.updated_environment(content, token)
                self.assertNotIn(token, str(raised.exception))
                self.assertNotIn(OLD_TOKEN, str(raised.exception))

    def test_invalid_secret_cannot_add_an_environment_variable_or_shell_command(self):
        for token in (NEW_TOKEN + "\nDB_HOST=attacker", NEW_TOKEN + "'", NEW_TOKEN + "$(id)", " " + NEW_TOKEN, NEW_TOKEN + "\r", "123456:short"):
            with self.subTest(token_length=len(token)):
                with self.assertRaises(rotation.RotationError):
                    rotation.updated_environment(("BOT_TOKEN=" + OLD_TOKEN).encode(), token)

    def test_atomic_replacement_is_private_and_does_not_modify_original_before_rename(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            original = ("BOT_TOKEN=" + OLD_TOKEN + "\nDB_PASSWORD=unchanged\n").encode()
            replacement, _ = rotation.updated_environment(original, NEW_TOKEN)
            path.write_bytes(original)
            path.chmod(0o644)
            inode = path.stat().st_ino
            actual_replace = os.replace

            def rename(temporary, target):
                self.assertEqual(path.read_bytes(), original)
                self.assertEqual(stat.S_IMODE(Path(temporary).stat().st_mode), 0o600)
                actual_replace(temporary, target)

            with mock.patch.object(rotation.os, "replace", side_effect=rename):
                rotation.replace_environment(path, original, replacement)
            self.assertNotEqual(path.stat().st_ino, inode)
            self.assertEqual(path.read_bytes(), replacement)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(list(path.parent.glob(".env.rotate-*")), [])

    def test_failed_rename_and_concurrent_edit_preserve_original_and_remove_temporary(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            original = ("BOT_TOKEN=" + OLD_TOKEN).encode()
            replacement, _ = rotation.updated_environment(original, NEW_TOKEN)
            path.write_bytes(original)
            cause = OSError("private details " + NEW_TOKEN)
            with mock.patch.object(rotation.os, "replace", side_effect=cause):
                with self.assertRaises(rotation.RotationError) as raised:
                    rotation.replace_environment(path, original, replacement)
            self.assertIs(raised.exception.__cause__, cause)
            self.assertNotIn(NEW_TOKEN, str(raised.exception))
            self.assertEqual(path.read_bytes(), original)
            self.assertEqual(list(path.parent.glob(".env.rotate-*")), [])
            external = original + b"\nEXTERNAL_CHANGE=preserve\n"
            path.write_bytes(external)
            with self.assertRaises(rotation.RotationError):
                rotation.replace_environment(path, original, replacement)
            self.assertEqual(path.read_bytes(), external)
            self.assertEqual(list(path.parent.glob(".env.rotate-*")), [])


class VerificationTests(unittest.TestCase):
    def test_get_me_checks_success_and_actual_bot_identity(self):
        for response, succeeds in (
            ({"ok": True, "result": {"id": 123456, "is_bot": True}}, True),
            ({"ok": True, "result": {"id": 654321, "is_bot": True}}, False),
            ({"ok": True, "result": {"id": 123456, "is_bot": False}}, False),
            ({"ok": False, "description": NEW_TOKEN}, False),
        ):
            with self.subTest(response_ok=response["ok"]):
                body = io.BytesIO(json.dumps(response).encode())
                opener = mock.Mock()
                opener.open.return_value = contextlib.closing(body)
                with mock.patch.object(rotation.urllib.request, "build_opener", return_value=opener):
                    if succeeds:
                        rotation.verify_token(NEW_TOKEN, 123456)
                    else:
                        with self.assertRaises(rotation.RotationError) as raised:
                            rotation.verify_token(NEW_TOKEN, 123456)
                        self.assertNotIn(NEW_TOKEN, str(raised.exception))
                request = opener.open.call_args.args[0]
                self.assertEqual(request.get_method(), "GET")
                self.assertTrue(request.full_url.endswith("/getMe"))

    def test_api_errors_keep_cause_without_exposing_url_or_token(self):
        url = "https://api.telegram.org/bot" + NEW_TOKEN + "/getMe"
        for cause in (
            urllib.error.HTTPError(url, 401, "private " + NEW_TOKEN, {}, None),
            urllib.error.URLError(url),
            ValueError("private " + NEW_TOKEN),
        ):
            opener = mock.Mock()
            opener.open.side_effect = cause
            with mock.patch.object(rotation.urllib.request, "build_opener", return_value=opener):
                with self.assertRaises(rotation.RotationError) as raised:
                    rotation.verify_token(NEW_TOKEN, 123456)
            self.assertIs(raised.exception.__cause__, cause)
            self.assertNotIn(NEW_TOKEN, str(raised.exception))
            self.assertNotIn("api.telegram.org", str(raised.exception))

    def test_deployment_guard_requires_selected_sha_and_same_existing_image(self):
        root = Path("/deployed/queue-bot-go")
        container = {
            "Image": IMAGE,
            "Config": {
                "Image": "queue-bot-go-queuebot",
                "Labels": {"com.docker.compose.service": "queuebot", "com.docker.compose.project.working_dir": str(root)},
                "Env": ["BOT_TOKEN=" + OLD_TOKEN],
            },
        }
        with mock.patch.object(rotation, "command", side_effect=[EXPECTED_SHA, "", IMAGE]), mock.patch.object(rotation, "inspect_container", return_value=container):
            self.assertEqual(rotation.verify_deployment(root, EXPECTED_SHA, 123456), IMAGE)
        with mock.patch.object(rotation, "command", return_value="b" * 40), mock.patch.object(rotation, "inspect_container") as inspect:
            with self.assertRaises(rotation.RotationError):
                rotation.verify_deployment(root, EXPECTED_SHA, 123456)
            inspect.assert_not_called()
        with mock.patch.object(rotation, "command", side_effect=[EXPECTED_SHA, "", "another-image"]), mock.patch.object(rotation, "inspect_container", return_value=container):
            with self.assertRaises(rotation.RotationError):
                rotation.verify_deployment(root, EXPECTED_SHA, 123456)

    def test_command_errors_cannot_log_captured_credentials(self):
        result = subprocess.CompletedProcess(["docker"], 1, stdout=NEW_TOKEN, stderr="private " + OLD_TOKEN)
        with mock.patch.object(rotation.subprocess, "run", return_value=result):
            with self.assertRaises(rotation.RotationError) as raised:
                rotation.command(["docker", "compose", "up"])
        self.assertNotIn(NEW_TOKEN, str(raised.exception))
        self.assertNotIn(OLD_TOKEN, str(raised.exception))


class RotationTests(unittest.TestCase):
    def test_validation_failure_leaves_environment_and_container_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / ".env"
            original = ("BOT_TOKEN=" + OLD_TOKEN).encode()
            path.write_bytes(original)
            with mock.patch.object(rotation.Path, "cwd", return_value=root), mock.patch.object(rotation, "verify_deployment", return_value=IMAGE), mock.patch.object(rotation, "verify_token", side_effect=rotation.RotationError("verification failed")), mock.patch.object(rotation, "command") as command:
                with self.assertRaises(rotation.RotationError):
                    rotation.rotate(NEW_TOKEN, EXPECTED_SHA)
            self.assertEqual(path.read_bytes(), original)
            command.assert_not_called()

    def test_rotation_changes_only_token_recreates_without_build_and_does_not_roll_back(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / ".env"
            original = ("BOT_TOKEN=" + OLD_TOKEN + "\nDB_PASSWORD=keep\n").encode()
            path.write_bytes(original)

            def recreate(arguments, timeout):
                self.assertEqual(path.read_bytes(), original.replace(OLD_TOKEN.encode(), NEW_TOKEN.encode()))
                self.assertEqual(arguments, ["docker", "compose", "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "queuebot"])
                return ""

            with mock.patch.object(rotation.Path, "cwd", return_value=root), mock.patch.object(rotation, "verify_deployment", return_value=IMAGE), mock.patch.object(rotation, "verify_token"), mock.patch.object(rotation, "command", side_effect=recreate), mock.patch.object(rotation, "wait_until_ready", side_effect=rotation.RotationError("not ready")):
                with self.assertRaises(rotation.RotationError):
                    rotation.rotate(NEW_TOKEN, EXPECTED_SHA)
            self.assertIn(NEW_TOKEN.encode(), path.read_bytes())
            self.assertNotIn(OLD_TOKEN.encode(), path.read_bytes())

    def test_readiness_does_not_accept_another_image(self):
        with mock.patch.object(rotation, "inspect_container", return_value={"Image": "different", "State": {"Status": "running", "Health": {"Status": "healthy"}}}):
            with self.assertRaises(rotation.RotationError):
                rotation.wait_until_ready(IMAGE)

    def test_cli_removes_token_from_child_environment_and_hides_unexpected_errors(self):
        output = io.StringIO()
        with mock.patch.dict(os.environ, {"ROTATE_BOT_TOKEN": NEW_TOKEN, "EXPECTED_SHA": EXPECTED_SHA}), mock.patch.object(rotation.sys, "argv", ["rotate_token.py", "rotate"]), mock.patch.object(rotation, "rotate", side_effect=RuntimeError("private " + NEW_TOKEN)), contextlib.redirect_stderr(output):
            self.assertEqual(rotation.main(), 1)
            self.assertNotIn("ROTATE_BOT_TOKEN", os.environ)
        self.assertNotIn(NEW_TOKEN, output.getvalue())
        self.assertNotIn("Traceback", output.getvalue())


if __name__ == "__main__":
    unittest.main()
