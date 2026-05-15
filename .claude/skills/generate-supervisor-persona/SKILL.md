---
name: generate-supervisor-persona
description: "DEPRECATED — use /generate-persona instead. Kept as a redirect wrapper."
---

# Generate Supervisor Persona (Deprecated)

This skill has been replaced by the unified `/generate-persona` skill.

## Redirect

When this skill is invoked, read `.claude/skills/generate-persona/SKILL.md` and follow its instructions with these preset parameters:

- `--use supervision`
- `--source chat-logs`

Pass through any arguments (`--project`, `--all`) to the new skill.

### Example Migration

Old:
```
/generate-supervisor-persona --project ~/code/<your-project>
```

New:
```
/generate-persona --use supervision --source chat-logs
```
Then provide the project filter (`~/code/<your-project>`) when prompted.
