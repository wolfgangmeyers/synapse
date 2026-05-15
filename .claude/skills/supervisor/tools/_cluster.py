"""Shared utility module for cluster configuration.

Loads and validates cluster topology from ~/.mecha-wolfgang/cluster.json.
If the file does not exist, the system operates in single-machine mode.

Cluster config schema:

Head machine:
  {
    "role": "head",
    "hostname": "wolfgangs-laptop",
    "subordinates": [
      {
        "id": "devbox-2",
        "hostname": "devbox-2",
        "capabilities": ["go", "node", "python"],
        "max_workers": 4
      }
    ]
  }

Subordinate machine:
  {
    "role": "subordinate",
    "hostname": "devbox-2",
    "head": "wolfgangs-laptop",
    "capabilities": ["go", "node", "python"],
    "max_workers": 4
  }
"""

import json
import os
import re
import subprocess
import sys

CLUSTER_FILE = os.environ.get(
    "MECHA_CLUSTER_FILE",
    os.path.expanduser("~/.mecha-wolfgang/cluster.json"),
)

# Only these commands may be executed on remote machines via SSH.
# Each entry is matched exactly as a command prefix — a space or end-of-string
# must follow the prefix to prevent bypass via appended shell metacharacters.
# NOTE: These paths are tightly coupled to the tools directory layout.
ALLOWED_REMOTE_COMMANDS = [
    "python3 ~/.claude/skills/supervisor/tools/mailbox_send.py",
    "python3 ~/.claude/skills/supervisor/tools/state_read.py",
    "tmux has-session -t mecha",
]

# Valid hostname pattern: alphanumeric, hyphens, dots. No leading hyphen.
_HOSTNAME_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]*$")

SSH_TIMEOUT = 30  # seconds

# Cache for cluster config — the file doesn't change during a single process.
_cluster_cache = {"loaded": False, "data": None}


def load_cluster():
    """Load cluster config. Returns dict or None if not configured.

    Caches the result for the lifetime of the process — the config file
    does not change during a single tool invocation.
    """
    if _cluster_cache["loaded"]:
        return _cluster_cache["data"]
    _cluster_cache["loaded"] = True
    if not os.path.isfile(CLUSTER_FILE):
        _cluster_cache["data"] = None
        return None
    try:
        with open(CLUSTER_FILE, "r", encoding="utf-8") as f:
            _cluster_cache["data"] = json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        print(f"Warning: could not load cluster config: {e}", file=sys.stderr)
        _cluster_cache["data"] = None
    return _cluster_cache["data"]


def get_role():
    """Return this machine's cluster role: 'head', 'subordinate', or None (single-machine)."""
    cluster = load_cluster()
    if cluster is None:
        return None
    return cluster.get("role")


def get_hostname():
    """Return this machine's hostname from cluster config, or None."""
    cluster = load_cluster()
    if cluster is None:
        return None
    return cluster.get("hostname")


def get_head_hostname():
    """Return the head supervisor's hostname. Works from both head and subordinate."""
    cluster = load_cluster()
    if cluster is None:
        return None
    role = cluster.get("role")
    if role == "head":
        return cluster.get("hostname")
    elif role == "subordinate":
        return cluster.get("head")
    return None


def get_subordinates():
    """Return list of subordinate dicts from a head's config. Empty list if not head."""
    cluster = load_cluster()
    if cluster is None or cluster.get("role") != "head":
        return []
    return cluster.get("subordinates", [])


def find_subordinate(host_id):
    """Find a subordinate by its id. Returns the subordinate dict or None."""
    for sub in get_subordinates():
        if sub.get("id") == host_id:
            return sub
    return None


def resolve_hostname(host_id):
    """Resolve a host ID to an SSH hostname using cluster.json.

    Resolution order:
    1. "head" alias → head's hostname (from subordinate config)
    2. Subordinate ID → subordinate's hostname
    3. This machine's hostname → pass through
    4. Fall back to using host_id as-is
    """
    cluster = load_cluster()
    if cluster is None:
        return host_id

    # "head" is a special alias for the head supervisor
    if host_id == "head" and cluster.get("role") == "subordinate":
        return cluster.get("head", host_id)

    # Check subordinates
    sub = find_subordinate(host_id)
    if sub is not None:
        return sub.get("hostname", host_id)

    # Pass through if it matches this machine
    if host_id == cluster.get("hostname"):
        return host_id

    return host_id


def validate_hostname(hostname):
    """Validate that hostname looks like a real hostname. Raises ValueError if not.

    Prevents SSH flag injection via hostnames starting with '-'.
    """
    if not _HOSTNAME_RE.match(hostname):
        raise ValueError(
            f"Invalid hostname: {hostname!r}. "
            "Must be alphanumeric with hyphens, dots, or underscores, "
            "and must not start with a hyphen."
        )


def make_remote_block(host_id, hostname, subordinate_alias=None, subordinate_task_id=None):
    """Create a remote block dict for remote-worker tasks.

    Shared schema used by remote_delegate.py and state_add.py.
    """
    return {
        "host": host_id,
        "hostname": hostname,
        "subordinate_alias": subordinate_alias,
        "subordinate_task_id": subordinate_task_id,
    }


def _validate_remote_command(cmd):
    """Check that cmd matches an allowed prefix followed by a space or end-of-string.

    Raises ValueError if the command is not whitelisted.
    """
    for allowed in ALLOWED_REMOTE_COMMANDS:
        if cmd == allowed or cmd.startswith(allowed + " "):
            return
    raise ValueError(
        f"Remote command not in whitelist: {cmd!r}\n"
        f"Allowed prefixes: {ALLOWED_REMOTE_COMMANDS}"
    )


def ssh_run(hostname, remote_cmd, timeout=SSH_TIMEOUT):
    """Run a whitelisted command on a remote machine via SSH.

    Returns a subprocess.CompletedProcess. Raises ValueError for
    non-whitelisted commands or invalid hostnames,
    subprocess.TimeoutExpired on timeout.
    """
    validate_hostname(hostname)
    _validate_remote_command(remote_cmd)

    # StrictHostKeyChecking=accept-new uses TOFU (trust on first use).
    # This is acceptable because Tailscale provides cryptographic node
    # identity and WireGuard transport encryption — SSH host key verification
    # is a secondary check in this environment.
    result = subprocess.run(
        [
            "ssh",
            "-o", "ConnectTimeout=10",
            "-o", "StrictHostKeyChecking=accept-new",
            "-o", "BatchMode=yes",
            hostname,
            remote_cmd,
        ],
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    return result
