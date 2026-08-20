#!/usr/bin/env python3
"""Bring the mecha supervisor up from cold, and verify every step.

This is the start path after a reboot. It runs six steps in order and verifies each
one before starting the next:

  1. the Mailbox router, verified by its health endpoint answering
  2. the router web UI, verified by a 200 on loopback
  3. a tmux window in the ``mecha`` session named exactly ``supervisor``
  4. the agent in that window, plus its bootstrap prompt
  5. the mailbox sidecar, verified both as a process and as an agent the router lists
  6. a real mail delivery to the supervisor, verified by the sidecar's own ack

Step 6 is the point of the whole script. A supervisor that cannot receive mail looks
exactly like one that works: every send to it still reports ``accepted: queued``, which
says the router took the message and nothing more. So the last thing this does is send
a message and confirm it was pasted into the supervisor's window.

Running it when things are already up starts nothing twice. Each step's first act is to
ask whether its own result already holds.

The commands this sends were read off the processes running on this machine, not copied
from documentation. If they drift, read the live ones again:

    tmux list-panes -a -F '#{window_name} | #{pane_start_command}'
    ps -Ao pid,command | grep -E 'run_router|router_web_ui|claude_sidecar'

Usage:
  python3 supervisor_start.py [--dir DIR] [--agent claude|pi|codex] [--compaction-policy POLICY]

Exit codes: see `--help`, which prints EXIT_CODE_HELP below. Not repeated here on purpose —
two copies of the same table drift the moment one of them is edited alone.

Attach with: tmux attach -t mecha
"""

import argparse
import contextlib
import errno
import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from urllib.error import URLError
from urllib.parse import urlparse
from urllib.request import urlopen

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
from _sidecar import ProcessTableUnavailable, SIDECAR_SCRIPT_PATH, find_sidecar_pids, sidecar_argv
from _tmux import (
    AGENT_ID as AGENT_ID_PATTERN,
    SESSION,
    capture_pane,
    clear_input_line,
    ensure_session,
    kill_window,
    new_window,
    pane_commands,
    respawn_window,
    send_command_line,
    send_enter,
    send_text,
    send_text_and_enter,
    set_remain_on_exit,
    window_exists,
)
from worker_runtime_config import (
    WorkerRuntimeConfigError,
    load_default_agent_runtime,
    worker_runtime_config_path,
)

# realpath, not abspath. `~/.local/bin/mecha-supervisor` is a symlink to this file, and
# abspath would build sibling-script paths inside ~/.local/bin, where neither
# claude_sidecar.mjs nor mailbox_send.py exists. worker_launch.py hit this and fixed it
# the same way; this is the third copy of that path-building, so it copied the bug too.
TOOLS_DIR = os.path.dirname(os.path.realpath(__file__))
DEFAULT_DIR = os.path.expanduser("~/code/mecha-developer")
PI_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(TOOLS_DIR))))

SUPERVISOR_WINDOW = "supervisor"
ROUTER_WINDOW = "mailbox-router"
ROUTER_WEB_UI_WINDOW = "router-web-ui"

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
DEFAULT_ROUTER_DATABASE = os.path.expanduser("~/.mecha-developer/router.sqlite")
DEFAULT_ROUTER_LOG = os.path.expanduser("~/.mecha-developer/router.jsonl")
DEFAULT_ROUTER_WEB_UI_PORT = 8787
# tools/router_web_ui.mjs compiles this in as its own --router default. The live web UI is
# started without --router because of it, and this script reproduces that exactly.
WEB_UI_COMPILED_DEFAULT_ROUTER = "http://127.0.0.1:8765"

ROUTER_URL_ENV = "MECHA_ROUTER_URL"
ROUTER_DATABASE_ENV = "MECHA_ROUTER_DATABASE"
ROUTER_LOG_ENV = "MECHA_ROUTER_LOG"
ROUTER_WEB_UI_PORT_ENV = "MECHA_ROUTER_WEB_UI_PORT"
# Test-only isolation. Empty in production. When set it suffixes all three window names
# together, so a cold-start rehearsal cannot half-apply and end up driving the live
# supervisor window while it thinks it is driving a throwaway one.
WINDOW_SUFFIX_ENV = "MECHA_SUPERVISOR_START_SUFFIX"

MAILBOX_SEND_SCRIPT = os.path.join(TOOLS_DIR, "mailbox_send.py")
SIDECAR_LOG_DIR = os.path.expanduser("~/.mecha-developer/claude-sidecars")
RUN_LOCK_PATH = os.path.expanduser("~/.mecha-developer/supervisor-start.lock")

PI_PROVIDER = "openai-codex"
PI_MODEL = "gpt-5.5"
PI_MAILBOX_EXTENSION_ENV = "MECHA_PI_MAILBOX_EXTENSION"
# The registered extension is the source in this checkout. Never fall back to the
# retired copied artifact under ~/.mecha-developer/extensions.
PI_MAILBOX_EXTENSION_PATH = os.path.join(PI_REPO_ROOT, "pi-extensions", "mecha-messaging.js")
PI_COMPACTION_POLICY_ENV = "MECHA_PI_COMPACTION_POLICY"
PI_MODEL_KEY_ENV = "MECHA_PI_MODEL_KEY"

LOOPBACK_ROUTER_HOSTS = {"127.0.0.1", "::1", "localhost"}
SUPPORTED_AGENTS = ("claude", "pi", "codex")
# Runtimes whose mail delivery this script knows how to start and verify.
VERIFIABLE_AGENTS = ("claude",)
# What is actually true about mail for the runtimes this script cannot verify. Checked
# against worker_launch.py: it passes the Mailbox V2 extension on the Pi branch only, and
# starts claude_sidecar.mjs on the Claude branch only. Codex gets neither, so a Codex
# supervisor has no delivery mechanism at all and mail addressed to it is accepted by the
# router and never reaches the pane.
DELIVERY_CAVEAT = {
    "pi": "Pi receives mail through the Mailbox V2 extension passed on its launch line, "
          "which this script does not check.",
    "codex": "Codex has NO mailbox delivery on this machine: no extension and no sidecar. "
             "Mail addressed to this supervisor will be accepted by the router and will "
             "never appear in its pane.",
}
# Runtimes that do not take a first prompt on the command line, so it has to be pasted in
# after the TUI is up.
PROMPT_PASTE_AGENTS = ("claude",)

WINDOW_SUFFIX_PATTERN = re.compile(r"^[A-Za-z0-9_-]+$")
ACCEPTED_MESSAGE_PATTERN = re.compile(r"^Message (\S+) accepted: \S+", re.MULTILINE)

# Foreground process names that mean the pane is sitting at a shell with nothing running.
SHELL_COMMANDS = frozenset({
    "sh", "-sh", "bash", "-bash", "zsh", "-zsh", "fish", "-fish",
    "dash", "-dash", "ksh", "-ksh", "csh", "-csh", "tcsh", "-tcsh",
})
# Claude Code paints one of these into its status area once the TUI can take a paste. The
# foreground process name flips earlier, at exec, and a prompt sent then is swallowed.
CLAUDE_READY_MARKERS = ("? for shortcuts", "bypass permissions on")

SERVICE_TIMEOUT_SECONDS = 15.0
AGENT_START_TIMEOUT_SECONDS = 60.0
TUI_READY_TIMEOUT_SECONDS = 60.0
SIDECAR_TIMEOUT_SECONDS = 15.0
DELIVERY_TIMEOUT_SECONDS = 60.0
POLL_INTERVAL_SECONDS = 0.25

STARTUP_SENDER = "supervisor-start"
STARTUP_SUBJECT = "supervisor start check"
STARTUP_BODY = (
    "Automated delivery check from supervisor_start.py. It confirms this window can "
    "receive mail. No reply and no action needed."
)

CODEX_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the Codex supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. Launch workers through worker_launch.py, which reads this machine's managed runtime configuration."""

CLAUDE_BOOTSTRAP_PROMPT = """/supervisor

Check the current supervisor state and resume all active work. Review any in-progress tasks, check on workers, and pick up where we left off."""

PI_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. Launch workers through worker_launch.py, which reads this machine's managed runtime configuration."""


def emit(message):
    """Print a progress line, flushed.

    Flushing matters here. Errors go to stderr, which is unbuffered, while stdout is block
    buffered when this is piped or captured. Without the flush the failure message arrives
    before the steps that led to it, which reads as though the first step failed.
    """
    print(message, flush=True)


@contextlib.contextmanager
def exclusive_run_lock(path=RUN_LOCK_PATH):
    """Hold an exclusive lock for the whole run, or refuse to start.

    Two concurrent runs both see the router unhealthy, both see no window, and both create
    one, which leaves a dead duplicate window and can leave two sidecars for one agent. A
    script whose stated job includes not adding to this machine's duplicate-sidecar problem
    must not be able to create one by being run twice. Realistic, because a run that looks
    slow is exactly what makes someone start a second one.

    The lock file is a single fixed path that is never appended to, so it needs no
    retention policy.
    """
    os.makedirs(os.path.dirname(path), exist_ok=True)
    handle = open(path, "w", encoding="utf-8")
    try:
        try:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as error:
            if error.errno not in (errno.EACCES, errno.EAGAIN):
                raise
            raise ConfigurationError(
                f"another supervisor_start.py is already running (lock held on {path}). "
                "Wait for it to finish rather than racing it."
            ) from error
        yield
    finally:
        handle.close()


class ConfigurationError(ValueError):
    """Bad arguments or bad machine configuration. Exit code 2."""


class StartupError(RuntimeError):
    """A startup step could not be verified. Exit code 1."""

    def __init__(self, message, *, report=True):
        super().__init__(message)
        self.report = report


# --------------------------------------------------------------------------------------
# Configuration
# --------------------------------------------------------------------------------------

def _validate_compaction_levels(levels):
    if not isinstance(levels, list) or not levels:
        raise ValueError("compaction policy must define at least one percentage level")
    for level in levels:
        percent = level if isinstance(level, (int, float)) else level.get("percent") if isinstance(level, dict) else None
        if not isinstance(percent, (int, float)) or percent <= 0 or percent >= 100:
            raise ValueError("compaction policy percentages must be greater than 0 and less than 100")


def validate_compaction_policy(policy):
    """Validate a JSON tiered policy, JSON levels policy, or comma-list percentages for Pi."""
    if policy is None:
        return None
    policy = policy.strip()
    if not policy:
        raise ValueError("compaction policy must not be empty")
    try:
        parsed = json.loads(policy)
    except json.JSONDecodeError:
        parsed = [float(part.strip()) for part in policy.split(",")]
    if isinstance(parsed, list):
        _validate_compaction_levels(parsed)
        return policy
    if not isinstance(parsed, dict):
        raise ValueError("compaction policy must be a levels list or tiered object")
    handoff = parsed.get("postCompactionNotification")
    if handoff is not None and not isinstance(handoff, str):
        raise ValueError("compaction policy postCompactionNotification must be a string")
    models = parsed.get("models")
    if models is not None and not isinstance(models, (dict, list)):
        raise ValueError("compaction policy models must be an object or list")
    if "levels" in parsed:
        _validate_compaction_levels(parsed.get("levels"))
        return policy
    tiers = parsed.get("tiers")
    if not isinstance(tiers, list) or not tiers:
        raise ValueError("compaction policy must define default levels or at least one context-size tier")
    for tier in tiers:
        if not isinstance(tier, dict):
            raise ValueError("compaction policy tiers must be objects")
        max_context = tier.get("maxContextTokens")
        if max_context is not None and (not isinstance(max_context, int) or max_context <= 0):
            raise ValueError("compaction policy tier maxContextTokens must be a positive integer or null")
        _validate_compaction_levels(tier.get("levels"))
    return policy


def resolve_router_url(environment=None):
    """Return the router base URL, validated and normalized, without a trailing slash.

    Validation matches what the consumers actually accept, so a bad value fails here with a
    message rather than twenty seconds later in a dead tmux pane:

    - the host must be one `tools/run_router.mjs` can bind, and it binds only 127.0.0.1 and
      ::1. `localhost` is normalized to 127.0.0.1 rather than accepted and passed on, which
      would make `--host localhost` and the router would refuse to start;
    - credentials are rejected, because `tools/router_web_ui.mjs` refuses a router URL that
      carries them, and because the URL is printed, put in a sidecar's argv where `ps` shows
      it, and for Pi exported in a shell line that lands in history;
    - the port must be present and in range. Left to `urlparse`, a bad port raises a plain
      ValueError out of `.port` far from here, which `main` does not map to an exit code.
    """
    environment = os.environ if environment is None else environment
    configured_url = (environment.get(ROUTER_URL_ENV) or DEFAULT_ROUTER_URL).strip()
    parsed = urlparse(configured_url)
    if (parsed.scheme not in {"http", "https"}
            or parsed.hostname not in LOOPBACK_ROUTER_HOSTS
            or parsed.path not in {"", "/"}
            or parsed.params or parsed.query or parsed.fragment):
        raise ConfigurationError(
            f"{ROUTER_URL_ENV} must be a loopback HTTP(S) base URL without a path"
        )
    if parsed.username is not None or parsed.password is not None:
        raise ConfigurationError(f"{ROUTER_URL_ENV} must not carry credentials")
    try:
        port = parsed.port
    except ValueError as error:
        raise ConfigurationError(f"{ROUTER_URL_ENV} has an invalid port") from error
    if port is None:
        raise ConfigurationError(f"{ROUTER_URL_ENV} must name an explicit port")
    host = "127.0.0.1" if parsed.hostname == "localhost" else parsed.hostname
    bracketed = f"[{host}]" if ":" in host else host
    return f"{parsed.scheme}://{bracketed}:{port}"


def resolve_web_ui_port(environment=None):
    """Return the web UI's loopback port."""
    environment = os.environ if environment is None else environment
    raw = environment.get(ROUTER_WEB_UI_PORT_ENV)
    if raw is None or raw == "":
        return DEFAULT_ROUTER_WEB_UI_PORT
    try:
        port = int(raw)
    except ValueError as error:
        raise ConfigurationError(f"{ROUTER_WEB_UI_PORT_ENV} must be an integer") from error
    if not 1 <= port <= 65535:
        raise ConfigurationError(f"{ROUTER_WEB_UI_PORT_ENV} must be from 1 through 65535")
    return port


def resolve_router_paths(environment=None):
    """Return the (database, log) paths the router is started with.

    Validated the same way every other override in this module is: fail here, with a
    message naming the variable, rather than fail once the router process dies in its pane
    roughly fifteen seconds from now with nothing but an empty argument to explain why.
    """
    environment = os.environ if environment is None else environment
    database = _require_nonblank_path(environment, ROUTER_DATABASE_ENV, DEFAULT_ROUTER_DATABASE)
    log = _require_nonblank_path(environment, ROUTER_LOG_ENV, DEFAULT_ROUTER_LOG)
    return database, log


def _require_nonblank_path(environment, env_name, default):
    raw = environment.get(env_name)
    if raw is None:
        return default
    if not raw.strip():
        raise ConfigurationError(f"{env_name} must not be blank")
    return os.path.expanduser(raw)


def resolve_window_suffix(environment=None):
    """Return the test-isolation window suffix, or the empty string in production."""
    environment = os.environ if environment is None else environment
    suffix = environment.get(WINDOW_SUFFIX_ENV, "")
    if suffix == "":
        return ""
    if not WINDOW_SUFFIX_PATTERN.fullmatch(suffix):
        raise ConfigurationError(
            f"{WINDOW_SUFFIX_ENV} must match [A-Za-z0-9_-]+ so the window names stay valid mailbox addresses"
        )
    return suffix


def window_name(base, suffix):
    """Return the window name for *base*, with the isolation suffix applied.

    The result is re-checked against the mailbox-address pattern rather than trusted from
    the two validated inputs, because this name becomes an agent id and a name that fails
    the pattern cannot receive mail at all.
    """
    name = base if suffix == "" else f"{base}-{suffix}"
    if not AGENT_ID_PATTERN.fullmatch(name):
        raise ConfigurationError(f"window name is not a valid mailbox address: {name}")
    return name


def resolve_agent(explicit_agent, environment=None):
    """Resolve the runtime from the explicit flag or this machine's worker-runtime config.

    A missing config is an error rather than a fallback. `worker_runtime_config` falls back
    to Pi for `worker_launch.py`, and on a machine where Pi cannot run that fallback would
    quietly start a supervisor that dies on its first model call.
    """
    if explicit_agent is not None:
        if explicit_agent not in SUPPORTED_AGENTS:
            raise ConfigurationError(f"unsupported agent: {explicit_agent}")
        return explicit_agent
    environment = os.environ if environment is None else environment
    config_path = worker_runtime_config_path(environment)
    if not os.path.isfile(config_path):
        raise ConfigurationError(
            f"no managed runtime is configured: {config_path} does not exist. "
            'Create it holding {"default_agent": "claude"}, or pass --agent explicitly.'
        )
    try:
        runtime = load_default_agent_runtime(environment=environment)
    except WorkerRuntimeConfigError as error:
        raise ConfigurationError(str(error)) from error
    if runtime not in SUPPORTED_AGENTS:
        raise ConfigurationError(
            f"{config_path} selects default_agent {runtime!r}, which this script cannot "
            f"launch as a supervisor. Supported: {', '.join(SUPPORTED_AGENTS)}."
        )
    return runtime


def resolve_pi_mailbox_extension(environment=None):
    """Return the explicit override or registered checkout extension for Pi."""
    environment = os.environ if environment is None else environment
    has_override = PI_MAILBOX_EXTENSION_ENV in environment
    configured_path = environment[PI_MAILBOX_EXTENSION_ENV] if has_override else PI_MAILBOX_EXTENSION_PATH
    extension_path = os.path.abspath(os.path.expanduser(configured_path))
    if not os.path.isfile(extension_path) or os.path.islink(extension_path):
        source = PI_MAILBOX_EXTENSION_ENV if has_override else "registered checkout source"
        raise ConfigurationError(
            f"{source} must name a regular, non-symlink checkout extension file: {extension_path}. "
            "Register the checkout source before launching."
        )
    return extension_path


# --------------------------------------------------------------------------------------
# Command lines, reproduced from the live processes
# --------------------------------------------------------------------------------------

def router_command(database, log, host, port, repo_dir=DEFAULT_DIR):
    """The shell line that starts the router, matching the live `mailbox-router` pane."""
    return " ".join([
        f"cd {shlex.quote(os.path.expanduser(repo_dir))}",
        "&&",
        "node tools/run_router.mjs",
        "--database", shlex.quote(database),
        "--log", shlex.quote(log),
        "--host", shlex.quote(host),
        "--port", shlex.quote(str(port)),
    ])


def web_ui_command(port, router_url, repo_dir=DEFAULT_DIR):
    """The shell line that starts the web UI, matching the live `router-web-ui` pane.

    `--router` is appended only when the router is somewhere other than the default the web
    UI already compiles in, so a production start reproduces the live command exactly and a
    rehearsal on another port still points at the right router.
    """
    parts = [
        f"cd {shlex.quote(os.path.expanduser(repo_dir))}",
        "&&",
        "node tools/router_web_ui.mjs",
        "--port", shlex.quote(str(port)),
    ]
    if router_url.rstrip("/") != WEB_UI_COMPILED_DEFAULT_ROUTER:
        parts.extend(["--router", shlex.quote(router_url)])
    return " ".join(parts)


def agent_command(agent, prompt, pi_mailbox_extension=None, window=SUPERVISOR_WINDOW):
    if agent == "pi":
        if pi_mailbox_extension is None:
            # Resolved by the caller, from the injected environment. Falling back to a
            # second lookup here would read os.environ and quietly ignore that injection.
            raise ConfigurationError("pi requires a resolved mailbox extension path")
        return " ".join([
            "pi",
            "--provider", PI_PROVIDER,
            "--model", PI_MODEL,
            "--name", window,
            "--extension", shlex.quote(pi_mailbox_extension),
            "--approve", shlex.quote(prompt),
        ])
    if agent == "codex":
        return " ".join([
            "codex", "--no-alt-screen", "-a", "never", "-s", "danger-full-access",
            shlex.quote(prompt),
        ])
    if agent == "claude":
        return "claude --dangerously-skip-permissions"
    raise ConfigurationError(f"unsupported agent: {agent}")


def bootstrap_prompt(agent):
    return {
        "pi": PI_BOOTSTRAP_PROMPT,
        "codex": CODEX_BOOTSTRAP_PROMPT,
        "claude": CLAUDE_BOOTSTRAP_PROMPT,
    }[agent]


# --------------------------------------------------------------------------------------
# Probes
# --------------------------------------------------------------------------------------

def wait_until(predicate, timeout, sleep=time.sleep, interval=POLL_INTERVAL_SECONDS,
               now=time.monotonic):
    """Poll *predicate* until it is true or *timeout* seconds of wall clock have passed.

    The budget is real elapsed time, not time spent sleeping. Every predicate here is an
    HTTP probe with its own two-second timeout, so counting only the sleeps let a router
    that accepts a connection and then never answers stretch a fifteen-second step past two
    minutes, silently, with three more steps waiting behind it.

    The predicate is always evaluated once before any sleeping, so a condition that already
    holds costs nothing.
    """
    deadline = now() + timeout
    while True:
        if predicate():
            return True
        if now() >= deadline:
            return False
        sleep(interval)


def router_is_healthy(router_url, opener=urlopen):
    """Is the router answering its health endpoint?

    Note what this cannot tell you: /v1/health reports status, version and migrations, and
    nothing that identifies the sqlite file. A router already listening on the port against
    a different database passes this. Accepted: on the default port there is only ever one.
    """
    try:
        with opener(f"{router_url}/v1/health", timeout=2) as response:
            if response.status != 200:
                return False
            payload = json.loads(response.read(64 * 1024))
    except (OSError, URLError, TimeoutError, ValueError, json.JSONDecodeError):
        return False
    return payload.get("status") == "ok"


def web_ui_is_healthy(port, opener=urlopen):
    """Is the web UI answering 200 on loopback?

    Same limit as `router_is_healthy`, and a wider one: this accepts a 200 from whatever
    process holds the port, and does not inspect the body. Accepted for the same reason.
    """
    try:
        with opener(f"http://127.0.0.1:{port}/", timeout=2) as response:
            return response.status == 200
    except (OSError, URLError, TimeoutError, ValueError):
        return False


def router_lists_agent(router_url, agent_id, opener=urlopen):
    """Does the router list *agent_id* as a connected agent?"""
    try:
        with opener(f"{router_url}/v1/agents", timeout=2) as response:
            if response.status != 200:
                return False
            payload = json.loads(response.read(1024 * 1024))
    except (OSError, URLError, TimeoutError, ValueError, json.JSONDecodeError):
        return False
    if not isinstance(payload, dict):
        # Valid JSON, wrong shape (a bare list, a number, null). Reading `.get` off it would
        # raise AttributeError, which escapes this predicate mid-`wait_until` poll and
        # replaces this script's worded failure message with a raw traceback — the exact
        # thing the whole design exists to avoid.
        return False
    agents = payload.get("agents")
    if not isinstance(agents, list):
        return False
    return any(isinstance(agent, dict) and agent.get("agent_id") == agent_id for agent in agents)


def agent_is_running(window, panes=pane_commands):
    """Is anything other than a bare shell running in *window*?

    Every pane is consulted, not the first. This answer decides whether the launch command
    gets typed into the window, and `send-keys` without a pane suffix goes to the *active*
    pane. Reading pane 0 while writing to the active pane means a window split so a human
    can work beside the agent reads as "empty" and gets a launch command pasted into the
    running agent's prompt. Any non-shell pane counts as occupied, which is the safe
    direction for a guard whose job is to not type into a live session.

    An unreadable window yields no panes and so reads as not running. That is the right
    answer for a window that does not exist, which is the case this actually sees: the
    caller has already created the window by the time it asks.
    """
    commands = panes(window)
    return any(command not in SHELL_COMMANDS for command in commands)


def claude_tui_ready(window, capture=capture_pane):
    """Has Claude Code painted a marker that means it will accept a pasted prompt?

    The visible screen only. `capture_pane` can reach back into scrollback, and scrollback
    is exactly where a crashed or exited Claude leaves its old status line. Reading history
    makes this true on the first poll for a pane that is sitting at a shell prompt under a
    dead session's footer, and the prompt then gets pasted into the shell. Callers pair
    this with `wait_for_fresh_marker`, which additionally requires the marker to be absent
    before it is present.
    """
    text = capture(window, lines=0)
    return text is not None and any(marker in text for marker in CLAUDE_READY_MARKERS)


def wait_for_fresh_marker(window, timeout, *, ready=claude_tui_ready, sleep=time.sleep,
                          now=time.monotonic):
    """Wait for a TUI-ready marker that this launch painted, not one left in the pane.

    Requires the marker to be absent and then present. A pane whose previous agent crashed
    keeps its old status line on screen, and a check that only asks "is a marker there"
    returns true immediately, so the bootstrap prompt is pasted into a shell and lost.
    Returns False if the marker is already there and never clears, which is the ambiguous
    case and must not be reported as ready.

    *timeout* bounds the whole call, not each phase. The two phases share one deadline
    rather than each getting a fresh `timeout`, because the exact case this guard exists
    for — a stale marker sitting in the pane — is the one where the first phase burns the
    most time; without a shared deadline that phase alone can cost the full `timeout` and
    the second phase then gets another full `timeout` on top of it, so a caller that
    quoted `ready_timeout` in its own error message would be wrong by 2x, and this call
    holds the whole-script run lock for all of it.
    """
    deadline = now() + timeout
    if not wait_until(lambda: not ready(window), max(0.0, deadline - now()), sleep=sleep, now=now):
        return False
    return wait_until(lambda: ready(window), max(0.0, deadline - now()), sleep=sleep, now=now)


def read_process_argv(pid, run=subprocess.run):
    """The full argv of *pid* as a string, or an empty string when it cannot be read."""
    try:
        result = run(["ps", "-o", "args=", "-p", str(pid)], capture_output=True, text=True, check=False)
    except OSError:
        return ""
    return result.stdout.strip() if result.returncode == 0 else ""


# --------------------------------------------------------------------------------------
# Steps 1 and 2: the router and the web UI
# --------------------------------------------------------------------------------------

def ensure_service_window(window, command, is_healthy, label, *, timeout=SERVICE_TIMEOUT_SECONDS,
                          output=emit, sleep=time.sleep, now=time.monotonic,
                          send_command=send_command_line,
                          exists=window_exists, make_session=ensure_session,
                          make_window=new_window, remove_window=kill_window,
                          restart_window=respawn_window, keep_window=set_remain_on_exit):
    """Start a loopback service in its own tmux window, and verify it is answering.

    Returns True when this call started it, False when it was already healthy. Raises
    StartupError when it does not become healthy.

    *command* may be a string or a zero-argument callable returning one. The callable form
    exists so a caller can defer side effects it needs only when something is actually
    started, such as creating the router database's parent directory.

    Service windows use ``remain-on-exit`` so a process exit leaves the window in place.
    When the window already exists but the service is down, ``respawn-window`` replaces
    the pane command instead of typing into a live shell.
    """
    if is_healthy():
        output(f"{label} already up.")
        return False
    make_session()
    command = command() if callable(command) else command
    restarted = False
    if exists(window):
        keep_window(window, True)
        restart_window(window, command)
        restarted = True
    else:
        make_window(window)
        keep_window(window, True)
        send_command(window, command)
    if wait_until(is_healthy, timeout, sleep=sleep, now=now):
        if restarted:
            output(f"Restarted {label} in '{SESSION}:{window}'.")
        else:
            output(f"Started {label} in '{SESSION}:{window}'.")
        return True
    # Remove the window this call created before giving up. Left behind, it trips the
    # refusal above on the next run, and one transient failure would turn the start path
    # into one that refuses forever until a human notices and kills a window by hand.
    pane = capture_pane(window, lines=0) or ""
    remove_window(window, sweep_sidecar=False)
    tail = "\n".join(line for line in pane.splitlines() if line.strip())[-800:]
    raise StartupError(
        f"{label} did not come up within {timeout:.0f}s. The window that was created for it "
        f"has been removed, so re-running this is safe. The command was: {command}"
        + (f"\nThe pane said:\n{tail}" if tail else "")
    )


# --------------------------------------------------------------------------------------
# Steps 3 and 4: the supervisor window and its agent
# --------------------------------------------------------------------------------------

def ensure_supervisor_window(window, *, output=emit, exists=window_exists,
                             make_session=ensure_session, make_window=new_window):
    """Guarantee a window named exactly *window* exists in the mecha session.

    The session is created first, on purpose. `ensure_session` creates the session with its
    first window already named `supervisor`, and `new_window` calls `ensure_session` itself,
    so creating the window without checking afterwards produced two windows with the same
    name whenever the session did not exist yet.

    Existence is checked by enumerating window names. `tmux display-message -p -t <missing>`
    exits 0 and answers about the active pane, so it cannot report a missing window.
    """
    make_session()
    if exists(window):
        output(f"tmux window '{SESSION}:{window}' is present.")
        return False
    make_window(window)
    if not exists(window):
        raise StartupError(f"could not create tmux window '{SESSION}:{window}'.")
    output(f"Created tmux window '{SESSION}:{window}'.")
    return True


# What step 4 did, which decides both the closing message and the exit code.
AGENT_ALREADY_RUNNING = "already-running"
AGENT_BOOTSTRAPPED = "bootstrapped"
AGENT_NOT_BOOTSTRAPPED = "not-bootstrapped"


def ensure_agent(window, agent, work_dir, command, prompt=None, *, output=emit, sleep=time.sleep,
                 now=time.monotonic,
                 is_running=agent_is_running, wait_ready=wait_for_fresh_marker,
                 clear_input=clear_input_line,
                 send=send_text, enter=send_enter, send_prompt=send_text_and_enter,
                 start_timeout=AGENT_START_TIMEOUT_SECONDS, ready_timeout=TUI_READY_TIMEOUT_SECONDS,
                 pi_exports=""):
    """Launch the agent in *window*, and paste the bootstrap prompt when it needs one.

    *prompt* is None for the runtimes that take their first prompt as a launch argument, and
    a string for Claude, which does not. Sending it twice would run the bootstrap twice.

    Returns one of the AGENT_* constants. The caller needs the difference: a window that
    already held an agent got no bootstrap prompt from this run, and saying "up and
    receiving mail" without saying that would certify a supervisor that never loaded its
    skill as fully healthy.
    """
    if is_running(window):
        output(f"An agent is already running in '{SESSION}:{window}'; leaving it alone.")
        return AGENT_ALREADY_RUNNING
    full_command = f"cd {shlex.quote(work_dir)} && unset CLAUDECODE && {pi_exports}{command}"
    # Discard anything a human left half-typed at this prompt. Without it that text becomes
    # a prefix of the launch line and Enter runs the concatenation. The service windows do
    # not need this because they are always created fresh; this one is deliberately reused.
    clear_input(window)
    send(window, full_command)
    sleep(0.5)
    enter(window)
    if not wait_until(lambda: is_running(window), start_timeout, sleep=sleep, now=now):
        raise StartupError(
            f"{agent} did not start in '{SESSION}:{window}' within {start_timeout:.0f}s; the pane "
            f"is still at a shell. Check it for the error. The command was: {full_command}"
        )
    if prompt is None:
        output(f"Launched {agent} in '{SESSION}:{window}'; its bootstrap prompt was a launch argument.")
        return AGENT_BOOTSTRAPPED
    if not wait_ready(window, ready_timeout, sleep=sleep, now=now):
        output(
            f"WARNING: {agent} started in '{SESSION}:{window}' but its prompt never became ready "
            f"within {ready_timeout:.0f}s. The bootstrap prompt was NOT sent, because a prompt "
            "pasted into a TUI that is not ready is silently lost. Send this by hand, and note "
            f"that re-running this script will not send it once an agent is running:\n{prompt}"
        )
        return AGENT_NOT_BOOTSTRAPPED
    send_prompt(window, prompt)
    output(f"Launched {agent} in '{SESSION}:{window}' and sent the bootstrap prompt.")
    return AGENT_BOOTSTRAPPED


# --------------------------------------------------------------------------------------
# Step 5: the mailbox sidecar
# --------------------------------------------------------------------------------------

def ensure_sidecar(window, router_url, *, output=emit, sleep=time.sleep, now=time.monotonic,
                   find_pids=find_sidecar_pids, popen=subprocess.Popen,
                   lists_agent=router_lists_agent, argv_of=read_process_argv,
                   log_dir=SIDECAR_LOG_DIR, timeout=SIDECAR_TIMEOUT_SECONDS):
    """Guarantee exactly one mailbox sidecar for *window*, and verify it two ways.

    Verified as a process, by argv match on `--agent-id`, and verified again by asking the
    router whether it lists that agent. Neither alone is enough: a live process may be
    talking to a different router, and a router listing an agent says nothing about whether
    the paste into its window works.

    Two or more sidecars is refused rather than repaired. Duplicate sidecars deliver the
    same mail twice and racing them is not this script's call to make.
    """
    try:
        pids = find_pids(window)
    except ProcessTableUnavailable as error:
        raise StartupError(
            f"could not read the process table, so whether a sidecar for '{window}' is running "
            f"is unknown ({error}). Nothing was started. This is retryable."
        ) from error

    if len(pids) > 1:
        listed = ", ".join(str(pid) for pid in pids)
        raise StartupError(
            f"{len(pids)} mailbox sidecars are running for '{window}' (pids {listed}). Nothing was "
            "started and nothing was killed. Duplicates deliver the same mail more than once; kill "
            "all but one by hand, then re-run."
        )

    if len(pids) == 1:
        output(f"Mailbox sidecar for '{window}' already running (pid {pids[0]}).")
    else:
        popen(
            sidecar_argv(window, router_url),
            start_new_session=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            cwd=TOOLS_DIR,
        )
        # The process table can go unreadable mid-poll. Treat that as "not yet", never as
        # an exception escaping the predicate and never as "none running". The pid the poll
        # saw is carried out rather than re-read: a third read is a third chance to raise,
        # and it can come back empty if the sidecar died in between.
        seen = []

        def _one_sidecar():
            try:
                found = find_pids(window)
            except ProcessTableUnavailable:
                return False
            if len(found) != 1:
                return False
            seen[:] = found
            return True

        if not wait_until(_one_sidecar, timeout, sleep=sleep, now=now):
            raise StartupError(
                f"started a mailbox sidecar for '{window}' but exactly one was not visible after "
                f"{timeout:.0f}s. Its output is discarded, so run this in the foreground to see "
                f"why: {shlex.join(sidecar_argv(window, router_url))}"
            )
        pids = list(seen)
        output(f"Started mailbox sidecar for '{window}' (pid {pids[0]}).")

    if not wait_until(lambda: lists_agent(router_url, window), timeout, sleep=sleep, now=now):
        argvs = "; ".join(filter(None, (argv_of(pid) for pid in pids))) or "unavailable"
        raise StartupError(
            f"a mailbox sidecar process for '{window}' exists but the router at {router_url} does "
            f"not list it as a connected agent after {timeout:.0f}s. Sidecars are found by "
            "--agent-id alone, so a sidecar left over from a different router looks exactly like "
            f"a good one. Its actual command line is: {argvs}. Its own record is "
            f"{sidecar_log_path(window, log_dir)}."
        )
    output(f"Router lists '{window}' as a connected agent.")
    return pids[0]


# --------------------------------------------------------------------------------------
# Step 6: a real delivery
# --------------------------------------------------------------------------------------

def sidecar_log_path(agent_id, log_dir=SIDECAR_LOG_DIR):
    """Where `claude_sidecar.mjs` writes its body-free record for *agent_id*."""
    return os.path.join(os.path.expanduser(log_dir), f"{agent_id}.log")


def parse_accepted_message_id(stdout):
    """Pull the message id out of `mailbox_send.py`'s acceptance line."""
    match = ACCEPTED_MESSAGE_PATTERN.search(stdout or "")
    if match is None:
        raise StartupError(
            "could not read a message id out of mailbox_send.py's output, so there is nothing to "
            f"confirm delivery of. It printed: {stdout!r}"
        )
    return match.group(1)


def send_startup_message(window, router_url, *, run=subprocess.run):
    """Send the startup check message and return the id the router accepted it under."""
    command = [
        sys.executable, MAILBOX_SEND_SCRIPT,
        "--from", STARTUP_SENDER,
        "--to", window,
        "--subject", STARTUP_SUBJECT,
        "--body", STARTUP_BODY,
        "--router-url", router_url,
    ]
    result = run(command, capture_output=True, text=True, check=False)
    if result.returncode != 0:
        # Not necessarily a rejection: mailbox_send.py exits non-zero for an unreachable
        # router, a missing sender identity and a missing script file too. Name the exit
        # code and the command instead of asserting a cause. Its empty output means the
        # sender already suppressed a duplicate durable notice; keep the failure status
        # without printing a second copy here.
        detail = (result.stderr or result.stdout or '').strip()
        raise StartupError(
            f"could not send a message to '{window}'. mailbox_send.py exited "
            f"{result.returncode}: {detail}\n"
            f"The command was: {shlex.join(command)}",
            report=bool(detail),
        )
    return parse_accepted_message_id(result.stdout)


def delivery_acked(agent_id, message_id, log_dir=SIDECAR_LOG_DIR):
    """Did the sidecar report that this message was pasted into its window?

    `claude_sidecar.mjs` writes `delivery_acked` only after `inject()` returns, and `inject`
    throws when the tmux paste fails. So this line is a positive statement that the text
    reached the window, which `accepted: queued` from the send is not.

    The rotated sibling is read too: the log rotates at 256 KiB, and a rotation between the
    send and the poll would otherwise read as a delivery failure.

    Both fields are matched on one line, in either order, rather than as one fixed string.
    The sidecar writes them adjacently today only because of the key order at its `log()`
    call; a check that depends on that adjacency turns a harmless reordering there into
    this script reporting that mail is broken on a healthy machine.
    """
    pattern = re.compile(
        r"^(?=.*\bevent=delivery_acked\b)(?=.*\bmessage_id="
        + re.escape(json.dumps(message_id))
        + r")",
    )
    base = sidecar_log_path(agent_id, log_dir)
    for path in (base, f"{base}.1"):
        try:
            with open(path, "r", encoding="utf-8", errors="replace") as handle:
                if any(pattern.search(line) for line in handle):
                    return True
        except OSError:
            continue
    return False


def message_diagnostic(router_url, message_id, opener=urlopen):
    """What the router still knows about a message, for a failed delivery check."""
    try:
        with opener(f"{router_url}/v1/messages/{message_id}", timeout=2) as response:
            payload = json.loads(response.read(1024 * 1024))
    except URLError as error:
        code = getattr(error, "code", None)
        if code == 404:
            # Deliberately not "so it was delivered". The row is also gone after a TTL
            # expiry, a dead-letter dismissal, and a batch merge into another queued
            # message for the same recipient, and an id that was never stored 404s
            # identically. Printed under a failure, "it was acked" would contradict the
            # sentence around it and point away from the log that has the answer.
            return (
                "the router no longer holds it, which covers delivered-and-cleared, expired, "
                "dismissed, and merged into another queued message for this recipient"
            )
        return f"the router could not be asked about it ({error})"
    except (OSError, TimeoutError, ValueError, json.JSONDecodeError) as error:
        return f"the router could not be asked about it ({error})"
    # Same shape guard as `router_lists_agent`: valid JSON that is not an object would
    # otherwise raise out of `.get`, right where this function exists to keep the failure
    # message readable instead of a traceback.
    message = payload.get("message") if isinstance(payload, dict) else None
    state = message.get("state") if isinstance(message, dict) else None
    return f"the router still holds it in state {state!r}"


def verify_delivery(window, router_url, *, output=emit, sleep=time.sleep, now=time.monotonic,
                    send=send_startup_message, acked=delivery_acked,
                    diagnose=message_diagnostic, log_dir=SIDECAR_LOG_DIR,
                    timeout=DELIVERY_TIMEOUT_SECONDS):
    """Send a real message to the supervisor and confirm it arrived.

    This is the check the whole script exists for. Everything before it can pass while mail
    silently goes nowhere.
    """
    message_id = send(window, router_url)
    output(f"Sent startup check message {message_id} to '{window}'; waiting for the sidecar to ack it.")
    if wait_until(lambda: acked(window, message_id, log_dir), timeout, sleep=sleep, now=now):
        output(f"Delivery confirmed: message {message_id} was pasted into '{SESSION}:{window}'.")
        return message_id
    raise StartupError(
        f"message {message_id} was accepted by the router but '{window}' never acked it within "
        f"{timeout:.0f}s, so mail is NOT reaching the supervisor. Right now {diagnose(router_url, message_id)}. "
        f"The sidecar's own record is {sidecar_log_path(window, log_dir)}."
    )


# --------------------------------------------------------------------------------------
# Sequencing
# --------------------------------------------------------------------------------------

EXIT_CODE_HELP = """exit codes:
  0  every step verified, and mail was confirmed to reach the supervisor
  1  a step failed its verification
  2  bad arguments or bad machine configuration
  3  the supervisor is up, but mail delivery to it was not verified
"""


def parse_args(argv=None):
    parser = argparse.ArgumentParser(
        description="Bring the mecha supervisor up and verify it.",
        epilog=EXIT_CODE_HELP,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--dir",
        default=DEFAULT_DIR,
        help=f"Working directory for the supervisor (default: {DEFAULT_DIR})",
    )
    parser.add_argument(
        "--agent",
        choices=list(SUPPORTED_AGENTS),
        default=None,
        help="Agent to launch. Defaults to this machine's managed runtime config.",
    )
    parser.add_argument(
        "--compaction-policy",
        default=None,
        metavar="POLICY",
        help="Pi-only: context notification policy as tiered JSON, levels JSON, or comma-separated percentages.",
    )
    return parser.parse_args(argv)


def run_startup(args, environment=None, output=emit):
    """Run the six steps. Returns the process exit code."""
    environment = os.environ if environment is None else environment

    agent = resolve_agent(args.agent, environment=environment)
    if args.compaction_policy is not None and agent != "pi":
        raise ConfigurationError("--compaction-policy is only supported for Pi")
    try:
        compaction_policy = validate_compaction_policy(args.compaction_policy)
    except ValueError as error:
        raise ConfigurationError(str(error)) from error

    work_dir = os.path.expanduser(args.dir)
    if not os.path.isdir(work_dir):
        raise ConfigurationError(f"directory does not exist: {work_dir}")

    router_url = resolve_router_url(environment)
    web_ui_port = resolve_web_ui_port(environment)
    database, log = resolve_router_paths(environment)
    suffix = resolve_window_suffix(environment)
    if suffix and router_url == DEFAULT_ROUTER_URL:
        # The suffix exists so a rehearsal cannot touch live infrastructure. Namespacing the
        # windows while pointing at the live router does the opposite: it starts a second
        # real supervisor, registers it with the live router, and mails it. Refuse rather
        # than let a variable left in a shell profile do that quietly.
        raise ConfigurationError(
            f"{WINDOW_SUFFIX_ENV} is set but {ROUTER_URL_ENV} is the live router "
            f"({DEFAULT_ROUTER_URL}). A rehearsal must use a throwaway router, or it registers "
            "a second supervisor against the live one."
        )

    supervisor_window = window_name(SUPERVISOR_WINDOW, suffix)
    router_window = window_name(ROUTER_WINDOW, suffix)
    web_ui_window = window_name(ROUTER_WEB_UI_WINDOW, suffix)
    if suffix:
        output(
            f"Isolation suffix '{suffix}' is set: driving windows {router_window}, "
            f"{web_ui_window}, {supervisor_window} against {router_url}. This is NOT the live "
            "supervisor."
        )

    pi_mailbox_extension = resolve_pi_mailbox_extension(environment) if agent == "pi" else None

    parsed = urlparse(router_url)
    host = parsed.hostname
    port = parsed.port

    def start_router():
        # Created here, not at the top: on the common path where the router is already
        # healthy the database path is never used, and a typo'd override should not leave a
        # directory tree behind.
        Path(database).parent.mkdir(parents=True, exist_ok=True)
        return router_command(database, log, host, port, repo_dir=DEFAULT_DIR)

    # Step 1: the router.
    ensure_service_window(
        router_window,
        start_router,
        lambda: router_is_healthy(router_url),
        f"Mailbox router at {router_url}",
        output=output,
    )

    # Step 2: the web UI.
    ensure_service_window(
        web_ui_window,
        web_ui_command(web_ui_port, router_url, repo_dir=DEFAULT_DIR),
        lambda: web_ui_is_healthy(web_ui_port),
        f"Router web UI at http://127.0.0.1:{web_ui_port}/",
        output=output,
    )

    # Step 3: the window whose name is the supervisor's mailbox address.
    ensure_supervisor_window(supervisor_window, output=output)

    # Step 4: the agent, and its bootstrap prompt.
    pi_exports = ""
    if agent == "pi":
        pi_exports = (
            f"export {ROUTER_URL_ENV}={shlex.quote(router_url)} && "
            f"export {PI_MODEL_KEY_ENV}={shlex.quote(f'{PI_PROVIDER}/{PI_MODEL}')} && "
        )
        if compaction_policy is not None:
            pi_exports += f"export {PI_COMPACTION_POLICY_ENV}={shlex.quote(compaction_policy)} && "
    prompt = bootstrap_prompt(agent)
    agent_state = ensure_agent(
        supervisor_window,
        agent,
        work_dir,
        agent_command(agent, prompt, pi_mailbox_extension, window=supervisor_window),
        prompt if agent in PROMPT_PASTE_AGENTS else None,
        output=output,
        pi_exports=pi_exports,
    )

    if agent not in VERIFIABLE_AGENTS:
        output("")
        output(f"The supervisor is up in '{SESSION}:{supervisor_window}'.")
        output(f"Mail delivery was NOT verified. {DELIVERY_CAVEAT[agent]}")
        output(
            f"Confirm by hand that a message sent to '{supervisor_window}' appears in its pane "
            "before relying on it."
        )
        return 3

    # Step 5: the sidecar, verified as a process and as a registered agent.
    ensure_sidecar(supervisor_window, router_url, output=output)

    # Step 6: a real message, confirmed to have arrived.
    verify_delivery(supervisor_window, router_url, output=output)

    output("")
    if agent_state == AGENT_ALREADY_RUNNING and agent in PROMPT_PASTE_AGENTS:
        # Mail reaches it, which is all steps 5 and 6 proved. Whether it ever loaded the
        # supervisor skill is a different question and this run did not answer it.
        output(
            f"'{SESSION}:{supervisor_window}' is receiving mail. An agent was already running "
            "there, so this run did not send the bootstrap prompt; if it was never bootstrapped, "
            "send it by hand."
        )
        return 0
    if agent_state == AGENT_NOT_BOOTSTRAPPED:
        output(
            f"'{SESSION}:{supervisor_window}' is receiving mail, but the bootstrap prompt was "
            "never sent. See the warning above."
        )
        return 3
    output(
        f"Supervisor is up in '{SESSION}:{supervisor_window}' and receiving mail. "
        f"Attach with: tmux attach -t {SESSION}"
    )
    return 0


def main(argv=None):
    args = parse_args(argv)
    try:
        with exclusive_run_lock():
            return run_startup(args)
    except ConfigurationError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 2
    except StartupError as error:
        if error.report:
            print(f"Error: {error}", file=sys.stderr)
        return 1
    except (OSError, subprocess.SubprocessError) as error:
        print(f"Error: supervisor startup failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
