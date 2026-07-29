#!/usr/bin/env python3
"""LLM-based Watchdog for the mecha-developer supervisor system.

This separate Watchdog captures tmux windows in the mecha session, asks an LLM
whether the supervisor needs a momentum nudge, and mails that note to the
supervisor only when the LLM says to nudge.

The LLM CLI is configurable via ~/.mecha-developer/watchdog-llm.json:
  {"cli": "claude" | "codex", "model": "<model>", "extra_args": []}
Defaults: claude + claude-sonnet-4-6 + [].

Usage:
  python3 watchdog.py
  python3 watchdog.py --dry-run
  python3 watchdog.py --self-test

"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

WATCHDOG_TOOLS = Path(__file__).resolve().parent
SUPERVISOR_TOOLS = WATCHDOG_TOOLS.parents[1] / "supervisor" / "tools"
sys.path.insert(0, str(SUPERVISOR_TOOLS))
from _tmux import capture_pane, list_windows, session_exists

DEFAULT_CLI = "claude"
DEFAULT_MODEL = "claude-sonnet-4-6"
ALLOWED_CLIS = ("claude", "codex")
LLM_TIMEOUT = 90
CAPTURE_LINES = 80
LLM_CONFIG_PATH = os.path.expanduser("~/.mecha-developer/watchdog-llm.json")
NTFY_TOPIC = "mecha-wolfgang-notify"
AUTH_ALERT_SENTINEL = os.path.expanduser("~/.mecha-developer/watchdog-auth-alerted")
AUTH_ALERT_DEBOUNCE_SEC = 6 * 60 * 60
AUTH_FAILURE_MARKERS = (
    "not logged in",
    "please run /login",
    "please run `codex login`",
    "please run codex login",
    "run `claude /login`",
    "run claude /login",
    "/login to authenticate",
)
# Windows excluded from monitoring and included in the prompt as context only. Empty
# since the supervisor dashboard was removed; the filter stays for a future service window.
SYSTEM_WINDOWS = set()

LOG_PREFIX = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def log(msg):
    print(f"{LOG_PREFIX} {msg}", flush=True)


def load_watchdog_guidance():
    """Load watchdog evaluation guidance from the dedicated watchdog skill."""
    skill_path = WATCHDOG_TOOLS.parent / "SKILL.md"
    try:
        with open(skill_path) as f:
            content = f.read()
    except FileNotFoundError:
        log(f"Warning: watchdog SKILL.md not found at {skill_path} - using fallback")
        return _fallback_guidance()

    if content.startswith("---"):
        end = content.find("\n---", 3)
        if end != -1:
            content = content[end + 4 :].strip()

    return content


def _fallback_guidance():
    return (
        "Decide whether to nudge the supervisor to keep work moving. "
        "Nudge only for finished or idle workers, or a supervisor waiting on "
        "something it could decide itself, unless the user explicitly said to hold. "
        'Respond only as JSON: {"nudge": true|false, "note": "..."}'
    )


def build_prompt(window_captures, guidance):
    """Build the LLM prompt from captured windows and watchdog guidance."""
    now_str = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    sections = []
    for name, content in window_captures.items():
        header = f"=== Window: {name} | capture time: {now_str} ==="
        sections.append(f"{header}\n{content}")

    windows_text = "\n\n".join(sections)
    system_note = (
        f" System windows named {sorted(SYSTEM_WINDOWS)} are included only for context."
        if SYSTEM_WINDOWS
        else ""
    )
    return f"""You are evaluating an AI supervisor system running in tmux.

Below are the current terminal captures.{system_note}

--- WINDOW CAPTURES ---

{windows_text}

--- WATCHDOG GUIDANCE ---

{guidance}

--- END GUIDANCE ---

Respond with ONLY valid JSON, no markdown:
{{"nudge": true|false, "note": "one short message to the supervisor, or empty"}}"""


def load_llm_config(path=LLM_CONFIG_PATH):
    """Load watchdog LLM config. Returns dict with cli/model/extra_args."""
    config = {"cli": DEFAULT_CLI, "model": DEFAULT_MODEL, "extra_args": []}
    if not os.path.isfile(path):
        return config

    with open(path) as f:
        data = json.load(f)

    if not isinstance(data, dict):
        raise ValueError(f"{path}: expected JSON object, got {type(data).__name__}")

    if "cli" in data:
        cli = data["cli"]
        if cli not in ALLOWED_CLIS:
            raise ValueError(f"{path}: cli must be one of {ALLOWED_CLIS}, got {cli!r}")
        config["cli"] = cli
    if "model" in data:
        if not isinstance(data["model"], str) or not data["model"]:
            raise ValueError(f"{path}: model must be a non-empty string")
        config["model"] = data["model"]
    if "extra_args" in data:
        extra_args = data["extra_args"]
        if not isinstance(extra_args, list) or not all(
            isinstance(arg, str) for arg in extra_args
        ):
            raise ValueError(f"{path}: extra_args must be a list of strings")
        config["extra_args"] = extra_args

    return config


def build_llm_argv(cli, model, prompt, extra_args, output_last_message=None):
    """Build the subprocess argv for the chosen CLI."""
    if cli == "claude":
        return ["claude", "-p", prompt, "--model", model, *extra_args]
    if cli == "codex":
        argv = ["codex", "exec", "--model", model, *extra_args]
        if output_last_message:
            argv.extend(["--output-last-message", output_last_message])
        argv.append(prompt)
        return argv
    raise ValueError(f"unsupported cli: {cli!r}")


def is_auth_failure(stderr, stdout=""):
    """Return True if the combined output looks like a CLI auth failure."""
    blob = (stderr + "\n" + stdout).lower()
    return any(marker in blob for marker in AUTH_FAILURE_MARKERS)


def _ntfy(title, body):
    """Send a push notification via ntfy.sh. Best-effort, errors logged not raised."""
    try:
        result = subprocess.run(
            [
                "curl",
                "-sS",
                "-H",
                f"Title: {title}",
                "-d",
                body,
                f"https://ntfy.sh/{NTFY_TOPIC}",
            ],
            capture_output=True,
            text=True,
            timeout=15,
        )
        if result.returncode != 0:
            log(f"ntfy alert failed (exit {result.returncode}): {result.stderr.strip()}")
        else:
            log(f"ntfy alert sent: {title}")
    except Exception as e:
        log(f"ntfy alert error: {e}")


def send_auth_failure_alert(cli):
    """Send a debounced ntfy push about an LLM CLI auth failure."""
    try:
        if os.path.isfile(AUTH_ALERT_SENTINEL):
            age = time.time() - os.path.getmtime(AUTH_ALERT_SENTINEL)
            if age < AUTH_ALERT_DEBOUNCE_SEC:
                log(f"auth-failure ntfy suppressed (last alert {int(age)}s ago)")
                return
    except OSError:
        pass

    login_cmd = "claude /login" if cli == "claude" else "codex login"
    _ntfy(
        title=f"Watchdog auth failure - run `{login_cmd}`",
        body=(
            f"Watchdog could not reach {cli}: not logged in. "
            f"Run `{login_cmd}` on the supervisor host to restore monitoring."
        ),
    )

    try:
        os.makedirs(os.path.dirname(AUTH_ALERT_SENTINEL), exist_ok=True)
        with open(AUTH_ALERT_SENTINEL, "w") as f:
            f.write(datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ\n"))
    except OSError as e:
        log(f"could not write auth-alert sentinel: {e}")


def clear_auth_alert_sentinel():
    """Remove the auth-alert debounce sentinel after a successful LLM call."""
    try:
        os.unlink(AUTH_ALERT_SENTINEL)
    except FileNotFoundError:
        pass
    except OSError as e:
        log(f"could not clear auth-alert sentinel: {e}")


def call_llm(prompt, config):
    """Run the configured LLM CLI. Returns (stdout, auth_failed)."""
    output_path = None
    if config["cli"] == "codex":
        fd, output_path = tempfile.mkstemp(prefix="watchdog-codex-", suffix=".txt")
        os.close(fd)

    argv = build_llm_argv(
        config["cli"],
        config["model"],
        prompt,
        config["extra_args"],
        output_last_message=output_path,
    )

    try:
        env = os.environ.copy()
        env.pop("CLAUDECODE", None)
        result = subprocess.run(
            argv,
            capture_output=True,
            text=True,
            timeout=LLM_TIMEOUT,
            env=env,
        )
        if result.returncode != 0:
            stderr = result.stderr.strip()
            stdout = result.stdout.strip()
            if is_auth_failure(stderr, stdout):
                log(f"{config['cli']} auth failure detected: {stderr[:200] or stdout[:200]}")
                send_auth_failure_alert(config["cli"])
                return None, True
            log(f"{config['cli']} exited with code {result.returncode}: {(stderr or stdout)[:200]}")
            return None, False

        clear_auth_alert_sentinel()
        if output_path:
            try:
                with open(output_path) as f:
                    return f.read().strip(), False
            except OSError as e:
                log(f"could not read codex last-message output: {e}")
                return None, False
        return result.stdout.strip(), False
    except FileNotFoundError:
        log(f"{config['cli']} not found in PATH - skipping watchdog check")
        return None, False
    except subprocess.TimeoutExpired:
        log(f"{config['cli']} timed out after {LLM_TIMEOUT}s - skipping watchdog check")
        return None, False
    finally:
        if output_path:
            try:
                os.unlink(output_path)
            except FileNotFoundError:
                pass
            except OSError as e:
                log(f"could not remove codex last-message output: {e}")


def parse_response(raw):
    """Parse and validate the LLM JSON response."""
    if not raw:
        return None

    text = raw.strip()
    if text.startswith("```"):
        lines = text.splitlines()[1:]
        if lines and lines[-1].strip().startswith("```"):
            lines = lines[:-1]
        text = "\n".join(lines).strip()

    try:
        data = json.loads(text)
    except json.JSONDecodeError as e:
        log(f"Failed to parse LLM response as JSON: {e}")
        log(f"Raw response was: {raw[:500]}")
        return None

    if not isinstance(data, dict):
        log(f"LLM response must be a JSON object, got {type(data).__name__}")
        return None
    if not isinstance(data.get("nudge"), bool):
        log("LLM response missing boolean 'nudge'")
        return None

    note = data.get("note", "")
    if note is None:
        note = ""
    if not isinstance(note, str):
        log("LLM response field 'note' must be a string")
        return None

    return {"nudge": data["nudge"], "note": note.strip()}


def send_mail(note, tools_dir, dry_run=False):
    """Send a watchdog momentum nudge to the supervisor mailbox."""
    body = f"[WATCHDOG] {note.strip()}"
    mailbox_send = os.path.join(tools_dir, "mailbox_send.py")

    if dry_run:
        print("\n=== MAIL BODY (dry run - not sent) ===")
        print(body)
        print("=== END MAIL BODY ===\n")
        return True

    try:
        result = subprocess.run(
            [
                sys.executable,
                mailbox_send,
                "--to",
                "supervisor",
                "--from",
                "watchdog",
                "--subject",
                "Momentum nudge",
                "--body",
                body,
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            log(f"mailbox_send.py failed: {result.stderr.strip()}")
            return False
        log("Mailed supervisor a watchdog nudge")
        return True
    except Exception as e:
        log(f"Failed to send mail: {e}")
        return False


def maybe_send_nudge(decision, tools_dir, dry_run=False):
    """Send mail only when the parsed decision requests a nudge."""
    if not decision["nudge"]:
        log("No nudge needed")
        return False

    note = decision["note"]
    if not note:
        log("Nudge requested with empty note - skipping mail")
        return False

    return send_mail(note, tools_dir, dry_run=dry_run)


def _capture_windows():
    window_captures = {}
    for _idx, name in list_windows():
        content = capture_pane(name, lines=CAPTURE_LINES)
        if content is not None:
            window_captures[name] = content.strip() or "(empty)"
        else:
            log(f"Could not capture window '{name}' - skipping")
    return window_captures


def main():
    parser = argparse.ArgumentParser(
        description="Watchdog cron job: nudge the supervisor when work stalls."
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print prompt, LLM response, and would-be mail without sending mail",
    )
    parser.add_argument(
        "--self-test",
        action="store_true",
        help="Run built-in unit tests and exit",
    )
    args = parser.parse_args()

    if args.self_test:
        sys.exit(_run_self_tests())
    try:
        config = load_llm_config()
    except (ValueError, json.JSONDecodeError, OSError) as e:
        log(f"Invalid watchdog LLM config: {e}")
        _ntfy(
            title="Watchdog config invalid",
            body=f"{LLM_CONFIG_PATH} rejected: {e}. Watchdog disabled until fixed.",
        )
        sys.exit(0)

    if not session_exists():
        log("mecha tmux session not running - nothing to do")
        sys.exit(0)

    window_captures = _capture_windows()
    if not window_captures:
        log("No window content captured - nothing to do")
        sys.exit(0)

    monitored = [name for name in window_captures if name not in SYSTEM_WINDOWS]
    if not monitored:
        log("No windows to monitor - nothing to check")
        sys.exit(0)

    log(f"Checking {len(monitored)} window(s): {', '.join(monitored)}")
    prompt = build_prompt(window_captures, load_watchdog_guidance())

    if args.dry_run:
        print("\n=== PROMPT ===")
        print(prompt)
        print("=== END PROMPT ===\n")

    log(f"Calling LLM: cli={config['cli']} model={config['model']}")
    raw_response, auth_failed = call_llm(prompt, config)
    if auth_failed:
        log("Auth failure path - ntfy sent, exiting 0 to keep cron quiet")
        sys.exit(0)
    if raw_response is None:
        sys.exit(0)

    if args.dry_run:
        print("\n=== LLM RESPONSE ===")
        print(raw_response)
        print("=== END LLM RESPONSE ===\n")

    decision = parse_response(raw_response)
    if decision is None:
        sys.exit(0)

    log(f"LLM decision: nudge={decision['nudge']} note={decision['note']!r}")
    maybe_send_nudge(decision, str(SUPERVISOR_TOOLS), dry_run=args.dry_run)


def _run_self_tests():
    import tempfile
    from unittest import mock

    failures = []

    def check(name, cond, detail=""):
        status = "PASS" if cond else "FAIL"
        print(f"[{status}] {name}{(' - ' + detail) if detail and not cond else ''}")
        if not cond:
            failures.append(name)

    cfg = load_llm_config(path="/nonexistent/path/watchdog-llm.json")
    check(
        "defaults_when_missing",
        cfg == {"cli": "claude", "model": "claude-sonnet-4-6", "extra_args": []},
        str(cfg),
    )

    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"cli": "codex", "model": "gpt-x", "extra_args": ["--foo"]}, f)
        tmp_path = f.name
    try:
        cfg = load_llm_config(path=tmp_path)
        check(
            "loader_uses_overrides",
            cfg == {"cli": "codex", "model": "gpt-x", "extra_args": ["--foo"]},
            str(cfg),
        )
    finally:
        os.unlink(tmp_path)

    argv = build_llm_argv("claude", "claude-sonnet-4-6", "hello", [])
    check(
        "claude_argv",
        argv == ["claude", "-p", "hello", "--model", "claude-sonnet-4-6"],
        str(argv),
    )

    argv = build_llm_argv(
        "codex", "gpt-x", "hello", ["--quiet"], output_last_message="/tmp/out"
    )
    check(
        "codex_argv_with_output",
        argv
        == [
            "codex",
            "exec",
            "--model",
            "gpt-x",
            "--quiet",
            "--output-last-message",
            "/tmp/out",
            "hello",
        ],
        str(argv),
    )

    check(
        "auth_detect_codex",
        is_auth_failure("error: not logged in. Please run `codex login`."),
    )

    prompt = build_prompt(
        {"supervisor": "Ready when you are", "worker-1": "Task complete"},
        "Decide whether to nudge.",
    )
    check("prompt_includes_guidance", "Decide whether to nudge." in prompt)
    check("prompt_includes_window_capture", "Task complete" in prompt)

    check(
        "parse_nudge_response",
        parse_response('{"nudge": true, "note": "Check worker-1."}')
        == {"nudge": True, "note": "Check worker-1."},
    )
    check(
        "parse_silent_response",
        parse_response('```json\n{"nudge": false, "note": ""}\n```')
        == {"nudge": False, "note": ""},
    )
    check("parse_rejects_old_schema", parse_response('{"verdict": "OK"}') is None)

    with mock.patch(__name__ + ".send_mail", return_value=True) as send:
        sent = maybe_send_nudge(
            {"nudge": True, "note": "Consider the available ways forward."},
            "/tmp/tools",
        )
        check("mail_sent_when_nudge_true", sent and send.call_count == 1)

    with mock.patch(__name__ + ".send_mail", return_value=True) as send:
        sent = maybe_send_nudge({"nudge": False, "note": ""}, "/tmp/tools")
        check("mail_not_sent_when_nudge_false", (not sent) and send.call_count == 0)

    print(f"\n{len(failures)} failure(s)" if failures else "\nAll tests passed.")
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"WATCHDOG FAIL: unexpected runtime error: {error}", file=sys.stderr, flush=True)
        sys.exit(1)
