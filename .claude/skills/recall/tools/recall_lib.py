#!/usr/bin/env python3
"""Shared helpers for the recall system."""

import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path


SUPERVISOR_TOOLS = Path(__file__).resolve().parents[2] / "supervisor" / "tools"
if str(SUPERVISOR_TOOLS) not in sys.path:
    sys.path.insert(0, str(SUPERVISOR_TOOLS))

from _tmux import capture_pane, window_exists


DEFAULT_DELAY_SECONDS = 60
DEFAULT_COOLDOWN_MESSAGES = 12
DEFAULT_CAPTURE_LINES = 500
DEFAULT_MODEL = "haiku"
DEFAULT_ENABLED = True
DEFAULT_SESSION_RETENTION_DAYS = 7

CLAUDE_TIMEOUT = 90
MAILBOX_SEND = SUPERVISOR_TOOLS / "mailbox_send.py"
SYSTEM_SENDER = "recall"

_FENCE_RE = re.compile(r"^```\w*\s*$", re.MULTILINE)
_LINK_RE = re.compile(r"\[[^\]]+\]\(([^)]+\.md(?:#[^)]+)?)\)")
_BARE_MD_RE = re.compile(r"(?<![\w/.-])([A-Za-z0-9_./-]+\.md)(?![\w.-])")


def now_utc():
    return datetime.now(timezone.utc)


def recall_base_dir():
    return Path(os.environ.get("MECHA_RECALL_DIR", os.path.expanduser("~/.mecha-wolfgang/recall")))


def config_file():
    return recall_base_dir() / "config.json"


def schedule_file():
    return recall_base_dir() / "scheduled.json"


def active_stores_file():
    return recall_base_dir() / "active_stores.json"


def sessions_dir():
    return recall_base_dir() / "sessions"


def isoformat_utc(dt):
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_iso8601(value):
    dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return dt.astimezone(timezone.utc)


def ensure_recall_dirs():
    recall_base_dir().mkdir(parents=True, exist_ok=True)
    sessions_dir().mkdir(parents=True, exist_ok=True)


def read_json(path, default):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)
    except FileNotFoundError:
        return default
    except json.JSONDecodeError as exc:
        raise ValueError(f"Invalid JSON in {path}: {exc}") from exc


def write_json_atomic(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2)
        f.write("\n")
    os.replace(tmp, path)


def default_config():
    return {
        "enabled": DEFAULT_ENABLED,
        "delay_seconds": DEFAULT_DELAY_SECONDS,
        "cooldown_messages": DEFAULT_COOLDOWN_MESSAGES,
        "capture_lines": DEFAULT_CAPTURE_LINES,
        "stores": [],
        "model": DEFAULT_MODEL,
        "session_retention_days": DEFAULT_SESSION_RETENTION_DAYS,
    }


def validate_store(store):
    required = ("name", "index_path", "base_path")
    missing = [field for field in required if not store.get(field)]
    if missing:
        raise ValueError(f"Store is missing required fields: {', '.join(missing)}")


def validate_config(config):
    for field in ("enabled", "delay_seconds", "cooldown_messages", "capture_lines", "stores", "model"):
        if field not in config:
            raise ValueError(f"Missing config field: {field}")

    if not isinstance(config["enabled"], bool):
        raise ValueError("enabled must be a boolean")
    for field in ("delay_seconds", "cooldown_messages", "capture_lines"):
        if not isinstance(config[field], int) or config[field] < 0:
            raise ValueError(f"{field} must be a non-negative integer")
    if "session_retention_days" in config:
        if not isinstance(config["session_retention_days"], int) or config["session_retention_days"] < 0:
            raise ValueError("session_retention_days must be a non-negative integer")
    if not isinstance(config["model"], str) or not config["model"].strip():
        raise ValueError("model must be a non-empty string")
    if not isinstance(config["stores"], list):
        raise ValueError("stores must be a list")
    seen_names = set()
    for store in config["stores"]:
        if not isinstance(store, dict):
            raise ValueError("each store must be an object")
        validate_store(store)
        if store["name"] in seen_names:
            raise ValueError(f"Duplicate store name: {store['name']}")
        seen_names.add(store["name"])


def load_config():
    ensure_recall_dirs()
    config = default_config()
    loaded = read_json(config_file(), None)
    if loaded is None:
        write_json_atomic(config_file(), config)
        return config
    if "cooldown_messages" not in loaded and "cooldown_seconds" in loaded:
        loaded["cooldown_messages"] = DEFAULT_COOLDOWN_MESSAGES
    loaded.pop("cooldown_seconds", None)
    config.update(loaded)
    validate_config(config)
    return config


def save_config(config):
    validate_config(config)
    ensure_recall_dirs()
    write_json_atomic(config_file(), config)


def load_schedule():
    ensure_recall_dirs()
    data = read_json(schedule_file(), None)
    if data is None:
        return {"scheduled": {}}
    if "scheduled" not in data or not isinstance(data["scheduled"], dict):
        raise ValueError("scheduled.json must contain an object field named 'scheduled'")
    return data


def save_schedule(data):
    if "scheduled" not in data or not isinstance(data["scheduled"], dict):
        raise ValueError("schedule data must contain a 'scheduled' object")
    ensure_recall_dirs()
    write_json_atomic(schedule_file(), data)


def schedule_window(window, delay_seconds, now=None):
    if not window:
        raise ValueError("window is required")
    now = now or now_utc()
    data = load_schedule()
    if window in data["scheduled"]:
        return False, parse_iso8601(data["scheduled"][window])
    due = now + timedelta(seconds=delay_seconds)
    data["scheduled"][window] = isoformat_utc(due)
    save_schedule(data)
    return True, due


def clear_scheduled_window(window):
    data = load_schedule()
    removed = data["scheduled"].pop(window, None)
    save_schedule(data)
    return removed is not None


def due_windows(now=None):
    now = now or now_utc()
    data = load_schedule()
    due = []
    for window, due_at in sorted(data["scheduled"].items()):
        due_dt = parse_iso8601(due_at)
        if due_dt <= now:
            due.append((window, due_dt))
    return due


def session_path(window):
    return sessions_dir() / f"{window}.json"


def load_session(window):
    path = session_path(window)
    data = read_json(path, None)
    if data is None:
        return {"window": window, "message_count": 0, "memories_recalled": {}}
    data.setdefault("window", window)
    data.setdefault("message_count", 0)
    data.setdefault("memories_recalled", {})
    return data


def save_session(window, session):
    session["window"] = window
    session.setdefault("message_count", 0)
    session.setdefault("memories_recalled", {})
    session["updated_at"] = isoformat_utc(now_utc())
    write_json_atomic(session_path(window), session)


def increment_message_count(window):
    session = load_session(window)
    session["message_count"] = session.get("message_count", 0) + 1
    save_session(window, session)
    return session["message_count"]


def recall_id(store_name, relative_path):
    return f"{store_name}:{relative_path}"


def parse_index_candidates(index_text, base_path):
    candidates = set()
    base = Path(os.path.expanduser(base_path)).resolve()

    for pattern in (_LINK_RE, _BARE_MD_RE):
        for match in pattern.findall(index_text):
            candidate = match.split("#", 1)[0].strip()
            if not candidate:
                continue
            path = (base / candidate).resolve()
            try:
                relative = path.relative_to(base).as_posix()
            except ValueError:
                continue
            if path.is_file():
                candidates.add(relative)
    return sorted(candidates)


def load_store_snapshots(config):
    stores = []
    for store in config["stores"]:
        index_path = Path(os.path.expanduser(store["index_path"]))
        base_path = Path(os.path.expanduser(store["base_path"])).resolve()
        try:
            with open(index_path, "r", encoding="utf-8") as f:
                index_text = f.read()
        except (FileNotFoundError, PermissionError, OSError):
            continue
        candidates = parse_index_candidates(index_text, str(base_path))
        stores.append({
            "name": store["name"],
            "index_path": str(index_path),
            "base_path": str(base_path),
            "index_text": index_text,
            "candidates": candidates,
            "instructions": store.get("instructions", ""),
        })
    return stores


def build_candidate_map(stores):
    mapping = {}
    for store in stores:
        for relative_path in store["candidates"]:
            rid = recall_id(store["name"], relative_path)
            mapping[rid] = {
                "id": rid,
                "store": store["name"],
                "relative_path": relative_path,
                "path": str(Path(store["base_path"]) / relative_path),
            }
    return mapping


def build_prompt(window, context, stores, candidate_map):
    """Build a (system, user) prompt pair for relevance evaluation.

    Returns a tuple of (system_prompt, user_prompt).
    """
    system = (
        "You are a memory relevance evaluator. Given a conversation context and a list "
        "of stored memories, identify which memories contain information that would be "
        "helpful RIGHT NOW — not generally useful, but specifically relevant to what the "
        "agent is currently doing or about to do.\n\n"
        "IMPORTANT: Most of the time, nothing is relevant. That is the desired outcome. "
        "Do not force matches. Only surface memories that would genuinely prevent a "
        "mistake or provide context the agent clearly needs but doesn't have.\n\n"
        "Respond with ONLY valid JSON in one of these forms:\n"
        '{"relevant": ["store:relative/path.md"]}\n'
        "or\n"
        "[]\n\n"
        "Rules:\n"
        "- Return an empty list if nothing is clearly relevant.\n"
        "- Only return IDs from the known candidate list.\n"
        "- Prefer precision over recall.\n"
        "- No explanation, no markdown fences, just the JSON."
    )

    candidate_lines = []
    for rid, item in sorted(candidate_map.items()):
        candidate_lines.append(f"- {rid} -> {item['path']}")

    store_sections = []
    for store in stores:
        lines = [
            f"=== Store: {store['name']} ===",
            f"Index path: {store['index_path']}",
        ]
        if store.get("instructions"):
            lines.append(f"Evaluation guidance: {store['instructions']}")
        lines.extend([
            "Known memory candidates:",
            *([f"- {recall_id(store['name'], rel)}" for rel in store["candidates"]] or ["- none found"]),
            "",
            "Index content:",
            store["index_text"],
        ])
        store_sections.append("\n".join(lines))

    user = "\n".join([
        f"Window: {window}",
        "",
        "## Current conversation context (recent terminal output)",
        "",
        context,
        "",
        "## Known candidate memory IDs",
        "",
        *(candidate_lines if candidate_lines else ["- none"]),
        "",
        "## Indexed memory store contents",
        "",
        *(store_sections if store_sections else ["No stores configured."]),
    ])

    return system, user


def _strip_markdown_fences(text):
    """Strip markdown code fences from model output."""
    text = text.strip()
    if text.startswith("```"):
        lines = text.splitlines()
        lines = [line for line in lines if not _FENCE_RE.match(line)]
        text = "\n".join(lines).strip()
    return text


def _parse_relevance_response(stdout):
    text = _strip_markdown_fences(stdout)
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ValueError(f"Failed to parse model response as JSON: {text[:200]}") from exc
    if isinstance(data, list):
        relevant = data
    elif isinstance(data, dict) and isinstance(data.get("relevant"), list):
        relevant = data["relevant"]
    else:
        raise ValueError("Model response must be a JSON list or object with 'relevant' list")

    normalized = []
    for item in relevant:
        if not isinstance(item, str):
            raise ValueError("Each relevant item must be a string")
        normalized.append(item)
    return normalized


def evaluate_relevance(window, context, stores, model):
    candidate_map = build_candidate_map(stores)
    if not candidate_map:
        return []

    system, user = build_prompt(window, context, stores, candidate_map)
    env = os.environ.copy()
    env.pop("CLAUDECODE", None)
    claude_bin = os.environ.get("MECHA_RECALL_CLAUDE_BIN", "claude")
    try:
        result = subprocess.run(
            [claude_bin, "-p", user, "--system-prompt", system, "--model", model],
            capture_output=True,
            text=True,
            timeout=CLAUDE_TIMEOUT,
            env=env,
        )
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError("claude CLI timed out") from exc
    except FileNotFoundError as exc:
        raise RuntimeError("claude CLI not found") from exc
    if result.returncode != 0:
        stderr = result.stderr.strip() or result.stdout.strip()[:200]
        raise RuntimeError(f"claude exited with code {result.returncode}: {stderr}")

    selected = _parse_relevance_response(result.stdout.strip())
    unknown = [item for item in selected if item not in candidate_map]
    if unknown:
        raise ValueError(f"Model returned unknown memory IDs: {', '.join(unknown)}")
    return [candidate_map[item] for item in selected]


def should_deliver(session, memory_id, cooldown_messages):
    current_count = session.get("message_count", 0)
    last = session.get("memories_recalled", {}).get(memory_id)
    if not last:
        return True
    if isinstance(last, str):
        return True
    recalled_at_count = last.get("message_count")
    if recalled_at_count is None:
        return True
    return (current_count - recalled_at_count) >= cooldown_messages


def read_memory_text(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.read().strip()


def deliver_memory(window, memory):
    """Deliver a memory via mailbox. Returns True on success, False on failure."""
    if not os.path.exists(memory["path"]):
        return False
    try:
        body = "\n".join([
            f"[RECALL] {memory['id']}",
            "",
            read_memory_text(memory["path"]),
        ]).rstrip()
        result = subprocess.run(
            [
                sys.executable,
                str(MAILBOX_SEND),
                "--to", window,
                "--from", SYSTEM_SENDER,
                "--subject", f"[RECALL] {memory['id']}",
                "--body", body,
            ],
            capture_output=True,
            text=True,
            timeout=10,
        )
        return result.returncode == 0
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
        return False


def list_session_files():
    ensure_recall_dirs()
    return sorted(sessions_dir().glob("*.json"))


def prune_sessions(older_than_days, now=None):
    now = now or now_utc()
    cutoff = now - timedelta(days=older_than_days)
    removed = []
    for path in list_session_files():
        mtime = datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc)
        if mtime < cutoff:
            path.unlink()
            removed.append(path.stem)
    return removed


def reset_state(delete_sessions=False, reset_schedule=False):
    removed_sessions = []
    if delete_sessions:
        for path in list_session_files():
            path.unlink()
            removed_sessions.append(path.stem)
    if reset_schedule:
        save_schedule({"scheduled": {}})
    return removed_sessions


def capture_window_context(window, capture_lines):
    if not window_exists(window):
        return None
    return capture_pane(window, lines=capture_lines)


def load_active_stores():
    """Load the per-window active stores mapping."""
    ensure_recall_dirs()
    return read_json(active_stores_file(), {})


def save_active_stores(data):
    """Save the per-window active stores mapping."""
    ensure_recall_dirs()
    write_json_atomic(active_stores_file(), data)


def get_active_stores_for_window(window):
    """Return list of active store names for a window, or None if no entry exists."""
    data = load_active_stores()
    return data.get(window)


def activate_store(window, store_name):
    """Activate a store for a window. Returns True if newly activated."""
    data = load_active_stores()
    stores = data.get(window, [])
    if store_name in stores:
        return False
    stores.append(store_name)
    data[window] = stores
    save_active_stores(data)
    return True


def deactivate_store(window, store_name):
    """Deactivate a store for a window. Returns True if was active."""
    data = load_active_stores()
    stores = data.get(window, [])
    if store_name not in stores:
        return False
    stores.remove(store_name)
    data[window] = stores
    save_active_stores(data)
    return True
