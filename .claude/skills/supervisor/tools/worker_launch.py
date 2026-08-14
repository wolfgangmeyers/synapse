#!/usr/bin/env python3
"""Launch a new worker in a tmux window.

Usage:
  python worker_launch.py --window WINDOW --dir DIR [--resume SESSION_ID] [--skip-permissions] [--agent pi|codex|claude|cursor] [--model MODEL] [--cursor-model MODEL] [--reasoning-effort low|medium|high|xhigh] [--notify-on-settled MAILBOX_RECIPIENT]

When --agent is omitted, the runtime comes from the machine-local worker runtime config and falls back to Pi.

Creates a tmux window, starts the agent in the specified directory,
and waits for it to initialize. Deliver instructions with mailbox_send.py only
for runtimes with verified Mailbox V2 delivery; bare Codex/Cursor need a
separate verified bridge or another proven context path.
"""

import argparse
import json
import os
import re
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid
from urllib.parse import urlparse

# Resolve symlinks: the documented launch path is
# ~/.claude/skills/supervisor/tools/worker_launch.py, where ~/.claude/skills/supervisor
# is a symlink into the repo. Sibling tool paths derived from a symlinked __file__ are
# handed to other processes, so they must be canonical.
TOOLS_DIR = os.path.dirname(os.path.realpath(__file__))

sys.path.insert(0, TOOLS_DIR)
from _sidecar import SIDECAR_SCRIPT_PATH as CLAUDE_SIDECAR_SCRIPT, sidecar_argv
from _tmux import AGENT_ID, ensure_session, new_window, send_text, send_enter, window_exists
from worker_runtime_config import (
    WorkerRuntimeConfigError,
    load_cursor_model,
    load_pi_model,
    resolve_agent_runtime,
)


PI_PROVIDER = "openai-codex"
PI_MODEL = "gpt-5.6-terra"
# Pi's own model catalog, kept up to date by Pi itself as providers and models are added or
# removed. This launcher used to hardcode PI_PROVIDER for every --model override, which broke
# the day a worker asked for a model hosted under a different provider (amazon-bedrock's
# zai.glm-5, while PI_PROVIDER stayed pinned to openai-codex): the worker registered and looked
# launched, but its first real message failed with "Provider is not configured: openai-codex"
# because that provider was never the one hosting the requested model. resolve_pi_provider()
# below looks the model up here instead of assuming it.
PI_MODELS_STORE_PATH = os.path.expanduser("~/.pi/agent/models-store.json")
CURSOR_MODEL = "glm-5.2"
CURSOR_MODEL_ENV = "MECHA_CURSOR_MODEL"
PI_MAILBOX_EXTENSION_ENV = "MECHA_PI_MAILBOX_EXTENSION"
PI_MAILBOX_EXTENSION_PATH = os.path.expanduser("~/.mecha-developer/extensions/mecha-messaging.js")
PI_ROUTER_URL_ENV = "MECHA_ROUTER_URL"
DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
PI_NOTIFY_ON_SETTLED_ENV = "MECHA_PI_NOTIFY_ON_SETTLED"
PI_COMPACTION_POLICY_ENV = "MECHA_PI_COMPACTION_POLICY"
PI_MODEL_KEY_ENV = "MECHA_PI_MODEL_KEY"
LOOPBACK_ROUTER_HOSTS = {"127.0.0.1", "::1", "localhost"}
# Unexpanded, and expanded at call time, matching the Cursor and Codex helpers so that
# redirecting HOME moves all three. Expanding here would bind the value at import.
CLAUDE_CONFIG_PATH = "~/.claude.json"
# One rolling copy of the config, overwritten on each write that changes it.
CLAUDE_CONFIG_BACKUP_SUFFIX = ".mecha-launcher-backup"
# For a config file that does not exist yet, and for the backup every time. An existing
# config keeps whatever mode it already had.
NEW_CLAUDE_CONFIG_MODE = 0o600
# Answers Claude Code's bypass permissions warning for one process. Claude Code reads
# skipDangerousModePermissionPrompt from four sources: enterprise managed settings, command line
# flags, <cwd>/.claude/settings.local.json and ~/.claude/settings.json. The flag is the only one
# that writes nothing, so it leaves no file in the user's repository and none in their home.
# Built with json.dumps so the payload cannot drift out of valid JSON, and compact because the
# whole command is pasted into a terminal.
#
# Only non-secret settings belong here. This ends up in ps output, in the worker's pane and in the
# shell history file, so anything sensitive needs a different mechanism.
CLAUDE_BYPASS_SETTINGS_JSON = json.dumps(
    {"skipDangerousModePermissionPrompt": True}, separators=(",", ":")
)


def _quote_command(argv):
    return " ".join(shlex.quote(part) for part in argv)


def pi_session_id(work_dir, window):
    """Return the stable UUID Pi uses for a worker's initial session."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"mecha-worker:{work_dir}:{window}"))


def resolve_pi_router_url():
    """Return the configured or default loopback HTTP(S) Mailbox V2 router base URL."""
    configured_url = os.environ.get(PI_ROUTER_URL_ENV) or DEFAULT_ROUTER_URL
    parsed = urlparse(configured_url)
    if (parsed.scheme not in {"http", "https"}
            or parsed.hostname not in LOOPBACK_ROUTER_HOSTS
            or parsed.path not in {"", "/"}
            or parsed.params or parsed.query or parsed.fragment):
        raise ValueError(
            f"{PI_ROUTER_URL_ENV} must be a loopback HTTP(S) base URL without a path"
        )
    return configured_url.rstrip("/")


def resolve_cursor_model(cli_model, *, environment=None):
    """Resolve Cursor's model without widening Pi's --model option."""
    environment = os.environ if environment is None else environment
    if cli_model is not None:
        model = cli_model.strip()
        if not model:
            raise ValueError("--cursor-model must not be empty")
        return model
    if CURSOR_MODEL_ENV in environment:
        model = environment[CURSOR_MODEL_ENV].strip()
        if not model:
            raise ValueError(f"{CURSOR_MODEL_ENV} must not be empty")
        return model
    return load_cursor_model(environment=environment) or CURSOR_MODEL


def resolve_pi_provider(model, models_store_path=None):
    """Return the Pi provider that hosts model, or the pinned default when unknown.

    Which provider hosts a given model id is Pi's own catalog to own, not this launcher's to
    guess, so this reads models-store.json (top level keyed by provider, each holding a
    "models" list of {"id": ..., ...}) and maps the requested model id back to the provider
    listing it. Two cases fall back to the historical PI_PROVIDER/PI_MODEL pairing instead of
    raising:

    - model is None, meaning the caller took no --model override at all. This keeps the
      pinned openai-codex/gpt-5.6-terra default.
    - model was given but the store is missing, unparsable, or simply does not list that id
      (a typo, or a model newer than the cached store). Pi itself will reject an unknown
      model id with a clearer error than this launcher could give, so failing the launch here
      would only duplicate that message worse.

    Checked against the live store on this machine: every model id appears under exactly one
    top-level provider key, and that key always matches the model entry's own "provider"
    field, so the first (and only) match is unambiguous.
    """
    if model is None:
        return PI_PROVIDER
    store_path = models_store_path or PI_MODELS_STORE_PATH
    try:
        with open(store_path, "r", encoding="utf-8") as handle:
            store = json.load(handle)
    # OSError covers a missing/unreadable file. ValueError covers a decode failure: not just
    # json.JSONDecodeError (raised on malformed JSON) but also UnicodeDecodeError (raised on
    # non-UTF-8 bytes, e.g. a store file caught mid-write), which is a ValueError subclass.
    # Either way this is Pi's catalog, not this launcher's, so a store it cannot read falls
    # back to the pinned default instead of crashing the launch.
    except (OSError, ValueError):
        return PI_PROVIDER
    if not isinstance(store, dict):
        return PI_PROVIDER
    for provider, payload in store.items():
        models = payload.get("models") if isinstance(payload, dict) else None
        if not isinstance(models, list):
            continue
        for entry in models:
            if isinstance(entry, dict) and entry.get("id") == model:
                return provider
    return PI_PROVIDER


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


def resolve_pi_mailbox_extension():
    """Return the explicit, validated Mailbox V2 extension path for Pi."""
    extension_path = (
        os.environ[PI_MAILBOX_EXTENSION_ENV]
        if PI_MAILBOX_EXTENSION_ENV in os.environ
        else PI_MAILBOX_EXTENSION_PATH
    )
    if not os.path.isfile(extension_path):
        raise ValueError(
            f"{PI_MAILBOX_EXTENSION_ENV} must name a validated extension file. "
            f"Install the canonical Pi extension first: "
            f"python3 {os.path.join(TOOLS_DIR, 'install_pi_extension.py')}"
        )
    return extension_path


def get_cursor_auth_token():
    """Read the Cursor auth token from its SQLite state DB."""
    db = os.path.expanduser("~/.config/Cursor/User/globalStorage/state.vscdb")
    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'"
    ).fetchone()
    conn.close()
    return row[0] if row else None


def _cursor_pretrust_workspace(work_dir):
    """Pre-trust a workspace directory for cursor-agent TUI mode.

    Creates ~/.cursor/projects/<slug>/.workspace-trusted to suppress the
    workspace trust dialog. Slug = path with '/' replaced by '-', leading '/' dropped.
    e.g. /home/wolfgang/code/foo → home-wolfgang-code-foo
    """
    from datetime import datetime, timezone

    slug = work_dir.lstrip("/").replace("/", "-")
    trust_dir = os.path.join(os.path.expanduser("~/.cursor/projects"), slug)
    os.makedirs(trust_dir, exist_ok=True)
    trust_file = os.path.join(trust_dir, ".workspace-trusted")
    if not os.path.exists(trust_file):
        trust_data = {
            "trustedAt": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z"),
            "workspacePath": work_dir,
        }
        with open(trust_file, "w") as f:
            json.dump(trust_data, f)


def _codex_pretrust_workspace(work_dir):
    """Pre-trust a workspace directory for Codex TUI mode.

    Codex stores workspace trust in ~/.codex/config.toml:
      [projects."/abs/path"]
      trust_level = "trusted"
    """
    config_file = os.path.join(os.path.expanduser("~/.codex"), "config.toml")
    os.makedirs(os.path.dirname(config_file), exist_ok=True)

    path_literal = json.dumps(work_dir)
    header = f"[projects.{path_literal}]"

    try:
        with open(config_file, "r") as f:
            config = f.read()
    except FileNotFoundError:
        config = ""

    table_pattern = re.compile(
        rf"(?ms)^(?P<header>\[projects\.{re.escape(path_literal)}\]\s*\n)"
        rf"(?P<body>.*?)(?=^\[|\Z)"
    )
    match = table_pattern.search(config)
    if match:
        body = match.group("body")
        if re.search(r'(?m)^\s*trust_level\s*=\s*"trusted"\s*$', body):
            return

        if re.search(r"(?m)^\s*trust_level\s*=", body):
            body = re.sub(
                r'(?m)^(\s*trust_level\s*=\s*).*$',
                r'\1"trusted"',
                body,
                count=1,
            )
        else:
            body = body.rstrip() + '\ntrust_level = "trusted"\n'

        config = config[: match.start("body")] + body + config[match.end("body") :]
    else:
        prefix = "\n\n" if config and not config.endswith("\n\n") else ""
        config = config + prefix + f'{header}\ntrust_level = "trusted"\n'

    with open(config_file, "w") as f:
        f.write(config)


def _new_claude_project_entry():
    """The fields Claude Code puts on a project it has just seen, marked trusted.

    All nine of them, taken from the defaults in 2.1.226. Claude Code backfills these only
    when the whole entry is missing, never field by field, so a partial entry written here
    stays partial for the life of the project. Measured: an entry this helper created with
    eight fields still had no mcpContextUris after a full session ran in that directory.

    A function, so every call gets its own list and dict values.
    """
    return {
        "allowedTools": [],
        "disabledMcpjsonServers": [],
        "enabledMcpjsonServers": [],
        "hasClaudeMdExternalIncludesApproved": False,
        "hasClaudeMdExternalIncludesWarningShown": False,
        "hasTrustDialogAccepted": True,
        "mcpContextUris": [],
        "mcpServers": {},
        "projectOnboardingSeenCount": 0,
    }


def _replace_file_atomically(path, data, mode):
    """Replace path with data in one step, never widening who can read it.

    mkstemp creates the temporary file 0600 and it is only ever widened as far as mode, so
    the new bytes are never more readable than the file they replace. shutil.copy2 does
    the opposite: it writes at the umask default and narrows afterwards, which leaves a
    window where a config holding account credentials is group readable.

    The fsync makes the data durable before the rename points the directory entry at it.
    The rename is atomic, so a reader never sees a partial file, and every failure above
    it leaves the original in place. The temporary file is created in the destination's
    own directory, which is what keeps the rename inside one filesystem; on a machine
    where TMPDIR is a different filesystem, the default location would fail with EXDEV.
    """
    tmp_path = None
    try:
        descriptor, tmp_path = tempfile.mkstemp(
            dir=os.path.dirname(path) or ".", prefix=".mecha-", suffix=".tmp"
        )
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
            # On the descriptor, so it cannot be redirected by a symlink swapped into the
            # directory between here and the rename.
            os.fchmod(handle.fileno(), mode)
        os.replace(tmp_path, path)
        tmp_path = None
    finally:
        # Bound to None before the try, so a mkstemp that fails on a full or read-only
        # filesystem raises its own OSError and not an UnboundLocalError from here.
        if tmp_path is not None:
            try:
                os.remove(tmp_path)
            except OSError:
                pass


def _write_claude_trust_entry(work_dir, config_path):
    """Record workspace trust for work_dir in config_path, or raise saying why not."""
    # The backup keeps the name it was asked for, while the config itself is written
    # through to the resolved target. When ~/.claude.json is a symlink into a dotfiles
    # repo, a backup beside the resolved path would drop a full copy of the user's
    # account credentials inside that repo, where the next `git add` would pick it up.
    # The temporary file still has to go in the resolved directory, because that is what
    # makes the rename atomic, so a hard kill mid-write can still strand one there.
    backup_path = config_path + CLAUDE_CONFIG_BACKUP_SUFFIX
    config_path = os.path.realpath(config_path)
    project_key = os.path.realpath(work_dir)

    try:
        with open(config_path, "rb") as handle:
            original = handle.read()
            # fstat on the open handle, so the mode belongs to the same inode the bytes
            # came from, and nothing has to stat the path a second time.
            mode = os.fstat(handle.fileno()).st_mode & 0o777
    except FileNotFoundError:
        original = None
        mode = NEW_CLAUDE_CONFIG_MODE

    config = {} if original is None else json.loads(original.decode("utf-8"))
    if not isinstance(config, dict):
        raise ValueError("its top level is not an object")
    # setdefault, because get would hand back a detached dict when the key is missing and
    # the mutation below would never reach config.
    projects = config.setdefault("projects", {})
    if not isinstance(projects, dict):
        raise ValueError("its projects value is not an object")

    # Membership, because a JSON null is a present value of a shape we do not understand
    # and .get() would report it as an absent entry and overwrite it.
    if project_key in projects:
        entry = projects[project_key]
        if not isinstance(entry, dict):
            raise ValueError(f"its entry for {project_key} is not an object")
        if entry.get("hasTrustDialogAccepted") is True:
            return
        # Copy the existing entry and set one field, so a field a later Claude Code adds
        # survives untouched.
        entry = dict(entry)
    else:
        entry = _new_claude_project_entry()
    entry["hasTrustDialogAccepted"] = True
    projects[project_key] = entry

    # indent=2 and unescaped non-ASCII are what Claude Code writes, so this rewrite does
    # not reformat the parts of the file it does not own. allow_nan=False because the
    # default emits a bare Infinity or NaN token, which is valid to json.loads and a
    # syntax error to the JSON.parse that Claude Code reads its config with. That is a
    # write that looks well formed and leaves the config unreadable to the tool that owns
    # it, so it fails closed into the warning path instead.
    payload = json.dumps(config, indent=2, ensure_ascii=False, allow_nan=False)
    _verify_the_rewrite_is_sound(original, payload, project_key)

    if original is not None:
        # Backup first, so the copy exists before anything replaces the config. Always
        # private: this is a file the user never created, holding whatever the config
        # holds, and nothing reads it back.
        _replace_file_atomically(backup_path, original, NEW_CLAUDE_CONFIG_MODE)
    _replace_file_atomically(config_path, payload.encode("utf-8"), mode)


def _verify_the_rewrite_is_sound(original, payload, project_key):
    """Refuse to write a config that lost something or that failed to do its job.

    Checks the transform, not the file. It compares the bytes about to be written against
    the bytes that were read, so a change another Claude Code session made in between is
    invisible to it. That race is accepted where this is called from.

    Two ways a write can be well formed and wrong. It can drop something, which the atomic
    rename would then make permanent and the backup only covers for one generation. Or it
    can land perfectly and not record the trust it was called to record, which is the
    failure the deleted per-project settings.json had for months: written successfully,
    read by nothing.
    """
    after = json.loads(payload)
    if after.get("projects", {}).get(project_key, {}).get("hasTrustDialogAccepted") is not True:
        raise ValueError(f"the rewrite would not mark {project_key} trusted")
    if original is None:
        return
    before = json.loads(original.decode("utf-8"))
    lost_keys = set(before) - set(after)
    if lost_keys:
        raise ValueError(f"the rewrite would drop top level keys: {sorted(lost_keys)}")
    lost_projects = set(before.get("projects", {})) - set(after.get("projects", {}))
    if lost_projects:
        raise ValueError(f"the rewrite would drop projects: {sorted(lost_projects)}")


def _claude_pretrust_workspace(work_dir, config_path=None):
    """Pre-trust a workspace directory so Claude Code skips its trust dialog.

    Claude Code opens a workspace trust dialog the first time an interactive session runs
    in a directory it has not seen. Trust lives in ~/.claude.json under
    projects.<path>.hasTrustDialogAccepted. Three things measured against 2.1.226:

    - A directory counts as trusted when the entry sits on it or on any ancestor. An entry
      on a parent suppresses the dialog for a child.
    - The paths compared are physical, so the entry has to be written under the resolved
      path. An entry under a spelling containing an unresolved symlink does not suppress
      the dialog, and the dialog itself prints the resolved path.
    - Claude Code writes its own entry under the enclosing git root when a human accepts
      the dialog. An exact entry on the work directory is found first by the ancestor walk,
      so it works either way.

    Not to be confused with skipDangerousModePermissionPrompt, which records that the
    bypass permissions warning has been accepted and has nothing to do with trust.

    Any failure is a warning, never an exception. This runs before the tmux window is
    created, so an escaping exception would mean no worker at all, which is worse than the
    dialog it exists to avoid. The catch is deliberately broad: the contract is that the
    launch survives whatever goes wrong in here.

    Known and accepted: Claude Code sessions write ~/.claude.json while they run, so this
    read-modify-write can lose a field one of them set between the read here and the
    rename. The window covers two writes with an fsync each, the launcher writes at most
    once per launch and not at all once the directory is trusted, and a lock would not
    help because Claude Code takes none.
    """
    config_path = config_path or os.path.expanduser(CLAUDE_CONFIG_PATH)
    try:
        _write_claude_trust_entry(work_dir, config_path)
    except Exception as error:
        print(
            f"Warning: could not record workspace trust for {work_dir} in {config_path}: "
            f"{error}. The worker may show Claude Code's workspace trust dialog, which "
            f"discards pasted text, so answer it before sending the worker its first "
            f"message.",
            file=sys.stderr,
        )


def main():
    parser = argparse.ArgumentParser(
        description="Launch a managed worker in a new tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument("--dir", required=True, help="Working directory for the agent")
    parser.add_argument("--resume", default=None, help="Session ID to resume")
    parser.add_argument(
        "--skip-permissions",
        action="store_true",
        default=True,
        help="Use --dangerously-skip-permissions flag (default: True)",
    )
    parser.add_argument(
        "--no-skip-permissions",
        action="store_false",
        dest="skip_permissions",
        help="Do NOT use --dangerously-skip-permissions flag",
    )

    parser.add_argument(
        "--init",
        action="store_true",
        default=False,
        help="Run 'mise trust' and 'winit' in the working directory before launching (for new worktrees)",
    )

    parser.add_argument(
        "--agent",
        default=None,
        choices=["pi", "claude", "codex", "cursor"],
        help="Agent to launch. Overrides the machine-local default runtime config.",
    )

    parser.add_argument(
        "--message",
        default=None,
        help="Unused; deliver instructions through Mailbox V2. Kept for backwards compatibility.",
    )
    parser.add_argument(
        "--model",
        default=None,
        metavar="MODEL",
        help=(
            "Pi-only model override for this worker launch. Defaults to gpt-5.6-terra on provider "
            "openai-codex; an explicit override's provider is looked up in "
            "~/.pi/agent/models-store.json, falling back to openai-codex if the model is not "
            "listed there."
        ),
    )
    parser.add_argument(
        "--cursor-model",
        default=None,
        metavar="MODEL",
        help=f"Cursor-only model override for this worker launch. Defaults to {CURSOR_MODEL}.",
    )
    parser.add_argument(
        "--reasoning-effort",
        choices=["low", "medium", "high", "xhigh"],
        default=None,
        help="Codex-only reasoning effort override for this worker launch.",
    )
    parser.add_argument(
        "--notify-on-settled",
        default=None,
        metavar="MAILBOX_RECIPIENT",
        help="Pi-only: notify this Mailbox agent ID after the worker settles idle.",
    )
    parser.add_argument(
        "--compaction-policy",
        default=None,
        metavar="POLICY",
        help="Pi-only: context notification policy as tiered JSON, levels JSON, or comma-separated percentages.",
    )

    args = parser.parse_args()
    try:
        args.agent = resolve_agent_runtime(args.agent)
    except WorkerRuntimeConfigError as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)

    if args.notify_on_settled is not None:
        if args.agent != "pi":
            print("Error: --notify-on-settled is only supported for Pi workers", file=sys.stderr)
            sys.exit(1)
        if AGENT_ID.fullmatch(args.notify_on_settled) is None:
            print("Error: --notify-on-settled must be a valid Mailbox agent ID", file=sys.stderr)
            sys.exit(1)
    if args.compaction_policy is not None and args.agent != "pi":
        print("Error: --compaction-policy is only supported for Pi workers", file=sys.stderr)
        sys.exit(1)
    if args.model is not None:
        if args.agent != "pi":
            print("Error: --model is only supported for Pi workers", file=sys.stderr)
            sys.exit(1)
        args.model = args.model.strip()
        if not args.model:
            print("Error: --model must not be empty", file=sys.stderr)
            sys.exit(1)
    pi_model = None
    pi_provider = None
    if args.agent == "pi":
        try:
            configured_pi_model = load_pi_model()
        except WorkerRuntimeConfigError as error:
            print(f"Error: {error}", file=sys.stderr)
            sys.exit(1)
        pi_model = args.model or configured_pi_model or PI_MODEL
        pi_provider = resolve_pi_provider(pi_model)
    cursor_model = None
    if args.agent == "cursor":
        try:
            cursor_model = resolve_cursor_model(args.cursor_model)
        except ValueError as error:
            print(f"Error: {error}", file=sys.stderr)
            sys.exit(1)
    try:
        compaction_policy = validate_compaction_policy(args.compaction_policy)
    except ValueError as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)

    # Deliberately abspath, not realpath: work_dir is the caller's spelling of the
    # worktree and feeds pi_session_id(), the tmux cd, and the Cursor and Codex trust
    # keys. Resolving it would silently change existing session IDs and those trust keys.
    # The Claude trust key is resolved separately inside _claude_pretrust_workspace(),
    # because Claude Code looks a project up by its resolved physical path.
    work_dir = os.path.abspath(os.path.expanduser(args.dir))
    pi_mailbox_extension = None
    mailbox_router_url = None
    if args.agent in {"pi", "claude"}:
        try:
            if args.agent == "pi":
                pi_mailbox_extension = resolve_pi_mailbox_extension()
            mailbox_router_url = resolve_pi_router_url()
        except ValueError as error:
            print(f"Error: {error}", file=sys.stderr)
            sys.exit(1)

    if not os.path.isdir(work_dir):
        print(f"Error: directory does not exist: {work_dir}", file=sys.stderr)
        sys.exit(1)

    if window_exists(args.window):
        print(f"Error: window '{args.window}' already exists", file=sys.stderr)
        sys.exit(1)

    # Initialize worktree if requested (mise trust + winit)
    if args.init:
        print(f"Initializing worktree at {work_dir}...")
        subprocess.run(["mise", "trust"], cwd=work_dir, capture_output=True)
        winit_result = subprocess.run(["winit"], cwd=work_dir, capture_output=True, text=True)
        if winit_result.returncode != 0 and winit_result.stderr:
            print(f"  winit warning: {winit_result.stderr.strip()}", file=sys.stderr)
        else:
            print("  Worktree initialized (mise trust + winit).")

    # Build the agent command
    if args.agent == "pi":
        parts = [
            "pi",
            "--provider",
            pi_provider,
            "--model",
            pi_model,
            "--name",
            args.window,
        ]
        # Use Pi's default configured extensions and add the managed-worker
        # Mailbox V2 extension explicitly so mailbox routing is always present.
        parts.extend([
            "--extension", pi_mailbox_extension,
        ])
        # Pi has no permission mode. --approve makes the trusted project resources
        # available without a startup prompt, matching the existing launcher default.
        if args.skip_permissions:
            parts.append("--approve")
        # Pi's --resume opens an interactive picker; --session accepts the supplied
        # session path or ID and is the supported non-interactive resume mapping.
        if args.resume:
            parts.extend(["--session", args.resume])
        else:
            parts.extend(["--session-id", pi_session_id(work_dir, args.window)])
        agent_cmd = _quote_command(parts)
    elif args.agent == "codex":
        _codex_pretrust_workspace(work_dir)
        parts = ["codex", "--no-alt-screen"]
        if args.reasoning_effort:
            parts.extend(["-c", f'model_reasoning_effort="{args.reasoning_effort}"'])
        if args.skip_permissions:
            parts.extend(["-a", "never", "-s", "danger-full-access"])
        if args.resume:
            parts.extend(["resume", args.resume])
        agent_cmd = _quote_command(parts)
    elif args.agent == "cursor":
        # Cursor agent: interactive TUI mode.
        token = get_cursor_auth_token()
        if not token:
            print("Error: could not read Cursor auth token from state.vscdb", file=sys.stderr)
            sys.exit(1)

        # Pre-trust the workspace to suppress the "do you trust this workspace?" dialog.
        _cursor_pretrust_workspace(work_dir)

        cursor_bin = os.path.expanduser("~/.local/bin/cursor-agent")
        parts = [f"export CURSOR_AUTH_TOKEN={shlex.quote(token)}"]

        # TUI mode: interactive session; worker_launch.py does not provide a
        # Mailbox V2 delivery bridge for Cursor.
        agent_parts = [cursor_bin, "--yolo", "--model", cursor_model]
        if args.resume:
            agent_parts.extend(["--resume", args.resume])

        parts.append(_quote_command(agent_parts))
        agent_cmd = " && ".join(parts)
    else:
        # Default: Claude Code, and only Claude Code. Both --agent and the machine-local
        # runtime config validate against the same four names, and the branches above
        # take pi, codex and cursor.
        #
        # Mark the work directory trusted before the agent starts. Claude Code opens a
        # workspace trust dialog the first time an interactive session runs in a directory
        # it has not seen. That dialog discards pasted text and reads Enter as "Yes, I
        # trust this folder", so the mailbox injector's first message, normally the
        # worker's brief, is lost and trust is granted by an automated keystroke.
        #
        # Trust is not skipDangerousModePermissionPrompt. That key records that the bypass
        # permissions warning has been accepted, and Claude Code reads it from
        # ~/.claude/settings.json, <cwd>/.claude/settings.local.json, CLI flags and
        # enterprise managed settings. It never reads ~/.claude/projects/<slug>/, which
        # holds session transcripts and memory. The two dialogs are answered separately:
        # trust by the config entry written just below, and the bypass permissions warning
        # by --settings on the command line.
        _claude_pretrust_workspace(work_dir)

        parts = ["claude"]
        if args.skip_permissions:
            # The second dialog, and the dangerous one. Measured against 2.1.226: a launch
            # with no recorded answer stops at a warning whose highlighted default is "No,
            # exit", so the injector's blind Enter exits the worker and takes its first
            # message with it. That reads as a crash, not as a delivery problem.
            #
            # --settings adds a settings source and does not replace the user's. Measured
            # with a SessionStart hook defined only in ~/.claude/settings.json: it fired on
            # a run with the flag and on a run without it, so the mailbox and activity
            # hooks keep working. Watch for that if this ever stops holding, because the
            # symptom is an activity dot that never appears and nothing logged.
            #
            # A flag outranks the work directory's own settings. Measured: a work directory
            # holding {"skipDangerousModePermissionPrompt": false} in
            # .claude/settings.local.json still started with no dialog. That is wanted,
            # because the same launch is passing --dangerously-skip-permissions and the two
            # belong together. Enterprise managed policy outranks the flag, so a policy
            # recording the key as false would still show the dialog. On 2026-08-09 there
            # was no managed policy file on this machine, at the macOS path
            # /Library/Application Support/ClaudeCode/.
            #
            # A Claude Code old enough to not know --settings would exit on an unrecognized
            # option and no worker would start. Nothing here waits on the process, so the
            # only place that shows is the pane, sitting back at a shell prompt.
            parts.extend([
                "--dangerously-skip-permissions", "--settings", CLAUDE_BYPASS_SETTINGS_JSON,
            ])
        if args.resume:
            parts.extend(["--resume", args.resume])
        # Quoted per part, because the settings payload is JSON and the whole command is
        # pasted into a shell. The payload carries no spaces, so unquoted it survives word
        # splitting and loses its double quotes instead, and Claude Code is handed a
        # settings value it cannot parse. Add a second key and the braces start expanding
        # too, which splits it for real.
        agent_cmd = _quote_command(parts)

    created = ensure_session()
    if created:
        print("Created mecha tmux session.")

    new_window(args.window)

    # Change to the worktree and start the agent. Its tmux window name is its identity.
    # For cursor: agent_cmd already contains its authentication export.
    if args.agent == "cursor":
        full_cmd = f"cd {work_dir} && {agent_cmd}"
    else:
        pi_exports = ""
        if args.agent == "pi":
            pi_exports = f"export {PI_ROUTER_URL_ENV}={shlex.quote(mailbox_router_url)} && "
            if args.notify_on_settled is not None:
                pi_exports += (
                    f"export {PI_NOTIFY_ON_SETTLED_ENV}="
                    f"{shlex.quote(args.notify_on_settled)} && "
                )
            pi_exports += f"export {PI_MODEL_KEY_ENV}={shlex.quote(f'{pi_provider}/{pi_model}')} && "
            if compaction_policy is not None:
                pi_exports += (
                    f"export {PI_COMPACTION_POLICY_ENV}="
                    f"{shlex.quote(compaction_policy)} && "
                )
        if args.agent == "claude":
            # Built by _sidecar, which also owns the matcher that has to recognise it.
            # Spelling the tokens out here once let the launcher and the finder drift.
            sidecar_cmd = _quote_command(sidecar_argv(args.window, mailbox_router_url))
            # The sidecar is backgrounded inside an explicit group so `&` binds only
            # the node command. Without the group, bash attaches `&` to the whole
            # preceding AND-list, which would background `cd`/`unset` and start the
            # agent in the wrong directory, and would leave $! pointing at the
            # subshell instead of the sidecar.
            agent_cmd = (
                f"{{ {sidecar_cmd} >/dev/null 2>&1 & }} && "
                f"sidecar_pid=$! && "
                f"trap 'kill $sidecar_pid >/dev/null 2>&1 || true' EXIT INT TERM && "
                f"{agent_cmd}"
            )
        full_cmd = (
            f"cd {work_dir} && unset CLAUDECODE && "
            f"{pi_exports}{agent_cmd}"
        )
    send_text(args.window, full_cmd)
    time.sleep(0.5)
    send_enter(args.window)

    # Cursor TUI needs ~10s to fully initialize before accepting input
    if args.agent == "cursor":
        time.sleep(10)

    action = "resumed" if args.resume else "launched"
    print(f"Worker ({args.agent}) {action} in window '{args.window}' at {work_dir}")
    print()
    print("--- Next steps ---")
    if args.agent == "pi":
        print("1. Verify the Pi worker registered with Mailbox V2, then inspect its status if needed:")
    elif args.agent == "claude":
        print("1. Verify the Claude sidecar registered with Mailbox V2, then inspect the worker pane if needed:")
    else:
        print("1. Verify the worker is running, then inspect its status if needed:")
    print(f"   python3 worker_capture.py --window {args.window}")
    if args.agent in {"pi", "claude"}:
        print("2. Before sending instructions, read ~/.agents/skills/worker-launch/SKILL.md.")
        print("   Deliver instructions through the verified Mailbox V2 path and explicitly tell the worker")
        print("   to use bounded background subagents for focused exploration, implementation slices,")
        print("   validation/log triage, and robot-review where useful:")
        print("   python3 mailbox_send.py --from supervisor \\")
        print(f"     --to {args.window} --body 'Read ~/.agents/skills/worker/SKILL.md first, then read TASK.md for your instructions. Use bounded background subagents proactively for focused exploration, implementation slices, validation/log triage, and robot-review where useful; keep them bounded and retain final judgment.'")
    else:
        print("2. This runtime has no launcher-provided Mailbox V2 delivery bridge; use only a verified delivery bridge or a relaunch context proven to reach the session.")
    print("3. Record the task in supervisor state:")
    if args.agent == "pi":
        print(
            f"   python3 state_add.py --alias <alias> --type worker --agent pi "
            f"--session {pi_session_id(work_dir, args.window)} ..."
        )
    elif args.resume:
        print(f"   python3 state_add.py --alias <alias> --type worker --agent {args.agent} --session {args.resume} ...")
    else:
        print(f"   python3 state_add.py --alias <alias> --type worker --agent {args.agent} ...")
    print()
    print("If the recipient is not registered or is not running, restore it through")
    print("the managed worker lifecycle/worker_launch.py. Do not type into its terminal or tmux pane.")
    print("Before shutting down, verify the work is correct (background subagent).")


if __name__ == "__main__":
    main()
