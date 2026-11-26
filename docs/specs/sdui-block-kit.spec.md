# Spec: Server-Driven UI (Block Kit) Architecture & Routing

- **ID**: `SPEC-SDUI-001`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/sdui-block-kit.spec.ts`

---

## 1. Intent & User Outcome

The Server-Driven UI (SDUI) Block Kit architecture replaces monolithic, client-hardcoded mobile application layouts with purely declarative, server-composed Block Kit specifications delivered by the Go router. Users experience instant layout adaptations, dynamic tool and form controls, collapsible diagnostic cards, and responsive agent switching without requiring client application recompilation or APK updates. The client device serves solely as an agnostic, high-performance rendering engine executing generic layout primitives (`section`, `card`, `collapsible`, `actions`, `input`, `image`, `remote_slot`, `divider`) styled in the cyberpunk glassmorphic design language.

---

## 2. Core Invariants

1. **Zero Domain Knowledge on Client**: The mobile client MUST NOT host agent-specific, session-specific, or tool-specific domain logic, hardcoded models, or specialized view controllers. It exclusively renders declarative Block Kit primitives and dispatches generic action envelopes.
2. **Pluggable Slot Hydration**: Root layouts and container views compose dynamic subviews via `remote_slot` primitives. Each slot fetches its child view independently from a designated URL and updates autonomously without requiring full-screen refreshes.
3. **Canonical Action Envelope**: Client interactions (button clicks, form submits, peer selections) MUST package state into a standard action envelope (`action_id`, `block_id`, `agent_id`, `surface_id`, `values`) sent via `POST /v1/ui/action`.
4. **Deterministic Server Mutations**: The router responds to action dispatches with explicit, type-safe response mutations:
   - `update`: Overwrites the active surface view tree.
   - `update_slot`: Directs a specific `remote_slot` (`slot_id`) to load a designated endpoint URL.
   - `push`: Pushes a new modal or navigation surface onto the view stack.
   - `clear_inputs`: Directs the client to reset input values for specified block IDs.
   - `errors`: Binds validation error messages to specified block IDs without altering existing user input.
5. **Safe Markdown & Layout Integrity**: Text within `section` and `card` blocks renders formatted markdown, inline code, and syntax blocks without script execution risks. Empty or malformed payload attributes MUST gracefully degrade with fallback rendering rather than crashing the client or router.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SDUI-001] Root bootstrap and remote slot hydration
- **User Goal**: Launch the mobile application and view the workspace layout with independent sidebar and active conversation views loaded.
- **Given**:
  - The router is running and healthy.
  - Active agents exist in the router store.
- **When**:
  - The client launches and requests `GET /v1/ui/bootstrap`.
- **Then**:
  - The router returns a `Surface` with `surface_id: "root"` containing two `remote_slot` blocks: `sidebar_slot` pointing to `/v1/ui/views/sidebar` and `main_slot` pointing to `/v1/ui/views/agents/{id}/conversation`.
  - The client renders the root surface and concurrently hydrates both remote slots from their respective endpoints.
  - The sidebar slot displays active peer cards with activity halos and status badges.
  - The main slot displays the active conversation header, message cards, and composer input.

### Scenario: [SDUI-002] Remote slot dynamic target switching (`update_slot`)
- **User Goal**: Switch conversation focus to a different agent by tapping their card in the sidebar.
- **Given**:
  - The root view is hydrated with `sidebar_slot` showing agents `alpha` and `beta`.
  - `main_slot` is currently displaying `alpha`'s conversation.
- **When**:
  - User taps the card for agent `beta` in the sidebar.
  - The client dispatches an action payload to `POST /v1/ui/action` with `action_id: "select_peer"`, `block_id: "peer_beta"`, and `agent_id: "beta"`.
- **Then**:
  - The router returns `response_action: "update_slot"` with `slot_id: "main_slot"` and `view_url: "/v1/ui/views/agents/beta/conversation"`.
  - The client updates only `main_slot`, fetching and rendering `beta`'s conversation view.
  - The sidebar view is NOT re-rendered or reset, preserving scroll position and active selection state.

### Scenario: [SDUI-003] Interactive form submission and input clearing (`clear_inputs`)
- **User Goal**: Send a message to an agent using the composer block.
- **Given**:
  - The conversation view for agent `alpha` is open in `main_slot`.
  - The composer input block `composer_input` contains action `message_text`.
- **When**:
  - User types "Run test suite" into `message_text` and taps the "Send" button.
  - The client dispatches `POST /v1/ui/action` with `action_id: "send_message"`, `agent_id: "alpha"`, and `values: {"composer_input": {"message_text": "Run test suite"}}`.
- **Then**:
  - The router processes the message send.
  - The router returns `response_action: "clear_inputs"` targeting `["composer_input"]`.
  - The client resets the composer input field while retaining focus and scroll position.

### Scenario: [SDUI-004] Server-side validation and error mapping (`errors`)
- **User Goal**: Correct invalid input in an agent configuration view.
- **Given**:
  - The user views the configuration screen for an agent via `GET /v1/ui/views/agents/{id}/config`.
  - A required configuration input block `cfg_temperature` is present.
- **When**:
  - User enters an invalid value (e.g. `2.5` or non-numeric) and taps "Save Config".
  - The client dispatches `POST /v1/ui/action` with `action_id: "save_config"`.
- **Then**:
  - The router detects the validation failure.
  - The router responds with `response_action: "errors"` containing `errors: {"cfg_temperature": "Value must be between 0.0 and 1.0"}`.
  - The client highlights the `cfg_temperature` block with an inline error message without clearing existing user input or resetting other form controls.

### Scenario: [SDUI-005] Modal surface push navigation (`push`)
- **User Goal**: Open an agent's full settings panel in a modal sheet.
- **Given**:
  - The user is viewing an agent conversation.
  - The header contains a settings button with `action_id: "open_config"`.
- **When**:
  - User taps the settings button.
  - Client dispatches `POST /v1/ui/action` with `action_id: "open_config"`, `agent_id: "alpha"`.
- **Then**:
  - The router returns `response_action: "push"` containing a new `Surface` (`surface_id: "config_alpha"`, `title: "Agent Configuration"`).
  - The client pushes the modal surface onto the navigation stack over the active surface.

### Scenario: [SDUI-006] Whole view replacement mutation (`update`)
- **User Goal**: Refresh or replace the active view after a workflow state transition or reset.
- **Given**:
  - An interactive view is currently loaded.
- **When**:
  - An action completes that requires completely re-rendering the current surface.
- **Then**:
  - The router returns `response_action: "update"` containing the updated `Surface`.
  - The client replaces the surface tree in-place, animating changes according to the Block Kit diff.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SDUI-001` | Root bootstrap and slot hydration | `tests/e2e/sdui-block-kit.spec.ts` | `bootstraps root surface and hydrates remote slots` |
| `SDUI-002` | Dynamic slot update on peer switch | `tests/e2e/sdui-block-kit.spec.ts` | `switches target slot conversation on peer select` |
| `SDUI-003` | Message submission and input clearing | `tests/e2e/sdui-block-kit.spec.ts` | `dispatches send action and clears composer input` |
| `SDUI-004` | Config validation error mapping | `tests/e2e/sdui-block-kit.spec.ts` | `renders inline field errors from error response` |
| `SDUI-005` | Modal surface push navigation | `tests/e2e/sdui-block-kit.spec.ts` | `pushes modal surface on navigation action` |
| `SDUI-006` | In-place view tree replacement | `tests/e2e/sdui-block-kit.spec.ts` | `updates surface in-place on update response` |

Supporting Go unit tests in `router-proxy/internal/sdui/sdui_test.go` and `router-proxy/internal/transport/sdui_http_test.go` verify valid JSON serialization, schema correctness, endpoint handlers, and action mutation dispatching.
