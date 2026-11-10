# Tool permissions popups

Status: Active

## User scenarios

- Global defaults and an agent's saved permissions show grouped tools and allow individual and category changes. Global defaults apply to any agent without a per-tool override, including existing agents; an agent-specific override takes precedence.
- A workflow override displays both saved and effective agent permissions; editing saved permissions does not imply changing the workflow override.
- Claude's unrestricted native tools are hidden from agent controls. Category actions affect only the visible tools, never hidden native tools.
- Tools without a category appear in General and its bulk controls affect those tools.
- After a failed or partially successful update the popup reports the error and reloads the authoritative state. An old response must not replace data for a different agent or a subsequently reopened popup.
- Closing and reopening shows persisted values from the router's SQLite store.
- Closing or switching agents does not cancel a bulk operation already started. A later view of the same permissions waits for pending writes before reading; partial failures remain visible alongside the reloaded saved state.
- A failed update remains visible after reopening until the user dismisses it after a successful authoritative reload; if reload fails, the popup does not imply the optimistic values were saved.
- Pending permission writes remain until they settle. Settled successful queue entries are removed; up to 100 settled failures per client are retained until dismissed, with older failures evicted first. Failure messages are limited to 512 characters.
