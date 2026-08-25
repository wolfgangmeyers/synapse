# Cursor tool surfaces for mecha-developer

mecha-developer native Pi tools are exposed to Cursor models through [pi-cursor-sdk](https://www.npmjs.com/package/pi-cursor-sdk) when a session runs a `cursor/*` model with the pi bridge enabled (default).

Reference: [pi-cursor-sdk cursor-tool-surfaces.md](https://cdn.jsdelivr.net/npm/pi-cursor-sdk@0.2.0/docs/cursor-tool-surfaces.md)

## Callable surfaces

| Surface | Examples | Notes |
| --- | --- | --- |
| Cursor SDK host tools | `read`, `bash`, `grep`, `edit` | Cursor-native; not prefixed |
| Configured Cursor MCP | project/user MCP servers | Separate from the pi bridge |
| Pi bridge (`pi__*`) | `pi__mecha_mailbox_send`, `pi__mecha_state`, `pi__goal` | Active mecha/goal tools from this repo |

Call exposed `pi__*` MCP names from Cursor. Do not treat pi history names, replay IDs (`cursor-replay-*`), or transcript labels as callable tools.

## Mecha native tools on the bridge

Operational tools (`pi-extensions/operational-tools.js`):

- `pi__mecha_state`
- `pi__mecha_priority`
- `pi__mecha_verification_handoff`
- `pi__mecha_reminder`
- `pi__mecha_worker`
- `pi__mecha_pr`
- `pi__mecha_git`
- `pi__mecha_router`

Messaging/control tools (`pi-extensions/mecha-messaging.js`):

- `pi__mecha_mailbox_send`
- `pi__mecha_parent_message`
- `pi__mecha_request_inline_tool_allowance`
- `pi__mecha_recall`
- `pi__mecha_settled_notifications`
- `pi__mecha_context`

Goal tool (`pi-extensions/goal.js`):

- `pi__goal`

Each tool carries `promptSnippet` and `promptGuidelines` so pi-cursor-sdk can include them in the bootstrap manifest and MCP descriptions.

Operational tools use a flat `action` object schema (not top-level JSON Schema `oneOf`) so the bridge forwards useful MCP input schemas to Cursor.

## Local test recipe

Prerequisites:

- `pi install npm:pi-cursor-sdk` (already present in Wolfgang's global pi install)
- mecha extensions installed in `~/.pi/agent/extensions/`, or registered in `~/.pi/agent/settings.json` under `extensions`: `mecha-messaging.js` and `goal.js`
- router running on loopback (`MECHA_ROUTER_URL=http://127.0.0.1:8765`)

Pi discovers installed extensions. Run a Cursor-model session from this repo:

```bash
cd /Users/wolfgang/code/mecha-developer
MECHA_ROUTER_URL=http://127.0.0.1:8765 \
pi --model cursor/composer-2-5
```

`mecha-messaging.js` imports and registers the operational tools. Pi uses its registered default extensions; `MECHA_PI_MAILBOX_EXTENSION` is not used by the worker launcher and does not load an extension into Pi.

Inside pi:

1. `/cursor-tools` — confirm bridge enabled and `pi__mecha_*` / `pi__goal` listed
2. Ask the model to call `pi__mecha_router` with `{ "action": "health" }`
3. Ask the model to call `pi__mecha_mailbox_send` with a loopback test recipient if appropriate

Disable bridge for comparison:

```bash
PI_CURSOR_PI_TOOL_BRIDGE=0 pi --model cursor/composer-2-5
```

## Environment toggles

| Variable | Default | Effect |
| --- | --- | --- |
| `PI_CURSOR_PI_TOOL_BRIDGE` | on | Exposes active pi tools as `pi__*` |
| `PI_CURSOR_TOOL_MANIFEST` | on | Bootstrap callable-surface manifest |
| `PI_CURSOR_EXPOSE_BUILTIN_TOOLS` | off | Hide overlapping pi builtins (`read`, `bash`, …) |

Cloud Cursor runtime does not receive the local pi bridge; use local runtime for mecha native tool testing.
