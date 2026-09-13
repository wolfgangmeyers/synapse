# C4 builtin lifecycle UI follow-up

The backend now emits one Complete history entry for a managed Pi builtin. It sends an active entry first and later reuses the same `id` for the terminal entry.

The UI owner needs to make these protocol changes:

- Accept `presentation.outcome` values `active` and `interrupted` for `kind: "tool"`.
- Treat a Complete `history_entry` with an existing `entry.id` as an in-place replacement. Keep the original timeline position. The current reducer ignores equal IDs, so a live terminal update is lost until reload.
- Render `active` as `Running <tool>` with the existing rotating border. Stop the animation for `success`, `failed`, and `interrupted`.
- Keep using only the existing safe fields: `id`, `tool_name`, `outcome`, `summary`, and static error code. Do not display tool call IDs, arguments, paths, output, results, or diagnostics.

Until that lands, history reloads show the terminal state correctly. A connected current UI can keep the active card after terminal completion because it discards same-ID frames.
