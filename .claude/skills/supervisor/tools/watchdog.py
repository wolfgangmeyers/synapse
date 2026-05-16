#!/usr/bin/env python3
"""Watchdog cron job for the mecha-developer supervisor system.

Runs every 30 minutes. Captures all tmux windows in the mecha session,
asks an LLM to identify stuck workers (idle without being told to wait),
and mails the supervisor if any are found.

The LLM CLI is configurable via ~/.mecha-developer/watchdog-llm.json:
  {"cli": "claude" | "codex", "model": "<model>", "extra_args": []}
Defaults: claude + claude-sonnet-4-6 + [].

Usage:
  python3 watchdog.py
  python3 watchdog.py --dry-run
  python3 watchdog.py --self-test

Crontab entry:
  */30 * * * * /usr/bin/python3 /home/wolfgang/code/mecha-developer/.claude/skills/supervisor/tools/watchdog.py >> ~/.mecha-developer/watchdog.log 2>&1
"""

import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import list_windows, capture_pane, session_exists

REMINDERS_TOOLS = os.path.join(
    os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
    "reminders",
    "tools",
)
sys.path.insert(0, REMINDERS_TOOLS)
from _reminders import locked_reminders, reminders_file

DEFAULT_CLI = "claude"
DEFAULT_MODEL = "claude-sonnet-4-6"
ALLOWED_CLIS = ("claude", "codex")
LLM_TIMEOUT = 90  # seconds
CAPTURE_LINES = 80
LLM_CONFIG_PATH = os.path.expanduser("~/.mecha-developer/watchdog-llm.json")
NTFY_TOPIC = "mecha-wolfgang-notify"
AUTH_ALERT_SENTINEL = os.path.expanduser("~/.mecha-developer/watchdog-auth-alerted")
AUTH_ALERT_DEBOUNCE_SEC = 6 * 60 * 60  # 6h — avoid 48 pushes/day while logged out
AUTH_FAILURE_MARKERS = (
    "not logged in",
    "please run /login",
    "please run `codex login`",
    "please run codex login",
    "run `claude /login`",
    "run claude /login",
    "/login to authenticate",
)

# Windows that are infrastructure display-only — never flag these.
SYSTEM_WINDOWS = {"dashboard"}

LOG_PREFIX = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def log(msg):
    print(f"{LOG_PREFIX} {msg}", flush=True)


def _to_utc_str(fire_at_str):
    """Convert a fire_at timestamp to UTC string for consistent comparison."""
    try:
        dt = datetime.fromisoformat(fire_at_str)
        if dt.tzinfo is None:
            # Legacy local time — convert to UTC
            dt = dt.astimezone(timezone.utc)
        return dt.strftime("%Y-%m-%dT%H:%M:%SZ")
    except (ValueError, TypeError):
        return fire_at_str


def load_watchdog_guidance():
    """Load watchdog evaluation guidance from the dedicated watchdog skill."""
    skill_path = os.path.normpath(
        os.path.join(
            os.path.dirname(os.path.abspath(__file__)),
            "..", "..", "watchdog", "SKILL.md",
        )
    )
    try:
        with open(skill_path) as f:
            content = f.read()
    except FileNotFoundError:
        log(f"Warning: watchdog SKILL.md not found at {skill_path} — using fallback guidance")
        return _fallback_guidance()

    # Strip YAML frontmatter (--- ... ---)
    if content.startswith("---"):
        end = content.find("\n---", 3)
        if end != -1:
            content = content[end + 4:].strip()

    return content


def _fallback_guidance():
    """Minimal fallback if SKILL.md cannot be loaded."""
    return (
        "A worker is STUCK if it is idle without an explicit park instruction, "
        "if it has completed its task and is sitting idle uncleaned, "
        "or if it is blocking on terminal input instead of the mailbox system. "
        "A worker is OK only if it is actively working (tool calls, spinner, output) "
        "or was explicitly told to wait. When uncertain, flag it. "
        "The supervisor window (named 'supervisor' or 'mecha') is STUCK if it "
        "contains phrases like 'Ready to proceed when you want' or 'Let me know when "
        "to continue' — these indicate the supervisor paused at an already-approved step."
    )


def _load_supervisor_state():
    """Load and parse supervisor-state.json. Returns dict or empty dict on failure."""
    state_path = os.path.expanduser("~/.mecha-developer/supervisor-state.json")
    if not os.path.isfile(state_path):
        return {}

    try:
        with open(state_path) as f:
            return json.load(f)
    except (json.JSONDecodeError, OSError):
        return {}


def load_state_notes(state=None):
    """Load watchdog_note fields from supervisor state for active tasks.

    Returns a dict of {window_or_alias: note} for tasks that have a watchdog_note.
    """
    if state is None:
        state = _load_supervisor_state()

    notes = {}
    for task in state.get("tasks", []):
        if task.get("state") != "active":
            continue
        note = task.get("watchdog_note")
        window = task.get("window", "")
        alias = task.get("alias", "")
        if note:
            if window and "worker exited" not in window:
                notes[window] = note
            if alias:
                notes[alias] = note
    return notes


def load_standing_orders(state=None):
    """Load standing orders from supervisor state.

    Returns a list of dicts with id, title, and description.
    """
    if state is None:
        state = _load_supervisor_state()

    return state.get("standing_orders", [])


def load_send_timestamps():
    """Load the last-sent timestamps from worker_send.py's sidecar file."""
    path = os.path.expanduser("~/.mecha-developer/worker_send_timestamps.json")
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def build_prompt(window_captures, guidance):
    """Build the prompt for the LLM given a dict of {window_name: content}."""
    send_timestamps = load_send_timestamps()
    now_str = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    sections = []
    for name, content in window_captures.items():
        ts = send_timestamps.get(name)
        header = f"=== Window: {name}"
        if ts:
            header += f" (last supervisor message: {ts})"
        header += f" | capture time: {now_str} ==="
        sections.append(f"{header}\n{content}")

    windows_text = "\n\n".join(sections)

    # Load supervisor state once for both notes and standing orders
    sup_state = _load_supervisor_state()

    state_notes = load_state_notes(sup_state)
    notes_section = ""
    if state_notes:
        note_lines = [f"  - {name}: {note}" for name, note in state_notes.items()]
        notes_section = (
            "\n\n--- SUPERVISOR NOTES (from task state) ---\n"
            "These notes were set by the supervisor to guide your evaluation. "
            "Follow any instructions they contain (e.g., 'keep moving' means "
            "flag the window as stuck if it appears idle so the supervisor relaunches it).\n\n"
            + "\n".join(note_lines)
            + "\n--- END SUPERVISOR NOTES ---"
        )

    # Load pending reminders for parked-worker cross-reference
    reminders_section = ""
    try:
        if reminders_file().exists():
            with locked_reminders(save=False) as reminders:
                reminders = list(reminders)
            if reminders:
                reminder_lines = [
                    f"  - [{r['id']}] window={r.get('window','?')} fires_at={_to_utc_str(r.get('fire_at','?'))} message={r.get('message','')[:100]}"
                    for r in reminders
                ]
                reminders_section = (
                    "\n\n--- PENDING REMINDERS ---\n"
                    "Cross-reference these with parked workers. A parked worker with no matching "
                    "reminder should be flagged as stuck.\n\n"
                    + "\n".join(reminder_lines)
                    + "\n--- END PENDING REMINDERS ---"
                )
        else:
            reminders_section = (
                "\n\n--- PENDING REMINDERS ---\n"
                "No reminders file found. Any parked worker without a reminder is suspect.\n"
                "--- END PENDING REMINDERS ---"
            )
    except (FileNotFoundError, json.JSONDecodeError):
        reminders_section = (
            "\n\n--- PENDING REMINDERS ---\n"
            "No reminders file found. Any parked worker without a reminder is suspect.\n"
            "--- END PENDING REMINDERS ---"
        )

    # Load standing orders
    standing_orders = load_standing_orders(sup_state)
    orders_section = ""
    if standing_orders:
        order_lines = [
            f"  - [{o['id']}] {o['title']}: {o.get('description', '')}"
            for o in standing_orders
            if o.get("id") and o.get("title")
        ]
        if order_lines:
            orders_section = (
                "\n\n--- STANDING ORDERS ---\n"
                "These are persistent directives the supervisor must honor. "
                "Flag the supervisor as STUCK if any are being violated.\n\n"
                + "\n".join(order_lines)
                + "\n--- END STANDING ORDERS ---"
            )

    prompt = f"""You are a watchdog for an AI supervisor system. AI worker agents and the supervisor run in tmux windows. System windows (dashboard) are shown for context only — never flag them. All other windows including the supervisor window are evaluated.

Below is the current terminal output from each window:

{windows_text}

--- EVALUATION INSTRUCTIONS (loaded from watchdog skill) ---

{guidance}

--- END INSTRUCTIONS ---{notes_section}{orders_section}{reminders_section}

Evaluate each non-system window (including the supervisor window) using the criteria above. Respond with ONLY valid JSON, no other text:
{{"verdict": "STUCK" or "OK", "stuck": [{{"window": "name", "reason": "brief reason why stuck"}}], "summary": "one sentence"}}"""

    return prompt


def load_llm_config(path=LLM_CONFIG_PATH):
    """Load watchdog LLM config. Returns dict with cli/model/extra_args.

    Falls back to defaults if file missing. Raises ValueError on invalid cli.
    """
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
            raise ValueError(
                f"{path}: cli must be one of {ALLOWED_CLIS}, got {cli!r}"
            )
        config["cli"] = cli
    if "model" in data:
        if not isinstance(data["model"], str) or not data["model"]:
            raise ValueError(f"{path}: model must be a non-empty string")
        config["model"] = data["model"]
    if "extra_args" in data:
        ea = data["extra_args"]
        if not isinstance(ea, list) or not all(isinstance(x, str) for x in ea):
            raise ValueError(f"{path}: extra_args must be a list of strings")
        config["extra_args"] = ea

    return config


def build_llm_argv(cli, model, prompt, extra_args):
    """Build the subprocess argv for the chosen CLI.

    claude:  claude -p <prompt> --model <model> [extra_args...]
    codex:   codex exec --model <model> [extra_args...] <prompt>
             (codex uses `exec` for non-interactive runs; prompt is positional.)
    """
    if cli == "claude":
        return ["claude", "-p", prompt, "--model", model, *extra_args]
    if cli == "codex":
        return ["codex", "exec", "--model", model, *extra_args, prompt]
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
                "-H", f"Title: {title}",
                "-d", body,
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
    """Send a loud ntfy push about the auth failure, debounced to once per 6h.

    Without debouncing, a logged-out CLI would generate one push every 30
    minutes — alert fatigue silences the channel. The sentinel is cleared
    on the next successful run.
    """
    try:
        if os.path.isfile(AUTH_ALERT_SENTINEL):
            age = time.time() - os.path.getmtime(AUTH_ALERT_SENTINEL)
            if age < AUTH_ALERT_DEBOUNCE_SEC:
                log(f"auth-failure ntfy suppressed (last alert {int(age)}s ago)")
                return
    except OSError:
        pass

    login_cmd = "claude /login" if cli == "claude" else "codex login"
    title = f"Watchdog auth failure - run `{login_cmd}`"
    body = (
        f"Watchdog could not reach {cli}: not logged in. "
        f"Run `{login_cmd}` on the supervisor host to restore monitoring."
    )
    _ntfy(title, body)

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


def call_llm(prompt, config, dry_run=False):
    """Run the configured LLM CLI with the prompt.

    Returns (stdout, auth_failed). stdout is None on any failure.
    """
    if dry_run:
        print("\n=== PROMPT ===")
        print(prompt)
        print("=== END PROMPT ===\n")
        return None, False

    argv = build_llm_argv(config["cli"], config["model"], prompt, config["extra_args"])

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
            err = stderr or stdout[:200]
            log(f"{config['cli']} exited with code {result.returncode}: {err}")
            return None, False
        clear_auth_alert_sentinel()
        return result.stdout.strip(), False
    except FileNotFoundError:
        log(f"{config['cli']} not found in PATH — skipping watchdog check")
        return None, False
    except subprocess.TimeoutExpired:
        log(f"{config['cli']} timed out after {LLM_TIMEOUT}s — skipping watchdog check")
        return None, False


def parse_response(raw):
    """Parse the LLM JSON response. Returns dict or None on failure."""
    if not raw:
        return None

    # Strip markdown code fences if present
    text = raw.strip()
    if text.startswith("```"):
        lines = text.splitlines()
        # Remove opening fence (``` or ```json)
        lines = lines[1:]
        # Remove closing fence
        if lines and lines[-1].strip().startswith("```"):
            lines = lines[:-1]
        text = "\n".join(lines).strip()

    try:
        data = json.loads(text)
        return data
    except json.JSONDecodeError as e:
        log(f"Failed to parse LLM response as JSON: {e}")
        log(f"Raw response was: {raw[:500]}")
        return None


def send_mail(stuck_workers, summary, tools_dir, dry_run=False):
    """Send a mailbox message to the supervisor about stuck windows."""
    lines = ["[WATCHDOG] Stuck windows detected:\n"]
    for w in stuck_workers:
        lines.append(f"  - {w['window']}: {w['reason']}")
    lines.append(f"\nSummary: {summary}")
    lines.append("\nCheck these windows and either redirect them or confirm they are intentionally parked.")

    body = "\n".join(lines)

    mailbox_send = os.path.join(tools_dir, "mailbox_send.py")

    if dry_run:
        print("\n=== MAIL BODY (dry run — not sent) ===")
        print(body)
        print("=== END MAIL BODY ===\n")
        return

    try:
        result = subprocess.run(
            [
                sys.executable,
                mailbox_send,
                "--to", "supervisor",
                "--from", "watchdog",
                "--subject", "Stuck windows",
                "--body", body,
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            log(f"mailbox_send.py failed: {result.stderr.strip()}")
        else:
            log(f"Mailed supervisor about {len(stuck_workers)} stuck window(s)")
    except Exception as e:
        log(f"Failed to send mail: {e}")


def main():
    parser = argparse.ArgumentParser(
        description="Watchdog cron job: detect stuck windows and alert supervisor."
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print prompt and LLM response without sending mail",
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

    tools_dir = os.path.dirname(os.path.abspath(__file__))

    # If the mecha session doesn't exist, there's nothing to watch.
    if not session_exists():
        log("mecha tmux session not running — nothing to do")
        sys.exit(0)

    windows = list_windows()
    if not windows:
        log("No windows in mecha session — nothing to do")
        sys.exit(0)

    # Capture each window
    window_captures = {}
    for _idx, name in windows:
        content = capture_pane(name, lines=CAPTURE_LINES)
        if content is not None:
            window_captures[name] = content.strip() or "(empty)"
        else:
            log(f"Could not capture window '{name}' — skipping")

    if not window_captures:
        log("No window content captured — nothing to do")
        sys.exit(0)

    # All windows except display-only system windows are monitored
    all_monitored = [n for n in window_captures if n not in SYSTEM_WINDOWS]
    if not all_monitored:
        log("No windows to monitor — nothing to check")
        sys.exit(0)

    log(f"Checking {len(all_monitored)} window(s): {', '.join(all_monitored)}")

    guidance = load_watchdog_guidance()
    prompt = build_prompt(window_captures, guidance)
    log(f"Calling LLM: cli={config['cli']} model={config['model']}")
    raw_response, auth_failed = call_llm(prompt, config, dry_run=args.dry_run)
    if auth_failed:
        log("Auth failure path — ntfy sent, exiting 0 to keep cron quiet")
        sys.exit(0)

    if args.dry_run:
        # In dry-run mode, call_llm already printed the prompt.
        # We simulate a response for display purposes.
        print("(dry-run: skipping actual LLM call and mail)")
        sys.exit(0)

    if raw_response is None:
        # Errors already logged inside call_llm — exit silently.
        sys.exit(0)

    data = parse_response(raw_response)
    if data is None:
        # parse_response already logged the error.
        sys.exit(0)

    verdict = data.get("verdict", "OK")
    stuck = data.get("stuck", [])
    summary = data.get("summary", "")

    log(f"LLM verdict: {verdict} | summary: {summary}")

    if verdict == "STUCK" and stuck:
        log(f"Stuck windows: {[w['window'] for w in stuck]}")
        send_mail(stuck, summary, tools_dir, dry_run=args.dry_run)
    else:
        log("All windows OK")


def _run_self_tests():
    """Tiny in-process tests for config loader, argv builder, and auth detection."""
    import tempfile

    failures = []

    def check(name, cond, detail=""):
        status = "PASS" if cond else "FAIL"
        print(f"[{status}] {name}{(' — ' + detail) if detail and not cond else ''}")
        if not cond:
            failures.append(name)

    # 1. Defaults when file missing
    cfg = load_llm_config(path="/nonexistent/path/watchdog-llm.json")
    check("defaults_when_missing",
          cfg == {"cli": "claude", "model": "claude-sonnet-4-6", "extra_args": []},
          str(cfg))

    # 2. Loader honors overrides
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"cli": "codex", "model": "gpt-x", "extra_args": ["--foo"]}, f)
        tmp_path = f.name
    try:
        cfg = load_llm_config(path=tmp_path)
        check("loader_uses_overrides",
              cfg == {"cli": "codex", "model": "gpt-x", "extra_args": ["--foo"]},
              str(cfg))
    finally:
        os.unlink(tmp_path)

    # 3. Invalid cli rejected
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"cli": "gemini"}, f)
        tmp_path = f.name
    try:
        rejected = False
        try:
            load_llm_config(path=tmp_path)
        except ValueError:
            rejected = True
        check("invalid_cli_rejected", rejected)
    finally:
        os.unlink(tmp_path)

    # 4. argv shape — claude
    argv = build_llm_argv("claude", "claude-sonnet-4-6", "hello", [])
    check("claude_argv",
          argv == ["claude", "-p", "hello", "--model", "claude-sonnet-4-6"],
          str(argv))

    # 5. argv shape — codex (prompt is positional, after extra_args)
    argv = build_llm_argv("codex", "gpt-x", "hello", ["--quiet"])
    check("codex_argv",
          argv == ["codex", "exec", "--model", "gpt-x", "--quiet", "hello"],
          str(argv))

    # 6. Auth-failure detection
    check("auth_detect_claude",
          is_auth_failure("Error: Not logged in. Please run /login to continue."))
    check("auth_detect_codex",
          is_auth_failure("error: not logged in. Please run `codex login`."))
    check("auth_detect_negative",
          not is_auth_failure("Error: connection refused"))
    check("auth_detect_uses_stdout",
          is_auth_failure("", "Not logged in"))

    # 7. extra_args type validation
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump({"extra_args": "not-a-list"}, f)
        tmp_path = f.name
    try:
        rejected = False
        try:
            load_llm_config(path=tmp_path)
        except ValueError:
            rejected = True
        check("invalid_extra_args_rejected", rejected)
    finally:
        os.unlink(tmp_path)

    print(f"\n{len(failures)} failure(s)" if failures else "\nAll tests passed.")
    return 1 if failures else 0


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        # Cron job — swallow all unexpected errors to avoid noise.
        print(f"{LOG_PREFIX} Unexpected error: {e}", flush=True)
        sys.exit(0)
