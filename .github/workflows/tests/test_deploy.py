import json
import os
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path


WORKFLOW = Path(__file__).parents[1] / "deploy.yml"
DEPLOY_SCRIPT = textwrap.dedent(WORKFLOW.read_text().split("          script: |\n", 1)[1])
DEPLOY_SHA = "a" * 40
SUPERSEDED_SHA = "b" * 40
PRIVATE_STDOUT = "PRIVATE_STDOUT_SENTINEL"
PRIVATE_STDERR = "PRIVATE_STDERR_SENTINEL"
INFRA_IMAGES = ["postgres:16-alpine", "dpage/pgadmin4:latest"]
FAKE_COMMAND = r'''
import json
import os
import sys
from pathlib import Path

name = Path(sys.argv[0]).name
arguments = sys.argv[1:]
root = Path(os.environ["FAKE_DEPLOY_COMMANDS"])
with (root / "calls.jsonl").open("a") as handle:
    handle.write(json.dumps([name, *arguments]) + "\n")
print("PRIVATE_STDERR_SENTINEL " + name, file=sys.stderr)
if name == "git" and arguments[:1] == ["rev-parse"]:
    stage = "rev-parse"
elif name == "git":
    stage = arguments[0]
elif name == "docker" and arguments[:3] == ["compose", "config", "--images"]:
    stage = "config-images"
elif name == "docker" and arguments[:1] == ["compose"]:
    stage = arguments[1]
elif name == "docker" and arguments[:2] == ["image", "inspect"]:
    stage = "image-inspect"
elif name == "docker" and arguments[:1] == ["pull"]:
    stage = "pull"
elif name == "docker" and arguments[:1] == ["inspect"]:
    stage = "inspect-" + arguments[-1]
else:
    stage = name
counter = root / (stage + ".count")
count = int(counter.read_text()) if counter.exists() else 0
counter.write_text(str(count + 1))
if stage not in ("rev-parse", "config-images") and not stage.startswith("inspect-"):
    print("PRIVATE_STDOUT_SENTINEL " + name)
if os.environ.get("FAKE_FAIL") == stage or os.environ.get("FAKE_FAIL") == f"{stage}-{count + 1}":
    sys.exit(23)
if stage == "image-inspect" and arguments[-1] in os.environ.get("FAKE_MISSING_IMAGES", "").split(","):
    sys.exit(1)
if stage == "config-images":
    print(os.environ["FAKE_INFRA_IMAGES"])
elif stage == "rev-parse":
    shas = os.environ["FAKE_MAIN_SHAS"].split(",")
    print(shas[min(count, len(shas) - 1)])
elif stage.startswith("inspect-"):
    states = os.environ.get(
        "FAKE_BOT_STATES" if arguments[-1] == "queuebot" else "FAKE_PGADMIN_STATES",
        "running:false:healthy" if arguments[-1] == "queuebot" else "running:false",
    ).split(";")
    print(states[min(count, len(states) - 1)])
elif name == "docker" and arguments[:1] == ["logs"]:
    print("PRIVATE_CONTAINER_DIAGNOSTIC")
'''


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.checkout = self.root / "checkout"
        self.checkout.mkdir()
        self.log_dir = self.root / "state"
        self.commands = self.root / "commands"
        self.commands.mkdir()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.fake_command = self.bin_dir / "fake-command"
        self.fake_command.write_text("#!" + sys.executable + "\n" + textwrap.dedent(FAKE_COMMAND))
        self.fake_command.chmod(0o700)
        for name in ("git", "docker", "sleep"):
            (self.bin_dir / name).symlink_to(self.fake_command)
        self.environment = {
            **os.environ,
            "PATH": str(self.bin_dir) + os.pathsep + os.environ["PATH"],
            "DEPLOY_SHA": DEPLOY_SHA,
            "QUEUEBOT_DEPLOY_DIR": str(self.checkout),
            "QUEUEBOT_DEPLOY_STATE_DIR": str(self.log_dir),
            "FAKE_DEPLOY_COMMANDS": str(self.commands),
            "FAKE_MAIN_SHAS": DEPLOY_SHA,
            "FAKE_INFRA_IMAGES": "\n".join(INFRA_IMAGES),
        }

    def deploy(self, **environment):
        result = subprocess.run(
            ["/bin/sh"], input=DEPLOY_SCRIPT, text=True, capture_output=True,
            cwd=self.checkout, env={**self.environment, **environment}, timeout=20,
        )
        self.assertEqual(result.stderr, "")
        self.assertNotIn(PRIVATE_STDOUT, result.stdout)
        self.assertNotIn(PRIVATE_STDERR, result.stdout)
        self.assertNotIn("PRIVATE_CONTAINER_DIAGNOSTIC", result.stdout)
        return result

    def calls(self):
        path = self.commands / "calls.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def log(self):
        return (self.log_dir / "deploy.log").read_text()

    def assert_failed(self, result, code):
        self.assertEqual(result.returncode, code)
        self.assertEqual(result.stdout, "Deployment failed.\n")
        self.assertIn(PRIVATE_STDERR, self.log())

    def assert_not_stopped(self):
        self.assertFalse(any(call[:3] == ["docker", "compose", "stop"] for call in self.calls()))
        self.assertFalse(any(call[:3] == ["docker", "compose", "up"] for call in self.calls()))

    def test_success_keeps_diagnostics_private_and_drains_before_recreating_all_services(self):
        result = self.deploy()
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Deployment ready.\n")
        calls = self.calls()
        config = calls.index(["docker", "compose", "config", "--quiet"])
        build = calls.index(["docker", "compose", "build", "queuebot"])
        last_fetch = len(calls) - 1 - calls[::-1].index(["git", "fetch", "origin", "main"])
        stop = calls.index(["docker", "compose", "stop", "queuebot"])
        up = calls.index(["docker", "compose", "up", "-d", "--no-build", "--pull", "never", "postgres", "pgadmin", "queuebot"])
        self.assertLess(config, build)
        image_config = calls.index(["docker", "compose", "config", "--images", "postgres", "pgadmin"])
        self.assertLess(build, image_config)
        for image in INFRA_IMAGES:
            image_inspect = calls.index(["docker", "image", "inspect", image])
            self.assertLess(image_config, image_inspect)
            self.assertLess(image_inspect, last_fetch)
        self.assertFalse(any(call[:2] == ["docker", "pull"] for call in calls))
        self.assertLess(last_fetch, stop)
        self.assertLess(stop, up)
        inspections = [call for call in calls if call[:2] == ["docker", "inspect"]]
        self.assertEqual([call[-1] for call in inspections], ["queuebot", "queuebot_pgadmin"])
        self.assertLess(up, calls.index(inspections[0]))
        self.assertIn(PRIVATE_STDOUT + " git", self.log())
        self.assertIn(PRIVATE_STDOUT + " docker", self.log())
        self.assertIn("queuebot: running:false:healthy", self.log())
        self.assertIn("queuebot_pgadmin: running:false", self.log())
        self.assertFalse(any(call[:3] == ["docker", "compose", "ps"] for call in calls))
        self.assertFalse(any(call[:2] == ["docker", "logs"] for call in calls))

    def test_config_and_build_failure_leave_running_services_untouched(self):
        for stage in ("config", "build", "config-images"):
            with self.subTest(stage=stage):
                result = self.deploy(FAKE_FAIL=stage)
                self.assert_failed(result, 23)
                self.assert_not_stopped()

    def test_only_missing_resolved_infrastructure_images_are_pulled_before_stopping(self):
        images = ["registry.example/database:stable", "registry.example/admin:latest"]
        for missing in (images[-1:], images):
            with self.subTest(missing=missing):
                result = self.deploy(FAKE_INFRA_IMAGES="\n".join(images), FAKE_MISSING_IMAGES=",".join(missing))
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "Deployment ready.\n")
                calls = self.calls()
                pulls = [call for call in calls if call[:2] == ["docker", "pull"]]
                self.assertEqual(pulls, [["docker", "pull", image] for image in missing])
                last_fetch = len(calls) - 1 - calls[::-1].index(["git", "fetch", "origin", "main"])
                stop = calls.index(["docker", "compose", "stop", "queuebot"])
                for pull in pulls:
                    self.assertLess(calls.index(["docker", "image", "inspect", pull[-1]]), calls.index(pull))
                    self.assertLess(calls.index(pull), last_fetch)
                self.assertLess(last_fetch, stop)
                (self.commands / "calls.jsonl").unlink()

    def test_failed_infrastructure_pull_leaves_the_bot_running(self):
        result = self.deploy(FAKE_MISSING_IMAGES=INFRA_IMAGES[-1], FAKE_FAIL="pull")
        self.assert_failed(result, 23)
        self.assertIn(["docker", "pull", INFRA_IMAGES[-1]], self.calls())
        self.assert_not_stopped()

    def test_git_and_stop_failures_stop_the_script_with_the_original_exit_code(self):
        for stage in ("fetch", "reset", "clean", "rev-parse", "stop"):
            with self.subTest(stage=stage):
                result = self.deploy(FAKE_FAIL=stage)
                self.assert_failed(result, 23)
                self.assertFalse(any(call[:3] == ["docker", "compose", "up"] for call in self.calls()))
                (self.commands / "calls.jsonl").unlink()

    def test_refresh_failure_after_build_does_not_stop_the_bot(self):
        result = self.deploy(FAKE_FAIL="fetch-2")
        self.assert_failed(result, 23)
        self.assertIn(["docker", "compose", "build", "queuebot"], self.calls())
        self.assert_not_stopped()

    def test_up_failure_is_not_reported_as_ready_and_never_checks_old_containers(self):
        result = self.deploy(FAKE_FAIL="up")
        self.assert_failed(result, 23)
        self.assertFalse(any(call[:2] == ["docker", "inspect"] for call in self.calls()))

    def test_inspect_failure_inside_readiness_propagates_without_errexit_suppression(self):
        for container in ("queuebot", "queuebot_pgadmin"):
            with self.subTest(container=container):
                result = self.deploy(FAKE_FAIL="inspect-" + container)
                self.assert_failed(result, 23)

    def test_not_ready_bot_fails_and_stores_container_diagnostics_only_on_server(self):
        result = self.deploy(FAKE_BOT_STATES="running:false:unhealthy")
        self.assert_failed(result, 1)
        self.assertIn("queuebot did not become ready", self.log())
        self.assertIn("PRIVATE_CONTAINER_DIAGNOSTIC", self.log())
        self.assertIn(["docker", "logs", "queuebot", "--tail", "30"], self.calls())
        self.assertFalse(any(call[-1] == "queuebot_pgadmin" for call in self.calls()))

    def test_restarting_or_stopped_pgadmin_does_not_pass_deployment(self):
        for state in ("running:true", "exited:false"):
            with self.subTest(state=state):
                result = self.deploy(FAKE_PGADMIN_STATES=state)
                self.assert_failed(result, 1)
                self.assertIn("queuebot_pgadmin did not become ready", self.log())
                self.assertIn(["docker", "logs", "queuebot_pgadmin", "--tail", "30"], self.calls())

    def test_readiness_waits_until_both_services_recover(self):
        result = self.deploy(
            FAKE_BOT_STATES="running:true:starting;running:false:healthy",
            FAKE_PGADMIN_STATES="restarting:true;running:false",
        )
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Deployment ready.\n")
        self.assertEqual(self.calls().count(["sleep", "2"]), 2)
        self.assertIn(PRIVATE_STDOUT + " sleep", self.log())

    def test_superseded_commit_is_skipped_before_checkout_changes_or_docker_commands(self):
        result = self.deploy(FAKE_MAIN_SHAS=SUPERSEDED_SHA)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Deployment skipped (superseded commit).\n")
        self.assertEqual(self.calls(), [["git", "fetch", "origin", "main"], ["git", "rev-parse", "origin/main"]])

    def test_main_changing_during_build_is_skipped_before_stopping_or_recreating(self):
        result = self.deploy(FAKE_MAIN_SHAS=DEPLOY_SHA + "," + SUPERSEDED_SHA)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Deployment skipped (superseded commit).\n")
        self.assertIn(["docker", "compose", "build", "queuebot"], self.calls())
        self.assertEqual(self.calls().count(["git", "fetch", "origin", "main"]), 2)
        self.assert_not_stopped()

    def test_only_current_and_previous_logs_are_retained_with_private_permissions(self):
        self.log_dir.mkdir(mode=0o755)
        (self.log_dir / "deploy.log").write_text("older deployment\n")
        (self.log_dir / "deploy.log").chmod(0o644)
        first = self.deploy(FAKE_FAIL="build")
        self.assert_failed(first, 23)
        self.assertEqual((self.log_dir / "deploy.previous.log").read_text(), "older deployment\n")
        failed_log = self.log()
        self.assertEqual(self.deploy().returncode, 0)
        self.assertEqual((self.log_dir / "deploy.previous.log").read_text(), failed_log)
        self.assertEqual(sorted(path.name for path in self.log_dir.iterdir()), ["deploy.log", "deploy.previous.log"])
        self.assertEqual(stat.S_IMODE(self.log_dir.stat().st_mode), 0o700)
        for path in self.log_dir.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_log_setup_failure_hides_raw_diagnostics_and_preserves_exit_status(self):
        (self.bin_dir / "mkdir").symlink_to(self.fake_command)
        result = self.deploy(FAKE_FAIL="mkdir")
        self.assertEqual(result.returncode, 23)
        self.assertEqual(result.stdout, "Deployment failed.\n")
        self.assertFalse((self.log_dir / "deploy.log").exists())
        self.assertEqual([call[0] for call in self.calls()], ["mkdir"])

    def test_checkout_failure_stays_private_and_does_not_invoke_git_or_docker(self):
        result = self.deploy(QUEUEBOT_DEPLOY_DIR=str(self.root / "PRIVATE_STDERR_SENTINEL"))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "Deployment failed.\n")
        self.assertIn(PRIVATE_STDERR, self.log())
        self.assertEqual(self.calls(), [])


if __name__ == "__main__":
    unittest.main()
