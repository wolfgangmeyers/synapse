# Spec: Conversation Links Widget

- **ID**: `SPEC-CONV-LINKS-001`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/conversation-links.spec.ts`

## 1. Intent & User Outcome

While chatting with an agent, links get buried in the scrollback. The conversation header has a Links button that opens a dialog listing every hyperlink either side has shared, so Wolfgang can find and open them quickly. He can also add links by hand and delete or clear entries. It is purely a UI convenience: agents have no tools for it and nothing is sent to the router.

## 2. Core Invariants

1. Links are collected from both incoming and outgoing messages of the selected agent's loaded conversation. Pending and failed messages are ignored.
2. Only `http` and `https` URLs are collected, from markdown links (`[label](url)`) and plain URLs. Other schemes (for example `javascript:`) are never listed or linkable.
3. Markdown links show their label. Plain URLs show a short `host/path` preview, truncated to 48 characters. The full URL is shown under the label only when the label differs from that preview.
4. Identical URLs appear once, ordered newest first by their most recent mention. A later bare repeat of a URL keeps the label from an earlier markdown mention.
5. Links open in a new tab with `rel="noopener noreferrer"`.
6. The add-link form is a separate view from the list; the two are never shown together. Closing the dialog discards any half-entered form.
7. Deleting a link hides it until a newer message mentions the URL again. A manually added link that shadows a message link is deleted together with it.
8. State is per agent, in memory for the page session only, and never persisted. Retention is bounded: at most 200 manual links per agent, a bounded deletion record, and the 20 most recently used agents.
9. The header button shows the link count and is highlighted (blue) when there is at least one link. It is absent when no agent is selected.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [LINK-001] Links from both directions are collected
- **User Goal**: See every link shared in this conversation in one place.
- **Given**: Wolfgang sent a message containing a plain URL and the agent replied with a markdown link and another plain URL.
- **When**: He clicks the Links button.
- **Then**:
  - The button reads `Links (3)` and the dialog lists three links, newest first.
  - The markdown link shows its label with its full URL beneath it.
  - Plain URLs show a `host/path` preview.
  - No add form is visible.

### Scenario: [LINK-002] Add a link manually
- **Given**: The Links dialog is open on the list view.
- **When**: He clicks "+ Add link", enters `https://manual.example.net/page` with label "Manual Page", and clicks Save.
- **Then**: The list view returns with the new link at the top, and the count increases.

### Scenario: [LINK-003] Invalid URL is rejected
- **Given**: The add form is open.
- **When**: He enters `not a url` and clicks Save.
- **Then**: An alert "Enter a valid http(s) URL." appears and the form stays open. Cancel returns to the unchanged list; reopening the form shows empty fields.

### Scenario: [LINK-004] Delete one link or clear all
- **When**: He clicks the remove button on a link, then "Clear all".
- **Then**: The removed link disappears, then the list shows "No links yet." and the header button is no longer highlighted.

### Scenario: [LINK-005] A deleted link reappears when mentioned again
- **Given**: He deleted a link that came from a message.
- **When**: A newer message mentions the same URL.
- **Then**: The link is listed again.

### Scenario: [LINK-006] Links stay with their agent
- **Given**: He added or deleted links while viewing agent A.
- **When**: He switches to agent B and back.
- **Then**: B shows only B's links; A's additions and deletions are still applied.

### Scenario: [LINK-007] Unsafe or malformed links are ignored
- **Given**: A message contains `[x](javascript:alert(1))` or an `ftp://` URL.
- **Then**: Nothing is listed for it.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `LINK-001`–`LINK-004` | Collect from both directions, add, reject invalid URL, remove, clear | `tests/e2e/conversation-links.spec.ts` | `conversation links collect shared links and support add, remove and clear` |
| `LINK-005`–`LINK-007` | Resurfacing, per-agent state, unsafe schemes | Unit/component coverage only: `web/router-ui-react/tests/unit/links.test.ts`, `web/router-ui-react/tests/component/conversation-links.test.tsx` | Pure client-side state rules with no router boundary involved |
