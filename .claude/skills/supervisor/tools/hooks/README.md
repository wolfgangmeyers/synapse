# Hooks

Claude Code hook scripts that fire on agent lifecycle events.

## Hook files

| File | Event | Purpose |
|---|---|---|
| `mailbox-deliver.py` | PostToolUse | Check agent mailbox; inject unread messages into model context |

Hooks are symlinked to `~/.claude/hooks/` by the install skill and registered in
`~/.claude/settings.json`.

---

## Critical: PostToolUse hooks must use JSON additionalContext

**Plain `print()` output from PostToolUse hooks goes to verbose/debug mode only.
The model never sees it.**

To inject content into the model's context from a PostToolUse hook, output JSON:

```python
import json, sys

output = {
    "hookSpecificOutput": {
        "hookEventName": "PostToolUse",
        "additionalContext": "Text the model will see",
    }
}
print(json.dumps(output))
sys.exit(0)
```

This is not obvious — the Claude Code docs describe stdout injection for PreToolUse
hooks (which use plain stdout), but PostToolUse requires the JSON wrapper.

### PreToolUse hooks (different)

PreToolUse hooks *can* use plain stdout and the model sees it. They can also block
tool execution by exiting non-zero. PostToolUse hooks cannot block (tool already ran).

### Exit codes

Both hook types: exit 0 always unless you intentionally want to block (PreToolUse only).
A non-zero exit from a PostToolUse hook is an error, not a block.

## `claude-activity.sh`

Records one line per Claude Code event so the sidecar can drive the router UI's working dot and
subagent badge. Registered on all eleven activity events by the install skill.

Two things worth knowing before debugging it:

- **Hooks load at session start.** A session that was already running when the hook was installed
  shows nothing, forever, until it restarts. Check when the session started before concluding the
  hook is broken.
- **It writes nothing unless a live sidecar is watching that pane.** It looks for
  `$MECHA_ACTIVITY_DIR/pane$TMUX_PANE.watch` and checks the pid inside is alive. No marker, a dead
  pid, or no tmux, and it exits before it even reads stdin. An empty activity directory usually
  means no sidecar, not a broken hook.

Open problems with the activity path are listed in `../ACTIVITY_KNOWN_ISSUES.md`. Every one of
them presents as working, so read it before concluding the indicator is simply broken.
