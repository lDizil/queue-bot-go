import json
import os
import re
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


TOKEN = re.compile(r"([1-9][0-9]{4,19}):[A-Za-z0-9_-]{30,100}\Z", re.ASCII)
TOKEN_LINE = re.compile(
    rb"(?m)^(?P<prefix>[ \t]*(?:export[ \t]+)?BOT_TOKEN[ \t]*=[ \t]*)(?P<value>[^\r\n]*)(?P<ending>\r?\n|$)"
)


class RotationError(Exception):
    pass


def token_bot_id(token):
    match = TOKEN.fullmatch(token)
    if match is None:
        raise RotationError("BOT_TOKEN must contain the complete token from BotFather, without whitespace")
    return int(match[1])


def updated_environment(content, token):
    new_id = token_bot_id(token)
    assignments = list(TOKEN_LINE.finditer(content))
    if len(assignments) != 1:
        raise RotationError("Existing .env must contain exactly one BOT_TOKEN assignment")
    assignment = assignments[0]
    value = assignment["value"]
    if value[:1] in (b"'", b'"'):
        quote = value[:1]
        end = value.find(quote, 1)
        if end == -1:
            raise RotationError("Existing BOT_TOKEN assignment is not valid")
        old = value[1:end]
        suffix = value[end + 1 :]
        replacement = quote + token.encode("ascii") + quote + suffix
    else:
        match = re.fullmatch(rb"([^ \t#]+)([ \t]*(?:#.*)?)", value)
        if match is None:
            raise RotationError("Existing BOT_TOKEN assignment is not valid")
        old, suffix = match.groups()
        replacement = token.encode("ascii") + suffix
    if re.fullmatch(rb"[ \t]*(?:#.*)?", suffix) is None:
        raise RotationError("Existing BOT_TOKEN assignment is not valid")
    try:
        old_id = token_bot_id(old.decode("ascii"))
    except (UnicodeDecodeError, RotationError) as exc:
        raise RotationError("Existing BOT_TOKEN assignment is not valid") from exc
    if old_id != new_id:
        raise RotationError("The replacement token belongs to a different bot")
    return content[: assignment.start("value")] + replacement + content[assignment.end("value") :], old_id


def replace_environment(path, expected, replacement):
    if not stat.S_ISREG(path.lstat().st_mode):
        raise RotationError("Existing .env must be a regular file")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="wb", dir=path.parent, prefix=".env.rotate-", delete=False) as handle:
            temporary = Path(handle.name)
            os.fchmod(handle.fileno(), 0o600)
            handle.write(replacement)
            handle.flush()
            os.fsync(handle.fileno())
        if path.read_bytes() != expected:
            raise RotationError("The server environment changed during verification; run the action again")
        os.replace(temporary, path)
        temporary = None
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except OSError as exc:
        raise RotationError("Could not atomically update the server environment") from exc
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def verify_token(token, expected_id):
    request = urllib.request.Request("https://api.telegram.org/bot" + token + "/getMe", method="GET")
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=10) as response:
            result = json.loads(response.read(65536))
    except urllib.error.HTTPError as exc:
        raise RotationError(f"Telegram rejected verification (HTTP {exc.code}); .env was not changed") from exc
    except (OSError, ValueError) as exc:
        raise RotationError("Could not verify the new token with Telegram; .env was not changed") from exc
    if not isinstance(result, dict) or result.get("ok") is not True:
        raise RotationError("Telegram did not accept the new token; .env was not changed")
    bot = result.get("result")
    if not isinstance(bot, dict) or bot.get("is_bot") is not True or bot.get("id") != expected_id:
        raise RotationError("Telegram returned a different bot; .env was not changed")


def command(arguments, timeout=30):
    try:
        result = subprocess.run(arguments, check=False, capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise RotationError("A server command could not complete") from exc
    if result.returncode != 0:
        raise RotationError("A server command failed; its output is hidden to protect credentials")
    return result.stdout.strip()


def inspect_container():
    try:
        containers = json.loads(command(["docker", "inspect", "queuebot"]))
        if len(containers) != 1:
            raise ValueError("missing deployment")
        return containers[0]
    except (ValueError, KeyError, TypeError) as exc:
        raise RotationError("Could not inspect the existing bot deployment") from exc


def verify_deployment(root, expected_sha, old_id):
    if re.fullmatch(r"[0-9a-f]{40}", expected_sha) is None:
        raise RotationError("The action must run from the current main commit")
    if command(["git", "rev-parse", "HEAD"]) != expected_sha:
        raise RotationError("The server checkout is not the selected main commit; finish its normal deployment first")
    command(["git", "diff", "--quiet", "HEAD", "--"])
    container = inspect_container()
    try:
        config = container["Config"]
        labels = config["Labels"]
        if labels.get("com.docker.compose.service") != "queuebot" or Path(labels.get("com.docker.compose.project.working_dir", "")).resolve() != root:
            raise ValueError("different compose deployment")
        environment = dict(item.split("=", 1) for item in config["Env"] if "=" in item)
        if token_bot_id(environment.get("BOT_TOKEN", "")) != old_id:
            raise ValueError("different deployed bot")
        image_id = container["Image"]
        local_image = command(["docker", "image", "inspect", "--format", "{{.Id}}", config["Image"]])
        if not image_id or local_image != image_id:
            raise ValueError("image tag does not identify deployed image")
    except (KeyError, TypeError, ValueError, RotationError) as exc:
        raise RotationError("Existing bot identity or image differs from its deployment; no token was changed") from exc
    return image_id


def wait_until_ready(image_id):
    for _ in range(60):
        container = inspect_container()
        if container.get("Image") != image_id:
            raise RotationError("The bot image changed during token rotation; no automatic rollback was performed")
        state = container.get("State", {})
        if state.get("Status") == "running" and not state.get("Restarting") and state.get("Health", {}).get("Status") == "healthy":
            return
        time.sleep(2)
    raise RotationError("The recreated bot did not become ready; the new token remains in .env, without automatic rollback")


def rotate(token, expected_sha):
    root = Path.cwd().resolve()
    path = root / ".env"
    try:
        if not stat.S_ISREG(path.lstat().st_mode):
            raise RotationError("Existing .env must be a regular file")
        original = path.read_bytes()
    except OSError as exc:
        raise RotationError("An initialized server .env is required") from exc
    replacement, old_id = updated_environment(original, token)
    image_id = verify_deployment(root, expected_sha, old_id)
    verify_token(token, old_id)
    replace_environment(path, original, replacement)
    command(["docker", "compose", "up", "-d", "--no-deps", "--no-build", "--pull", "never", "--force-recreate", "queuebot"], timeout=120)
    wait_until_ready(image_id)
    print("The existing bot image was recreated with the verified token and is ready")


def main():
    token = os.environ.pop("ROTATE_BOT_TOKEN", "")
    try:
        token_bot_id(token)
        if sys.argv[1:] == ["validate"]:
            print("::add-mask::" + token)
            return 0
        if sys.argv[1:] != ["rotate"]:
            raise RotationError("Use validate or rotate")
        rotate(token, os.environ.get("EXPECTED_SHA", ""))
        return 0
    except RotationError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    except Exception:
        print("Token rotation failed; diagnostic details are hidden to protect credentials", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
