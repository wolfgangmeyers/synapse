"""Shared tmux utility module for worker management.

Provides low-level tmux operations used by the worker management scripts.
All tmux interaction is centralized here so the higher-level scripts
stay clean and consistent.
"""

import os
import re
import subprocess
import time

SESSION = "mecha"
_AGENT_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]*$")

# Claude Code shows a "pasted text #N +N lines" confirmation dialog when it
# receives large input. A longer delay before sending Enter gives Claude Code
# time to finish rendering the dialog so Enter confirms it rather than being
# swallowed as a newline.
LARGE_TEXT_THRESHOLD = 500
LARGE_TEXT_ENTER_DELAY = 5  # seconds


def run(args, check=True, capture=True):
    """Run a tmux command and return the result."""
    cmd = ["tmux"] + args
    return subprocess.run(
        cmd,
        capture_output=capture,
        text=True,
        check=check,
    )


def current_agent_id(environment=None, tmux_run=run):
    """Return this process's validated tmux window name, or None on failure."""
    environment = os.environ if environment is None else environment
    pane = environment.get("TMUX_PANE")
    if not environment.get("TMUX") or not isinstance(pane, str) or not re.fullmatch(r"%\d+", pane):
        return None
    try:
        result = tmux_run(
            ["display-message", "-p", "-t", pane, "#{window_name}"],
            check=False,
        )
    except OSError:
        return None
    window = result.stdout.strip() if result.returncode == 0 else ""
    return window if _AGENT_ID.fullmatch(window) else None


def session_exists():
    """Return True if the mecha tmux session exists."""
    result = run(["has-session", "-t", SESSION], check=False)
    return result.returncode == 0


def ensure_session():
    """Create the mecha session if it doesn't exist. Returns True if created."""
    if session_exists():
        return False
    run(["new-session", "-d", "-s", SESSION, "-n", "supervisor"])
    return True


def list_windows():
    """Return a list of (index, name) tuples for all windows in the session."""
    if not session_exists():
        return []
    result = run(
        ["list-windows", "-t", SESSION, "-F", "#{window_index}: #{window_name}"],
        check=False,
    )
    if result.returncode != 0:
        return []
    windows = []
    for line in result.stdout.strip().splitlines():
        if ": " in line:
            idx, name = line.split(": ", 1)
            windows.append((idx.strip(), name.strip()))
    return windows


def new_window(name):
    """Create a new tmux window without stealing focus from attached clients."""
    ensure_session()
    run(["new-window", "-d", "-t", SESSION, "-n", name])


def send_text(window, text):
    """Send text to a window WITHOUT pressing Enter.

    Wraps text in bracketed paste sequences (ESC[200~...ESC[201~) so TUI apps
    (Claude Code, Codex, cursor-agent) treat it as a paste event. This is what
    terminals send when a user does Ctrl+V — it works universally and bypasses
    anti-rapid-keystroke guards like cursor-agent's Z.current counter.
    """
    run(["send-keys", "-t", f"{SESSION}:{window}", "\x1b[200~"])
    time.sleep(0.05)
    run(["send-keys", "-l", "-t", f"{SESSION}:{window}", text])
    time.sleep(0.05)
    run(["send-keys", "-t", f"{SESSION}:{window}", "\x1b[201~"])


def send_enter(window, use_cr=False, use_lf=False):
    """Send Enter key to a window.

    use_cr=True sends C-m (carriage return, 0x0D) instead of the 'Enter' key name.
    use_lf=True sends C-j (line feed, 0x0A) — required for some TUI apps like cursor-agent.
    Some TUI applications use LF as the submit key and CR as newline (inverted from normal).
    """
    if use_lf:
        key = "C-j"
    elif use_cr:
        key = "C-m"
    else:
        key = "Enter"
    run(["send-keys", "-t", f"{SESSION}:{window}", key])


def send_text_and_enter(window, text, delay=5, use_cr=False, use_lf=False):
    """Send text, wait, then press Enter.

    The delay between text and Enter is critical — Claude Code treats
    immediate Enter as a soft newline instead of submitting the prompt.

    For large text (> LARGE_TEXT_THRESHOLD chars), Claude Code shows a
    "pasted text #N +N lines" confirmation dialog before accepting input.
    We use a longer delay (LARGE_TEXT_ENTER_DELAY) to give it time to
    render the dialog, so Enter confirms it rather than being swallowed.

    use_cr=True sends C-m instead of Enter — carriage return (0x0D).
    use_lf=True sends C-j instead of Enter — line feed (0x0A), required for cursor-agent TUI.
    """
    send_text(window, text)
    effective_delay = LARGE_TEXT_ENTER_DELAY if len(text) > LARGE_TEXT_THRESHOLD else delay
    time.sleep(effective_delay)
    send_enter(window, use_cr=use_cr, use_lf=use_lf)


def send_escape(window):
    """Send Escape key to cancel an in-flight operation."""
    run(["send-keys", "-t", f"{SESSION}:{window}", "Escape"])


def send_special_key(window, key):
    """Send a named special key (Down, Up, Tab, Escape, Enter, etc.) to a window.

    This is for navigating TUI prompts like Claude Code's permission dialogs.
    """
    run(["send-keys", "-t", f"{SESSION}:{window}", key])


def capture_pane(window, lines=50):
    """Capture terminal output from a window.

    Returns the captured text, or None if the window doesn't exist.
    *lines* controls how many lines of scrollback to capture.
    """
    result = run(
        ["capture-pane", "-t", f"{SESSION}:{window}", "-p", "-S", f"-{lines}"],
        check=False,
    )
    if result.returncode != 0:
        return None
    return result.stdout


def kill_window(window):
    """Kill a tmux window. Silently succeeds if the window doesn't exist."""
    run(["kill-window", "-t", f"{SESSION}:{window}"], check=False)


def window_exists(window):
    """Return True if the named window exists in the mecha session."""
    for _, name in list_windows():
        if name == window:
            return True
    return False


def window_idle_seconds(window):
    """Return seconds since last activity in the window, or None on error."""
    result = run(
        ["display-message", "-t", f"{SESSION}:{window}", "-p", "#{window_activity}"],
        check=False,
    )
    if result.returncode != 0:
        return None
    try:
        return time.time() - int(result.stdout.strip())
    except ValueError:
        return None
