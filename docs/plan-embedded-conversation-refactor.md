# Implementation Plan: Embedded Agent Direct Conversation Refactor

## Behavioral Test-Driven Implementation Strategy
All changes follow the strict `dev-loop` workflow:
1. Build an end-to-end behavioral test harness putting all real components together (HTTP router, SQLite DB, WebSocket hub, embedded manager) with a deterministic mock LLM.
2. Verify all existing behaviors and assert the new contracts before touching production code.
3. Implement in modular, verifiable phases.

---

## Phase 1: End-to-End Behavioral Test Harness
- **Target**: Create `router-proxy/internal/managed/embedded_conversation_e2e_test.go`
- **Scope**:
  - Spin up real in-memory/temp SQLite DB, HTTP router, WebSocket listener, and embedded manager.
  - Wire a deterministic mock LLM provider to script responses, tool executions, and mid-turn steering.
  - Establish behavioral test cases:
    1. Direct user chat -> immediate mock reply -> WebSocket `conversation_message` frame verified.
    2. Mid-turn steering during tool execution.
    3. `mecha_mailbox_send` tool execution -> queues in mailbox -> zero duplicate chat messages.
    4. Multi-user attribution (`wolfgang`, `alice`).
- **Done When**: Behavioral tests compile and run, validating baseline expectations.

---

## Phase 2: WebUI Activity Rendering & Deduplication Cleanup
- **Files**: `web/router-ui-react/src/components/CompleteTimelineEntry.tsx`, `web/router-ui-react/src/components/MessageList.tsx`
- Remove `isMailboxSendToUser` chat bubble promotion so `mecha_mailbox_send` renders strictly as a tool activity card.
- Strip out fuzzy timestamp matching (`isAlreadyInMessages`) since chat messages and tool activities are cleanly separated by type.
- Verify message bubbles display the sender's username (`from`).
- **Verification**: Run frontend tests; verify in WebUI that `mecha_mailbox_send` appears as a clickable card, not a chat bubble, and no duplicate entries appear.

---

## Phase 3: Clean Text Inbound Feeding & Steering
- **File**: `router-proxy/internal/managed/embedded_mail.go`
- In `trySteerActiveSession` and `runEmbeddedMail`:
  - Replace JSON envelope wrapping (`{"Version":"embedded-mail/v1",...}`) with simple clean text headers:
    - User messages: `[Direct message from <user>]: <message>`
    - Mailbox messages: `[Mailbox message from <sender> | Subject: <subject>]: <body>`
  - Pass clean user text and attachments directly to the SDK turn or mid-turn steering call.
- **Verification**: Behavioral E2E tests for direct chat and mid-turn steering pass.

---

## Phase 4: Direct Assistant Reply Publication & Outbox Retirement
- **Files**: `router-proxy/internal/managed/embedded_session.go`, `router-proxy/internal/store/embedded_reply.go`
- In `AppendMessage`, write completed assistant text directly to `conversation_messages` inside the turn transaction and broadcast via WebSocket `conversation_message`.
- Stop inserting into `embedded_reply_outbox` for embedded agent turns; retire `LastHumanRecipient` guessing for embedded agents.
- **Verification**: Behavioral E2E tests confirm assistant replies commit immediately to `conversation_messages` and broadcast over WebSocket without outbox latency.

---

## Phase 5: Regression & Parity Verification
- Run Android test suite: `ConversationHistoryTest`, `InboundDeliveryTest`, `OutboundDeliveryTest`.
- Run router Go tests: `go test -v ./router-proxy/internal/...`.
- Run frontend build/tests: `npm run lint`, `npm run build`.
- Live browser test: Send messages, verify prompt attribution, verify mid-turn steering, and verify mailbox tool cards.
