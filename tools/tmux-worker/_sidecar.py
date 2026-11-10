"""Locate and terminate the Mailbox sidecar that serves a Claude worker window.

`worker_launch.py` starts `claude_sidecar.mjs` as a detached background job inside
the worker's shell. A sidecar that outlives its window stays registered with the
router as a connected agent, so mail addressed to the dead worker is accepted and
pasted into a window that no longer exists — lost with no error to the sender.

Two indirect backstops already exist: the launch shell traps EXIT/INT/TERM and kills
the sidecar, and the sidecar itself polls for its tmux window every 5 seconds and
exits when the window is gone (`createTmuxWindowMonitor`). Both depend on something
else working — the shell reaching its trap, or the sidecar's own event loop still
running. This module is the direct path: shutdown kills the sidecar itself and
confirms it, which closes the seconds-long window the poller leaves and covers the
case where neither backstop fires. Sidecars launched before either backstop existed
survived indefinitely; that is the failure this exists to make impossible.

Shutdown paths call `cleanup_sidecar()` — it kills the sidecar alongside the window,
verifies it is gone, reports the outcome to the operator, and returns False when a
sidecar may still be running so the caller can exit non-zero. `terminate_sidecar()`
is the mechanism underneath it and reports nothing; new shutdown paths want
`cleanup_sidecar()`.

Killing the sidecar is enough to clear the router's view of the agent: the process
death closes its WebSocket, and the router releases the lease and emits
`agent_disconnected` from its socket-close handler (`router-proxy Go router`). No
router-side call is needed here.
"""

import os
import signal
import subprocess
import sys
import time

SIDECAR_SCRIPT = "claude_sidecar.mjs"
SIDECAR_INTERPRETER = "node"
AGENT_ID_FLAG = "--agent-id"
# realpath, not abspath. This module is reached through `~/.local/bin` symlinks, and a
# path built from the as-invoked location hands `node` a script that is not there.
# worker_launch.py hit exactly that and carries a guard test for it.
SIDECAR_SCRIPT_PATH = os.path.join(os.path.dirname(os.path.realpath(__file__)), SIDECAR_SCRIPT)


def sidecar_argv(window, router_url):
    """The argv that starts the mailbox sidecar for *window*.

    This lives beside `_is_sidecar_argv`, which has to recognise what this produces. They
    are two halves of one fact: how a sidecar is started and how a running one is found.
    Held apart, a flag added to one launcher still matches the finder while meaning
    something different, and a sidecar can be started that nothing can later locate or
    clean up. Every launcher calls this rather than spelling the tokens out.

    `--agent-id` and `--window` are deliberately the same value. The agent id is the
    mailbox address and the window is where mail gets pasted; splitting them delivers one
    agent's mail into another agent's pane.
    """
    return [
        SIDECAR_INTERPRETER, SIDECAR_SCRIPT_PATH,
        AGENT_ID_FLAG, window,
        "--window", window,
        "--router-url", router_url,
    ]

# node options that consume the following argv token as their value, so that token is
# not the entrypoint. Options written as `--opt=value` need no entry here.
NODE_VALUE_OPTIONS = frozenset({
    "-r", "--require", "--import", "--loader", "--experimental-loader",
    "--conditions", "-C", "--env-file", "--max-old-space-size", "--title",
})
# node options that mean there is no entrypoint script at all.
NODE_EVAL_OPTIONS = frozenset({"-e", "--eval", "-p", "--print", "--check"})

# Seconds to wait for the sidecar to exit after each signal before escalating.
TERMINATE_TIMEOUT = 3.0
POLL_INTERVAL = 0.1


class ProcessTableUnavailable(RuntimeError):
    """`ps` could not be read, so no claim can be made about running sidecars."""


def _process_table():
    """Return `(pid, args)` pairs for every process visible to this user.

    Raises ProcessTableUnavailable rather than returning an empty table: "there is
    no sidecar" and "I could not look" must not resolve to the same answer, because
    the first is a clean shutdown and the second is a possible silent mail leak.
    """
    try:
        result = subprocess.run(
            ["ps", "-Ao", "pid=,args="],
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as error:
        raise ProcessTableUnavailable(f"could not run ps: {error}") from error
    if result.returncode != 0:
        raise ProcessTableUnavailable(f"ps exited with status {result.returncode}")
    entries = []
    for line in result.stdout.splitlines():
        pid_text, _, args = line.strip().partition(" ")
        if not pid_text.isdigit() or not args:
            continue
        entries.append((int(pid_text), args))
    return entries


def _node_entrypoint_index(tokens):
    """Index of the script node would execute, or None if there is no script.

    Walks node's own options the way node does: `--opt=value` is self-contained, a few
    options take the next token as their value, and `-e`/`-p`/`--check` mean there is no
    entrypoint. The first token that is left is the script.
    """
    index = 1
    while index < len(tokens):
        token = tokens[index]
        if not token.startswith("-"):
            return index
        if token in NODE_EVAL_OPTIONS:
            return None
        if token in NODE_VALUE_OPTIONS:
            index += 2
            continue
        index += 1
    return None


def _is_sidecar_argv(args, window):
    """Is this argv line a running sidecar for `window`?

    The launcher starts the sidecar as `node <path>/claude_sidecar.mjs --agent-id
    <window> ...` (see worker_launch.py). The match requires that exact shape: node as
    the executable, the sidecar as the script node actually runs, and the flag pair in
    the script's own arguments. Anything looser matches processes that merely mention
    the sidecar — the tmux `send-keys` that pastes the launch command carries the whole
    string in its argv, and `node watcher.mjs --watch <path>/claude_sidecar.mjs
    --agent-id w` would pass a check that only asked for the script somewhere in argv.

    What keeps this from killing the wrong process is exact token equality, on both the
    script's basename and the window value — not validation of `window`, which neither
    this module nor its callers perform. A `window` that is malformed, whitespace-laden,
    or full of shell metacharacters can therefore only fail to match; it cannot select
    something else. Failing to match is the safe direction, and the one case where it
    would mean a missed sidecar is unreachable: `claude_sidecar.mjs` calls
    `assertAgentId` at startup (`^[A-Za-z0-9][A-Za-z0-9_-]*$`), so a sidecar whose agent
    ID contains whitespace exits instead of registering, and there is nothing to find.
    """
    tokens = args.split()
    if len(tokens) < 4 or os.path.basename(tokens[0]) != SIDECAR_INTERPRETER:
        return False
    entrypoint = _node_entrypoint_index(tokens)
    if entrypoint is None or os.path.basename(tokens[entrypoint]) != SIDECAR_SCRIPT:
        return False
    arguments = tokens[entrypoint + 1:]
    return any(
        token == AGENT_ID_FLAG and arguments[index + 1] == window
        for index, token in enumerate(arguments[:-1])
    )


def find_sidecar_pids(window):
    """Return PIDs of sidecars launched with `--agent-id <window>`.

    Matching is on whole argv tokens so `worker-1` never matches `worker-10`.
    """
    return [pid for pid, args in _process_table() if _is_sidecar_argv(args, window)]


def terminate_sidecar(window):
    """Terminate the window's sidecar(s) and verify they are gone.

    Sends SIGTERM, waits up to TERMINATE_TIMEOUT seconds, then escalates to SIGKILL
    and waits again. Returns `{"found": [...], "terminated": [...], "survived": [...]}`
    with PIDs; `found` is empty when there was no sidecar to clean up. Raises
    ProcessTableUnavailable if the process table cannot be read at any point.
    """
    found = find_sidecar_pids(window)
    if not found:
        return {"found": [], "terminated": [], "survived": []}

    remaining = list(found)
    for sig in (signal.SIGTERM, signal.SIGKILL):
        for pid in remaining:
            try:
                os.kill(pid, sig)
            except OSError:
                # Already exited, or not ours to signal. The verify pass below decides.
                pass
        remaining = _wait_for_exit(window, remaining, TERMINATE_TIMEOUT)
        if not remaining:
            break

    return {
        "found": found,
        "terminated": [pid for pid in found if pid not in remaining],
        "survived": remaining,
    }


def _wait_for_exit(window, pids, timeout):
    """Poll until `pids` are gone; return whichever are still alive.

    The budget is `timeout` seconds of sleeping, so real elapsed time also includes one
    `ps` per poll — a bounded overshoot, not an unbounded wait.
    """
    remaining_seconds = timeout
    while True:
        alive = [pid for pid in find_sidecar_pids(window) if pid in pids]
        if not alive or remaining_seconds <= 0:
            return alive
        time.sleep(POLL_INTERVAL)
        remaining_seconds -= POLL_INTERVAL


def _pgrep_hint(window):
    """A diagnostic that lists only this window's sidecar.

    Deliberately not a bare `pgrep -fl claude_sidecar.mjs`: that lists every agent's
    sidecar, and anyone who kills what it prints cuts mail delivery for every live
    Claude worker. `-x` style exact matching is unavailable for argv patterns, so the
    window name is anchored by the following flag to keep `worker-1` off `worker-10`.
    """
    return f"pgrep -fl 'claude_sidecar.mjs {AGENT_ID_FLAG} {window} '"


def cleanup_sidecar(window):
    """Terminate the sidecar for `window` and report the outcome to the operator.

    Returns True when no sidecar remains. Returns False when one survived SIGKILL or
    when the process table could not be read — in both cases a sidecar may still be
    registered with the router, accepting mail for a window that is gone.
    """
    try:
        result = terminate_sidecar(window)
    except ProcessTableUnavailable as error:
        print(
            f"WARNING: could not confirm whether a mailbox sidecar for '{window}' is "
            f"running or was killed ({error}). If one is still up it stays registered "
            "with the router and mail sent to this agent is accepted and lost. This is "
            f"retryable — re-run this tool. To check by hand: {_pgrep_hint(window)}",
            file=sys.stderr,
        )
        return False
    if not result["found"]:
        print(f"No mailbox sidecar process found for '{window}'.")
        return True
    if result["survived"]:
        print(
            f"WARNING: mailbox sidecar for '{window}' survived SIGKILL "
            f"(pid {', '.join(str(pid) for pid in result['survived'])}). "
            "It is still registered with the router, so mail sent to this agent is "
            "accepted and lost. Re-running this tool will not help — kill these PIDs "
            f"by hand. To confirm which they are: {_pgrep_hint(window)}",
            file=sys.stderr,
        )
        return False
    print(
        f"Terminated mailbox sidecar for '{window}' "
        f"(pid {', '.join(str(pid) for pid in result['terminated'])})."
    )
    return True
