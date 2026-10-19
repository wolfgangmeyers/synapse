# Technical Specification: Embedded Agent Direct Conversation Refactor

## 1. Core Architecture: Shared Agent Room

An embedded agent has a **single, unified conversation room**:
- **Identical View for All Users**: Every user or client observing the agent sees the exact same chronological stream of messages, tool activities, and events.
- **User Attribution**: Inbound messages are stored with the sender's identity (`from: "username"`). The agent prompt tags the message cleanly (`[Direct message from <username>]: ...`) so the model knows who is speaking in multi-user settings.
- **Direct Assistant Output**: Assistant text commits directly to `conversation_messages` inside the same database transaction as turn completion and immediately broadcasts over WebSocket. There is no outbox table or async polling loop.
- **Mailbox Tools as Normal Tools**: `mecha_mailbox_send` is an ordinary autonomous tool call. It renders on the timeline as an inspectable activity event card, never as a chat bubble.
- **Clean Inbound Format**: All messages entering the agent's context (whether user chat or inter-agent mailbox) use clean text headers instead of serialized JSON envelopes (`{"Version":"embedded-mail/v1",...}`).
- **Strict Wire Compatibility**: External contracts for `POST /v1/messages`, `GET /v1/conversations`, and WebSocket `conversation_message` remain 100% backward-compatible for Android and WebUI.

---

## 2. System Simplifications

### 1. Direct DB Commit (Retire `embedded_reply_outbox` for Embedded Agents)
- **Eliminated**: Asynchronous outbox queuing, outbox lease locking, retries, reaper routines, and the 7-step `LastHumanRecipient` guessing algorithm for embedded agents.
- **Replacement**: When the Pi SDK turn finishes with assistant text:
  1. Commit the message directly to `conversation_messages` (`from = agent_id`, `to = agent_id` or conversation scope).
  2. Broadcast the `type: "conversation_message"` frame over WebSocket to all clients watching the agent.
- Legacy tmux/RPC workers continue using their existing path; embedded agents write immediately and atomically.

### 2. Frontend Simplification (Delete Fuzzy Deduplication)
- **Eliminated**: The multi-pass `isAlreadyInMessages` logic, 120-second timestamp fuzzy matching, and synthetic chat bubble promotion in `MessageList.tsx` and `CompleteTimelineEntry.tsx`.
- **Replacement**: Clean separation of types:
  - Chat messages (`conversation_messages`) render as message bubbles with the sender's username.
  - Tool calls (`mecha_mailbox_send`) render as standard tool activity cards.
  - Because mailbox tool calls are no longer promoted to chat messages, they never collide or duplicate.

### 3. Clean Text Prompt Formatting (Retire JSON Envelopes)
- **Eliminated**: JSON envelope serialization and unwrapping (`{"Version":"embedded-mail/v1", "Subject":...}`) when feeding messages into Pi SDK sessions.
- **Replacement**:
  - Direct user message: `[Direct message from <user>]: <message>`
  - Inter-agent mailbox message: `[Mailbox message from <sender> | Subject: <subject>]: <body>`
  - Saves tokens, eliminates JSON parsing edge cases, and presents natural text to the model.

### 4. Direct Inbound Pathway & Table Separation
- **Eliminated**: Bypasses the asynchronous mailbox leasing/ACK state machine (`ReserveEmbeddedMail`, `AcknowledgeEmbeddedMail`) for direct user chat turns.
- **Replacement**: User chat messages write directly to `conversation_messages` and trigger an immediate turn or mid-turn steering call. The `messages` table remains reserved exclusively for inter-agent asynchronous mailbox mail.

---

## 3. Behavioral Testing Architecture

Per project standards, automated testing exercises **as many real system-owned components together as possible**:
- **Real Components Running Together**: Live HTTP router server, SQLite database, WebSocket multiplexer, embedded session manager, and timeline persistence.
- **Mocked Boundary Only**: The external LLM API boundary is replaced with deterministic, pre-programmed mock completions (e.g. scripted tool calls, thinking, and conversational replies).
- **Test Scenarios**:
  1. **Full Turn**: `POST /v1/messages` -> direct turn -> mock LLM responds -> `conversation_messages` committed -> WebSocket frame received -> `GET /v1/conversations` verified.
  2. **Mid-Turn Steering**: User message arrives while mock LLM is executing a tool -> session steered cleanly with direct user prompt -> verified in timeline.
  3. **Mailbox Tool Isolation**: Mock LLM calls `mecha_mailbox_send` -> envelope queued in `messages` -> tool activity logged in `managed_complete_history` -> zero duplicate chat bubbles in `conversation_messages`.
  4. **Multi-User Room**: Messages from `wolfgang` and `alice` broadcast to all connected WebSocket clients with sender attribution.
  5. **Android Parity**: Existing Android unit and integration tests run against the server contracts without modification.

---

## 4. Wire Contract Guarantees

1. **`POST /v1/messages`**: Unchanged. Accepts `{ id, from, to, body, created_at, attachments }` and returns HTTP 200/201 with `{ "conversation_message": ... }`.
2. **`GET /v1/conversations`**: Unchanged. Returns `{ owner, peer, high_water_sequence, messages }`. Inside `messages`, each entry has `{ id, from, to, body, created_at, sequence, attachments }`.
3. **WebSocket `/v1/agents`**: Unchanged. Broadcasts `{ type: "conversation_message", message: { ... } }`.
4. **Attachments**: Handled identically through `attachments.Store`.
