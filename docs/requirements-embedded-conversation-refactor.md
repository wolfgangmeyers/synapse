# Requirements: Embedded Agent Direct Conversation & Mailbox Decoupling

## 1. Executive Summary & Objective

In the legacy architecture designed for tmux-based background workers, agents communicated exclusively through an asynchronous "mailbox" queuing system. When embedded agents were introduced, a workaround was implemented: ordinary conversational text was wrapped into mailbox delivery envelopes, and the router used a "last sender" heuristic to guess who should receive the assistant's reply.

This refactor eliminates that workaround for embedded agents:
1. **Direct Conversational Model**: User messages and assistant replies are treated as first-class conversation messages.
2. **Elimination of the "Last Sender" Mailbox Hack**: Assistant conversational text is posted directly to the conversation thread, not routed through mailbox delivery queues or guessing algorithms.
3. **Mailbox Usage as Timeline Events**: Explicit mailbox tool calls (`mecha_mailbox_send`) are inter-agent actions; they appear on the timeline as inspectable activity events, not primary chat bubbles.
4. **Client Contract Preservation**: The external API contracts for both the WebUI and the Android client (`POST /v1/messages`, `GET /v1/conversations`, and WebSocket `conversation_message` frames) remain strictly backward-compatible.

---

## 2. Key Decisions

### Decision 1: Direct Conversation Delivery (No Mailbox Enveloping)
- **Problem**: When an embedded agent speaks during a turn, the router currently buffers the text into an `embedded_reply_outbox` table and uses `LastHumanRecipient` heuristics (scanning past history across 7 fallback stages) to construct a synthetic mailbox transmission.
- **Decision**: Remove the "last sender" heuristic for embedded agent conversational replies. Normal assistant conversational output is recorded and broadcast directly as an assistant conversation message (`from: agent_id`, `to: user_id`) on the agent's conversation thread.
- **Scope**: Legacy tmux/RPC workers continue to use their existing delivery mechanics; this change applies to embedded agents.

### Decision 2: User Input as Native Conversation Messages
- **Problem**: In the embedded agent session loop, incoming user messages were routed through mail dispatch queues and mailbox reservations.
- **Decision**: Messages sent from the user (WebUI or Android) are native user conversation messages. When submitted:
  - If the embedded agent is idle, the message starts an execution turn directly.
  - If the embedded agent is actively running a turn, the message acts as a live mid-turn steering message.
  - The message is not wrapped in synthetic mailbox envelopes or fake system notifications.

### Decision 3: Mailbox Tool Usage Rendered as Activity Events
- **Problem**: The WebUI previously had logic to detect certain `mecha_mailbox_send` activity events and promote them into chat bubbles, creating confusion between talking to the user and messaging another agent.
- **Decision**: When an agent invokes the `mecha_mailbox_send` tool (e.g. contacting a supervisor or peer worker):
  - It is recorded as an operational activity event.
  - In the UI timeline, it renders as an inspectable event card (showing recipient, subject, and body when clicked).
  - It is never promoted to or duplicated as a primary conversation chat bubble.

### Decision 4: Preserve Client-Server API Contracts (WebUI & Android)
- **Problem**: The Android client strictly enforces JSON response schemas and property keys on `/v1/conversations` and WebSocket frames. Breaking changes would crash or desync the mobile app.
- **Decision**: Keep the external HTTP and WebSocket wire contracts identical:
  - `POST /v1/messages`: Accepts identical envelope payload; returns HTTP 200/201 with `{ "conversation_message": ... }`.
  - `GET /v1/conversations`: Preserves exact JSON schema (`owner`, `peer`, `high_water_sequence`, `messages`).
  - WebSocket `/v1/agents`: Live messages continue to arrive as `type: "conversation_message"`.
  - Attachments: Existing image/file attachment upload, storage, and inline rendering contracts remain unchanged.

---

## 3. Detailed Requirements

### 3.1 Inbound User Messages (`POST /v1/messages`)
- The endpoint accepts user submissions from WebUI or Android:
  ```json
  {
    "id": "<uuid>",
    "from": "<user-id>",
    "to": "<agent-id>",
    "body": "<text>",
    "created_at": "<iso-timestamp>",
    "attachments": [...]
  }
  ```
- **Storage**: The message is stored in the conversation history table with `from = user-id`, `to = agent-id`.
- **Broadcast**: A `type: "conversation_message"` frame is pushed to subscribed WebSocket clients.
- **Agent Delivery**:
  - The message is passed directly to the embedded agent's active execution context.
  - No synthetic mailbox notification headers or bracketed system wrappers are injected into the message body.

### 3.2 Outbound Assistant Responses
- As the embedded agent produces conversational text:
  - The text is recorded as an assistant conversation entry (`from = agent-id`, `to = user-id`).
  - The message is immediately broadcast over WebSocket as a `conversation_message` frame.
  - The message is returned in subsequent `GET /v1/conversations` queries.
- **Eliminated Behavior**:
  - The router will no longer insert conversational text into `embedded_reply_outbox` for delivery via synthetic mailbox routing.
  - The router will no longer run `LastHumanRecipient` fallback scans to guess who should receive assistant conversational replies.

### 3.3 Inter-Agent Mailbox Communications (`mecha_mailbox_send`)
- Embedded agents retain the `mecha_mailbox_send` tool for communicating with other autonomous agents (supervisors, background workers).
- When `mecha_mailbox_send` is invoked:
  - The envelope is delivered to the target agent's inbox as before.
  - An activity entry is logged in the agent's history (`kind = "tool_call"`, `tool_name = "mecha_mailbox_send"`).
  - The WebUI displays this entry as an expandable event card on the timeline.
  - The WebUI timeline filters will not synthesize a standard chat bubble from this tool execution.

### 3.4 Media & Attachments Contract
- Attachments (images and uploaded files) submitted with user messages must continue to:
  - Store safely in the router attachment store (`attachments.Store`).
  - Serve via `GET /v1/attachments/{id}`.
  - Populate the `attachments` array in conversation messages.
  - Display inline within the message bubble in both the WebUI and Android client.

---

## 4. Verification & Acceptance Criteria

1. **Direct Chat Flow**:
   - Sending a message from the WebUI or Android creates a standard user message bubble.
   - The embedded agent responds directly with an assistant message bubble without synthetic mailbox envelopes or "last sender" recipient lookups.
2. **Mailbox Tool Inspection**:
   - When an embedded agent calls `mecha_mailbox_send`, the recipient receives the message.
   - On the embedded agent's timeline, the action appears as an inspectable tool card showing subject, recipient, and body, with no duplicate chat bubbles.
3. **Android Compatibility**:
   - Android client unit and integration tests (`ConversationHistoryTest`, `InboundDeliveryTest`, `OutboundDeliveryTest`) pass without modification.
   - Live Android sessions receive and display incoming messages and operational activity cards without schema errors.
4. **Clean History Hydration**:
   - Refreshing or reconnecting the WebUI or Android correctly hydrates conversations via `GET /v1/conversations` and timeline events via `GET /v1/history`.
