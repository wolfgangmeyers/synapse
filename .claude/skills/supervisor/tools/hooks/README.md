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
