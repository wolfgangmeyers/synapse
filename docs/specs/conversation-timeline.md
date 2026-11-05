# Spec: Conversation Timeline & Real-Time Event Streaming

- **ID**: `SPEC-CONV-001`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Existing related E2E suites**: `tests/e2e/conversation-feed.spec.ts`, `tests/e2e/conversation-architecture-simplification.spec.ts` (partial coverage; see matrix)

---

## 1. Intent & User Outcome

The conversation feed is the user's primary interface for observing and conversing with managed agents. It presents an unambiguous, visually differentiated timeline where direct conversations between the user and the agent appear as intuitive speech bubbles, inter-agent coordination messages appear as distinct expandable mailbox envelope cards, and agent execution events (tool invocations, thinking traces, subagent delegations, and context resets) appear as compact timeline activity notifications.

Crucially, **real-time updates must flow directly into the conversation view without requiring user action, window refocus, or page reload**. As an agent runs turns, executes tools, generates internal thinking, and outputs assistant text, the conversation UI must dynamically and immediately append and update these events in real-time over the active WebSocket transport.

---

## 2. Core Invariants

1. **Real-Time Streaming Over WebSocket**: Incoming agent turns, tools, mailbox messages, thinking processes, and status changes MUST append or update in the conversation view immediately without user action or page reload.
2. **Direct Communication Partitioning**: User-to-agent prompts MUST ALWAYS render as outgoing speech bubbles (`.bubble-wrap.outgoing`), and agent-to-user replies MUST ALWAYS render as incoming speech bubbles (`.bubble-wrap.incoming`). They MUST NEVER render as envelope cards or generic activity cards.
3. **Inter-Agent Mailbox Isolation**: Messages sent between agents (peer-to-peer mailbox messages where neither the sender nor recipient is the active user) MUST ALWAYS render as expandable envelope cards (`.timeline-card.mailbox-envelope`), NEVER as direct conversational speech bubbles.
4. **Internal Event Classification**: Tool executions, reasoning/thinking traces, subagent lifecycles, and context resets MUST ALWAYS render as timeline activity items (`.complete-entry` / `.timeline-card`), visually distinct from speech bubbles.
5. **No Flash-and-Shift on Finalization**: Optimistic messages, streaming tokens, or pending tool states MUST transition to their finalized stored state in-place without unmounting, layout shifting, or causing the feed to jitter.
6. **No Duplicate Renderings**: The deduplication layer MUST guarantee that a message existing in both the timeline events stream and the managed history store renders exactly once.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-FEED-001] Real-time event streaming and appending over WebSocket
- **User Goal**: Watch the conversation feed update live as the agent operates without touching the browser.
- **Given**:
  - The conversation view is open and connected via WebSocket to the router for agent `worker-agent`.
  - The user has sent a prompt or the agent has initiated background work.
- **When**:
  - The agent begins thinking, executes a tool (e.g. `bash` or `read`), or produces an assistant reply.
  - The backend emits live WebSocket frames (`history_entry` via UI-live and/or `conversation_message` via router transport).
- **Then**:
  - The tool execution or thinking trace immediately appears in the conversation feed in real-time.
  - When the final response is generated, the incoming speech bubble immediately appends to the bottom of the feed.
  - No page reload, manual refresh, or navigation click is needed for the new items to be visible in the DOM.

#### Scenario: [SCN-FEED-002] User sends a prompt and receives an assistant response
- **User Goal**: Converse naturally with an agent and see speech bubbles on appropriate sides of the feed.
- **Given**:
  - The conversation view is open for agent `researcher`.
  - The agent is idle and listening.
- **When**:
  - User submits "Synthesize the latest test results".
  - The agent processes the prompt and sends back "Here is the summary of the test run...".
- **Then**:
  - The user's prompt renders as an outgoing speech bubble aligned to the right with blue accenting and user avatar.
  - The agent's response renders as an incoming speech bubble aligned to the left with purple accenting, the agent's robot avatar, and timestamp.
  - Neither entry renders as an envelope card or tool activity box.

#### Scenario: [SCN-FEED-003] Agent coordinates with peer worker via mailbox
- **User Goal**: Clearly distinguish agent background coordination from agent responses directed to the user.
- **Given**:
  - The conversation view is open for supervisor agent `lead-agent`.
- **When**:
  - `lead-agent` sends a task assignment mailbox message to `worker-coder` with subject "Implement auth schema".
  - `worker-coder` sends a mailbox reply to `lead-agent` with subject "Auth schema complete".
- **Then**:
  - Both messages render in the timeline as mailbox envelope cards (`.mailbox-envelope`).
  - The envelope card header displays a compact summary: sender avatar/name, arrow indicator, recipient avatar/name, subject line, and timestamp.
  - The envelope card body is collapsed by default and does not clutter the conversational feed until expanded by clicking the card.

#### Scenario: [SCN-FEED-004] Agent performs tool calls and thinking during a turn
- **User Goal**: Observe internal agent work as lightweight compact notifications while awaiting the final answer.
- **Given**:
  - The conversation view is open for agent `analyzer`.
- **When**:
  - Agent executes a bash command `pytest` followed by a file read.
- **Then**:
  - The tool invocations appear as compact timeline activity entries styled with cyan/purple indicator orbs.
  - Thinking blocks are collapsible and styled with subtle muted styling.
  - The final synthesis reply appears below the activities as an incoming speech bubble.

#### Scenario: [SCN-FEED-005] Seamless optimistic-to-confirmed message transition
- **User Goal**: Send a message with instant feedback and no visual jump when the router assigns a permanent ID.
- **Given**:
  - Conversation view is open.
- **When**:
  - User hits Enter to send "Run migration 112".
- **Then**:
  - An outgoing bubble appears immediately in the feed with a subtle "Sending..." indicator.
  - When the router acknowledges receipt via WebSocket, the "Sending..." label smoothly transitions to a timestamp.
  - The bubble remains at the exact same vertical offset without unmounting, redrawing markdown, or flickering.

---

### Non-Happy / Error / Edge Path Scenarios

#### Scenario: [SCN-FEED-006] Failed message delivery indication
- **User Goal**: Clearly understand when a message failed to reach the router or agent.
- **Given**:
  - The router becomes temporarily unreachable right as a message is submitted.
- **When**:
  - User sends "Deploy build".
  - The transport request times out or receives an HTTP 500 error.
- **Then**:
  - The message bubble remains visible in the feed.
  - A red danger status tag "Failed" appears next to the bubble timestamp.
  - The message body does not disappear, allowing the user to copy or retry.

#### Scenario: [SCN-FEED-008] Empty conversation with selected agent welcome card
- **User Goal**: See a clean, inviting agent welcome state when selecting an agent that has no prior messages or activity, rather than a hung loading badge.
- **Given**:
  - The user selects an agent from the sidebar that has no prior conversation history or activity entries.
- **When**:
  - The initial conversation fetch completes with 0 events.
- **Then**:
  - The conversation view renders an Agent Welcome Card featuring the agent's avatar, formatted name, and readiness status.
  - The view indicates the agent is ready for prompts and invites the user to start a conversation.
  - The view does not remain stuck on "Loading conversation…".

#### Scenario: [SCN-FEED-009] Transient conversation timeline loading state
- **User Goal**: Receive subtle, non-disruptive feedback while conversation history is being fetched from the router.
- **Given**:
  - An agent is selected and history is actively being fetched over the network.
- **When**:
  - The timeline query is in flight (`loading === true`).
- **Then**:
  - The timeline renders a sleek timeline loading shimmer/indicator instead of a static modal-like empty state box.
  - Once the fetch completes, the loading indicator cleanly resolves to either the conversation timeline (if items exist) or the agent welcome state (if empty).



#### Scenario: [SCN-FEED-010] Agent encounters tool execution failure
- **User Goal**: Inspect error details when an internal tool execution fails.
- **Given**:
  - Conversation view is open for `test-worker`.
- **When**:
  - A tool execution fails with a non-zero exit code or error output.
- **Then**:
  - The activity card renders with a red danger orb and border styling.
  - The card summary indicates the failed action.
  - Clicking the card opens the Activity Detail dialog displaying the full stderr/error log without breaking the feed layout.

#### Scenario: [SCN-FEED-011] Context reset boundary event
- **User Goal**: Clearly identify when an agent's memory window was compacted or reset.
- **Given**:
  - An agent triggers a context reset or reaches context limit threshold.
- **When**:
  - A `context_reset` event is recorded in the timeline.
- **Then**:
  - A distinct horizontal partition card renders across the feed indicating "Context Reset" with the epoch transition and reason.
  - Prior conversation history remains scrollable above the reset partition.
  - Subsequent conversation begins cleanly below the partition without wiping prior bubbles.


---

## 4. Automated E2E Verification Matrix

These are existing test assertions, not a claim that the full scenario passes. The named tests have not been run for this docs-only consolidation. The former target `tests/e2e/conversation-feed-rendering.spec.ts` does not exist.

Verification requires headless Playwright E2E tests exercising the real browser, router HTTP, proxy, WebSocket and SQLite boundaries for every scenario. Tests must simulate live events streamed over WebSocket, such as an incoming message or tool event while the conversation view is mounted, and verify that the corresponding DOM nodes appear dynamically without a page reload. No external LLM APIs may be called; drive agent turns and completions with deterministic local fixtures.

| Scenario | Existing related test and asserted subset | Unverified gaps |
| :--- | :--- | :--- |
| `SCN-FEED-001` | `tests/e2e/conversation-feed.spec.ts`: `App loads cleanly, connects to isolated router proxy, and renders conversation feed` posts an agent message after the view is mounted and checks its incoming bubble without reload. | Live tool, thinking and status updates; transport attribution and timing beyond eventual visibility. |
| `SCN-FEED-002` | Same test checks incoming and outgoing bubble selectors; `tests/e2e/conversation-architecture-simplification.spec.ts`: `renders user and assistant speech bubbles with avatars, compact envelope cards for peer messages, attachments without leaked paths, and maintains scroll stability` also checks both avatars and the user bubble label. | Assistant bubble label, full visual styling, timestamps and all exclusion requirements. |
| `SCN-FEED-003` | The two tests above check an outgoing peer activity card and subject/header. | Both directions, complete header, collapsed default and expansion; exact envelope classification. |
| `SCN-FEED-004` | No mapped complete E2E test. | Tool styling, collapsible thinking and final ordering. |
| `SCN-FEED-005` | No mapped complete E2E test. | Pending-to-confirmed transition, node identity, markdown and layout stability. |
| `SCN-FEED-006` | No mapped complete E2E test. | Failure status, retained body and copy/retry access. |
| `SCN-FEED-008` | No mapped complete E2E test. | Empty-history welcome content and loading resolution. |
| `SCN-FEED-009` | No mapped complete E2E test. | In-flight indicator and both completion outcomes. |
| `SCN-FEED-010` | No mapped complete E2E test. | Failed tool styling, summary and full error detail dialog. |
| `SCN-FEED-011` | No mapped complete E2E test. | Reset divider, epoch/reason and preserved history on both sides. |

Deduplication and in-place finalization of streaming tokens and pending tool states are also invariants above; their full E2E verification remains unverified.
