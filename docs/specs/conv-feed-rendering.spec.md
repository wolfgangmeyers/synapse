# Spec: Feed Rendering & Entry Partitioning

- **ID**: `SPEC-CONV-001`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/conversation-feed-rendering.spec.ts`

---

## 1. Intent & User Outcome

The conversation feed is the user's primary interface for observing and conversing with managed agents. It presents an unambiguous, visually differentiated timeline where direct conversations between the user and the agent appear as intuitive speech bubbles, inter-agent coordination messages appear as distinct expandable mailbox envelope cards, and agent execution events (tool invocations, thinking phases, subagent delegations, and context resets) appear as compact timeline activity notifications.

Users experience a smooth, flicker-free stream where new events append cleanly without jumping, collapsing, or flashing between temporary and finalized states.

---

## 2. Core Invariants

1. **Direct Communication Partitioning**: User-to-agent prompts MUST ALWAYS render as outgoing speech bubbles (`.bubble-wrap.outgoing`), and agent-to-user replies MUST ALWAYS render as incoming speech bubbles (`.bubble-wrap.incoming`). They MUST NEVER render as envelope cards or generic activity cards.
2. **Inter-Agent Mailbox Isolation**: Messages sent between agents (peer-to-peer mailbox messages where neither the sender nor recipient is the active user) MUST ALWAYS render as expandable envelope cards (`.timeline-card.mailbox-envelope`), NEVER as direct conversational speech bubbles.
3. **Internal Event Classification**: Tool executions, reasoning/thinking traces, subagent lifecycles, and context resets MUST ALWAYS render as timeline activity items (`.complete-entry` / `.timeline-card`), visually distinct from speech bubbles.
4. **No Flash-and-Shift on Finalization**: Optimistic messages, streaming tokens, or pending tool states MUST transition to their finalized stored state in-place without unmounting, layout shifting, or causing the feed to jitter.
5. **No Duplicate Renderings**: The deduplication layer MUST guarantee that a message existing in both the timeline events stream and the managed history store renders exactly once.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-FEED-001] User sends a prompt and receives an assistant response
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

#### Scenario: [SCN-FEED-002] Agent coordinates with peer worker via mailbox
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

#### Scenario: [SCN-FEED-003] Agent performs tool calls and thinking during a turn
- **User Goal**: Observe internal agent work as lightweight compact notifications while awaiting the final answer.
- **Given**:
  - The conversation view is open for agent `analyzer`.
- **When**:
  - Agent executes a bash command `pytest` followed by a file read.
- **Then**:
  - The tool invocations appear as compact timeline activity entries styled with cyan/purple indicator orbs.
  - Thinking blocks are collapsible and styled with subtle muted styling.
  - The final synthesis reply appears below the activities as an incoming speech bubble.

#### Scenario: [SCN-FEED-004] Seamless optimistic-to-confirmed message transition
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

#### Scenario: [SCN-FEED-005] Failed message delivery indication
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

#### Scenario: [SCN-FEED-006] Agent encounters tool execution failure
- **User Goal**: Inspect error details when an internal tool execution fails.
- **Given**:
  - Conversation view is open for `test-worker`.
- **When**:
  - A tool execution fails with a non-zero exit code or error output.
- **Then**:
  - The activity card renders with a red danger orb and border styling.
  - The card summary indicates the failed action.
  - Clicking the card opens the Activity Detail dialog displaying the full stderr/error log without breaking the feed layout.

#### Scenario: [SCN-FEED-007] Context reset boundary event
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

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-FEED-001` | User prompt & assistant reply rendering | `tests/e2e/conversation-feed-rendering.spec.ts` | `renders direct dialogue as incoming and outgoing speech bubbles` |
| `SCN-FEED-002` | Inter-agent mailbox envelope isolation | `tests/e2e/conversation-feed-rendering.spec.ts` | `renders peer agent messages as envelope cards, not speech bubbles` |
| `SCN-FEED-003` | Tool execution & thinking timeline items | `tests/e2e/conversation-feed-rendering.spec.ts` | `renders tool invocations as compact activity notifications` |
| `SCN-FEED-004` | Optimistic send smooth finalization | `tests/e2e/conversation-feed-rendering.spec.ts` | `transitions pending bubble to confirmed without flash or shift` |
| `SCN-FEED-005` | Failed message delivery visual treatment | `tests/e2e/conversation-feed-rendering.spec.ts` | `displays failed tag on message bubble when send request fails` |
| `SCN-FEED-006` | Failed tool activity card & modal inspection | `tests/e2e/conversation-feed-rendering.spec.ts` | `shows danger styling for failed tools and opens inspection dialog` |
| `SCN-FEED-007` | Context reset timeline partition | `tests/e2e/conversation-feed-rendering.spec.ts` | `displays horizontal reset divider while preserving prior history` |
