"""Shared utility module for supervisor state management.

Provides loading, saving, querying, and rendering of the supervisor
state stored in ~/.mecha-developer/supervisor-state.json.
"""

import contextlib
import fcntl
import json
import os
from datetime import datetime, timezone

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

STATE_FILE = os.environ.get(
    "MECHA_STATE_FILE",
    os.path.expanduser("~/.mecha-developer/supervisor-state.json"),
)

REQUIRED_FIELDS = ["alias", "type", "project", "description"]

OPTIONAL_FIELDS = [
    "window",
    "agent",
    "branch",
    "worktree",
    "pr",
    "linear",
    "dev_environment",
    "idle_notify",
    "purpose",
    "requirements",
]

STATE_FIELDS = [
    "last_checked",
    "last_summary",
    "blocked",
    "blocked_reason",
    "completed",
    "result",
    "abandoned",
    "reason",
]

ALL_FIELDS = REQUIRED_FIELDS + OPTIONAL_FIELDS + STATE_FIELDS + ["id", "state"]

# ---------------------------------------------------------------------------
# Field-key to human-readable label mapping
# ---------------------------------------------------------------------------

FIELD_LABELS = {
    "type": "Type",
    "project": "Project",
    "window": "Window",
    "agent": "Agent",
    "branch": "Branch",
    "worktree": "Worktree",
    "pr": "PR",
    "linear": "Linear",
    "description": "Description",
    "dev_environment": "Dev environment",
    "idle_notify": "Idle notify",
    "purpose": "Purpose",
    "requirements": "Requirements",
    "last_checked": "Last checked",
    "last_summary": "Last summary",
    "blocked": "Blocked",
    "blocked_reason": "Blocked reason",
    "completed": "Completed",
    "result": "Result",
    "abandoned": "Abandoned",
    "reason": "Reason",
    "remote": "Remote",
}

# ---------------------------------------------------------------------------
# Ordered field lists per task state
# ---------------------------------------------------------------------------

ACTIVE_FIELDS = [
    "type",
    "project",
    "window",
    "agent",
    "branch",
    "worktree",
    "pr",
    "linear",
    "description",
    "purpose",
    "requirements",
    "dev_environment",
    "idle_notify",
    "last_checked",
    "last_summary",
]

BLOCKED_FIELDS = [
    "type",
    "project",
    "window",
    "agent",
    "branch",
    "worktree",
    "pr",
    "linear",
    "description",
    "purpose",
    "requirements",
    "dev_environment",
    "idle_notify",
    "blocked",
    "blocked_reason",
    "last_checked",
    "last_summary",
]

COMPLETED_FIELDS = [
    "type",
    "project",
    "completed",
    "result",
]

ABANDONED_FIELDS = [
    "type",
    "project",
    "abandoned",
    "reason",
]

REMOTE_WORKER_FIELDS = [
    "type",
    "project",
    "description",
    "purpose",
    "requirements",
    "remote",
    "last_checked",
    "last_summary",
]

FIELDS_BY_STATE = {
    "active": ACTIVE_FIELDS,
    "blocked": BLOCKED_FIELDS,
    "completed": COMPLETED_FIELDS,
    "abandoned": ABANDONED_FIELDS,
}

# ---------------------------------------------------------------------------
# Core I/O helpers
# ---------------------------------------------------------------------------


def load_state():
    """Read and return the JSON state.

    If the file or its parent directory does not exist, return a blank state
    with ``next_task_id`` set to 1 and an empty tasks list.
    """
    if not os.path.isfile(STATE_FILE):
        return {"next_task_id": 1, "tasks": [], "standing_orders": []}
    with open(STATE_FILE, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    # Ensure standing_orders key exists for older state files.
    if "standing_orders" not in data:
        data["standing_orders"] = []
    return data


def save_state(state):
    """Atomically write *state* to the JSON file, pretty-printed with indent=2.

    Writes to ``<STATE_FILE>.tmp``, fsyncs, then ``os.replace``s into place so
    a concurrent reader never sees a partial file. Creates the parent
    directory if it does not already exist.
    """
    directory = os.path.dirname(STATE_FILE)
    os.makedirs(directory, exist_ok=True)
    tmp_path = STATE_FILE + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as fh:
        json.dump(state, fh, indent=2)
        fh.write("\n")
        fh.flush()
        os.fsync(fh.fileno())
    os.replace(tmp_path, STATE_FILE)


@contextlib.contextmanager
def locked_state():
    """Yield the state under an exclusive ``fcntl.LOCK_EX`` advisory lock.

    Acquires the lock on a sentinel ``<STATE_FILE>.lock`` file, loads the
    current state, yields it for mutation, and on normal exit writes it back
    atomically via ``save_state``. The lock is always released. If the
    ``with`` block raises, the state is NOT saved.
    """
    directory = os.path.dirname(STATE_FILE)
    os.makedirs(directory, exist_ok=True)
    lock_path = STATE_FILE + ".lock"
    with open(lock_path, "a+", encoding="utf-8") as lock_fh:
        fcntl.flock(lock_fh.fileno(), fcntl.LOCK_EX)
        try:
            state = load_state()
            yield state
            save_state(state)
        finally:
            fcntl.flock(lock_fh.fileno(), fcntl.LOCK_UN)


def create_task(state, alias, task_type, project, description, **kwargs):
    """Create a new task dict and append it to state.

    Returns the new task dict. Accepts any optional/state fields as kwargs.
    Automatically assigns the next task ID and sets last_checked.
    """
    task_id = state.get("next_task_id", 1)
    task = {
        "id": task_id,
        "alias": alias,
        "state": "active",
        "type": task_type,
        "project": project,
        "description": description,
        "last_checked": now_timestamp(),
    }
    # Apply optional and state fields from kwargs
    for field in OPTIONAL_FIELDS + STATE_FIELDS:
        if field in kwargs:
            task[field] = kwargs[field]
    # Allow 'remote' block to be passed directly
    if "remote" in kwargs:
        task["remote"] = kwargs["remote"]
    state["tasks"].append(task)
    state["next_task_id"] = task_id + 1
    return task


# ---------------------------------------------------------------------------
# Query helpers
# ---------------------------------------------------------------------------


def find_task(state, identifier):
    """Find a task by *identifier*.

    *identifier* may be an ``int`` (matched against the task ``id``) or a
    ``str`` (matched against the task ``alias``).  Returns the task dict or
    ``None`` if no match is found.
    """
    for task in state.get("tasks", []):
        if isinstance(identifier, int) and task.get("id") == identifier:
            return task
        if isinstance(identifier, str) and task.get("alias") == identifier:
            return task
    return None


def find_task_by_window(state, window):
    """Find an active or blocked task by its *window* name.

    Skips completed and abandoned tasks since their windows are no longer
    relevant.  Returns the task dict or ``None`` if no match is found.
    """
    for task in state.get("tasks", []):
        if task.get("state") in ("completed", "abandoned"):
            continue
        if task.get("window") == window:
            return task
    return None


# ---------------------------------------------------------------------------
# Timestamp helper
# ---------------------------------------------------------------------------


def now_timestamp():
    """Return the current UTC time as ``"YYYY-MM-DD HH:MM"``."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M")


def parse_last_checked(ts_str):
    """Convert a ``"YYYY-MM-DD HH:MM"`` timestamp string to a Unix float.

    Returns ``0.0`` for ``None``, empty, or unparseable strings.
    """
    if not ts_str:
        return 0.0
    try:
        return datetime.strptime(ts_str, "%Y-%m-%d %H:%M").replace(tzinfo=timezone.utc).timestamp()
    except (ValueError, TypeError):
        return 0.0


# ---------------------------------------------------------------------------
# Standing orders helpers
# ---------------------------------------------------------------------------


def next_standing_order_id(state):
    """Return the next available standing order ID string (e.g. 'so-3')."""
    orders = state.get("standing_orders", [])
    if not orders:
        return "so-1"
    max_num = 0
    for o in orders:
        oid = o.get("id", "")
        if oid.startswith("so-"):
            try:
                max_num = max(max_num, int(oid[3:]))
            except ValueError:
                pass
    return f"so-{max_num + 1}"


def add_standing_order(state, title, description, priority="normal"):
    """Add a standing order to the state. Returns the new order dict."""
    order = {
        "id": next_standing_order_id(state),
        "priority": priority,
        "title": title,
        "description": description,
        "created": now_timestamp(),
    }
    state.setdefault("standing_orders", []).append(order)
    return order


def remove_standing_order(state, order_id):
    """Remove a standing order by ID. Returns True if found and removed."""
    orders = state.get("standing_orders", [])
    for i, o in enumerate(orders):
        if o.get("id") == order_id:
            orders.pop(i)
            return True
    return False


def find_standing_order(state, order_id):
    """Find a standing order by ID. Returns the order dict or None."""
    for o in state.get("standing_orders", []):
        if o.get("id") == order_id:
            return o
    return None


# ---------------------------------------------------------------------------
# Markdown rendering
# ---------------------------------------------------------------------------


def render_task(task, fields=None, show_state=False):
    """Return a list of markdown lines for a single task.

    *fields* defaults to the appropriate list for the task's state.
    If *show_state* is True, append ``[state]`` to the header line.
    """
    if fields is None:
        # Use REMOTE_WORKER_FIELDS for active/blocked remote-worker tasks
        if task.get("type") == "remote-worker" and task.get("state") in ("active", "blocked"):
            fields = REMOTE_WORKER_FIELDS
        else:
            fields = FIELDS_BY_STATE.get(task.get("state", "active"), ACTIVE_FIELDS)

    lines = []
    task_id = task.get("id", "?")
    alias = task.get("alias", "unknown")
    header = f"### task-{task_id}: {alias}"
    if show_state:
        header += f" [{task.get('state', 'unknown')}]"
    lines.append(header)

    for field in fields:
        value = task.get(field)
        if value is None:
            continue
        label = FIELD_LABELS.get(field, field)
        # Render remote block as sub-fields
        if field == "remote" and isinstance(value, dict):
            lines.append(f"- **Remote host**: {value.get('host', '?')}")
            if value.get("hostname") and value["hostname"] != value.get("host"):
                lines.append(f"- **Remote hostname**: {value['hostname']}")
            if value.get("subordinate_alias"):
                lines.append(f"- **Subordinate alias**: {value['subordinate_alias']}")
            if value.get("subordinate_task_id") is not None:
                lines.append(f"- **Subordinate task ID**: {value['subordinate_task_id']}")
            continue
        # Truncate long requirements in list view (~200 chars)
        if field == "requirements" and isinstance(value, str) and len(value) > 200:
            value = value[:200] + "..."
        lines.append(f"- **{label}**: {value}")

    # Show journal entry count if present
    journal = task.get("journal")
    if journal:
        count = len(journal)
        noun = "entry" if count == 1 else "entries"
        lines.append(f"- **Journal**: {count} {noun}")

    return lines


def render_markdown(state):
    """Render the full supervisor state as a markdown string.

    The output matches the canonical ``responsibilities.md`` format with
    sections for Blocked, Active, Completed, and Abandoned tasks.
    Blocked tasks are shown first to draw immediate attention.
    """
    tasks = state.get("tasks", [])

    blocked = sorted(
        [t for t in tasks if t.get("blocked")],
        key=lambda t: t.get("id", 0),
    )
    active = sorted(
        [t for t in tasks if t.get("state") == "active" and not t.get("blocked")],
        key=lambda t: t.get("id", 0),
    )
    completed = sorted(
        [t for t in tasks if t.get("state") == "completed"],
        key=lambda t: t.get("id", 0),
    )
    abandoned = sorted(
        [t for t in tasks if t.get("state") == "abandoned"],
        key=lambda t: t.get("id", 0),
    )

    lines = ["# Supervisor State", ""]

    # -- Standing Orders ------------------------------------------------------
    orders = state.get("standing_orders", [])
    if orders:
        lines.append("## Standing Orders")
        lines.append("")
        for o in orders:
            priority_tag = f" **[{o['priority'].upper()}]**" if o.get("priority", "normal") != "normal" else ""
            lines.append(f"- **{o.get('id', '?')}**: {o.get('title', 'untitled')}{priority_tag}")
            lines.append(f"  {o.get('description', '')}")
        lines.append("")

    # -- Blocked --------------------------------------------------------------
    if blocked:
        lines.append("## ⚠️  Blocked (Needs Attention)")
        lines.append("")
        for task in blocked:
            lines.extend(render_task(task, BLOCKED_FIELDS))
            lines.append("")

    # -- Active ---------------------------------------------------------------
    lines.append("## Active")
    lines.append("")
    if not active:
        lines.append("_No active tasks_")
        lines.append("")
    for task in active:
        lines.extend(render_task(task, ACTIVE_FIELDS))
        lines.append("")

    # -- Completed ------------------------------------------------------------
    lines.append("## Completed")
    lines.append("")
    if not completed:
        lines.append("_No completed tasks_")
        lines.append("")
    for task in completed:
        lines.extend(render_task(task, COMPLETED_FIELDS))
        lines.append("")

    # -- Abandoned ------------------------------------------------------------
    lines.append("## Abandoned")
    lines.append("")
    if not abandoned:
        lines.append("_No abandoned tasks_")
        lines.append("")
    for task in abandoned:
        lines.extend(render_task(task, ABANDONED_FIELDS))
        lines.append("")

    # Strip any trailing blank lines then add a single final newline.
    while lines and lines[-1] == "":
        lines.pop()
    lines.append("")

    return "\n".join(lines)
