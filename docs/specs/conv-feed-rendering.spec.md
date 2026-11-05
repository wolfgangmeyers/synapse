# Feed Rendering & Entry Partitioning: Consolidated

The canonical active contract is [Conversation Timeline & Real-Time Event Streaming](./conversation-timeline.md) (`SPEC-CONV-001`). This file is a pointer, not a second specification. The canonical verification matrix records existing partial tests and unverified gaps.

## Previous scenario ID crosswalk

IDs below are qualified by their former file because the two documents reused IDs for different behaviors. Canonical IDs preserve the former timeline IDs; the two feed-only scenarios receive new IDs.

| Former feed-rendering ID | Canonical timeline ID | Behavior |
| :--- | :--- | :--- |
| `SCN-FEED-001` | `SCN-FEED-002` | Direct dialogue |
| `SCN-FEED-002` | `SCN-FEED-003` | Peer mailbox |
| `SCN-FEED-003` | `SCN-FEED-004` | Tools and thinking |
| `SCN-FEED-004` | `SCN-FEED-005` | Optimistic finalization |
| `SCN-FEED-005` | `SCN-FEED-006` | Failed delivery |
| `SCN-FEED-006` | `SCN-FEED-010` | Failed tool detail |
| `SCN-FEED-007` | `SCN-FEED-011` | Context reset divider |

Former timeline IDs `SCN-FEED-001`–`006`, `008`, and `009` remain unchanged in the canonical file, including live WebSocket updates, welcome and loading states.
