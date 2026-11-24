# Spec: Attachments & Thumbnail Gallery

- **ID**: `SPEC-CONV-002`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/conversation-attachments.spec.ts`

## 1. Intent & User Outcome

Users can view image attachments in conversation messages as interactive thumbnails that open into a clean, dedicated fullscreen preview modal. The modal overlays all timeline and page elements, locks body scrolling, preserves image aspect ratio within the viewport, and supports intuitive dismissal via close button, backdrop click, or the Escape key.

## 2. Core Invariants

1. Opening an image attachment thumbnail MUST render a centered preview modal overlay attached directly to `document.body` outside message DOM hierarchy.
2. The preview modal overlay MUST prevent background document/timeline scrolling while open (`overflow: hidden` on `document.body`) and restore the original scroll behavior upon dismissal or component unmount.
3. The preview modal MUST provide standard dismissal controls: a circular `.icon-btn-round` close button, backdrop click dismissal, and the `Escape` key.
4. Clicking directly on the preview image MUST NOT dismiss the modal (`stopPropagation`).
5. Dismissing the modal MUST clean up the overlay and restore interactive access to the conversation timeline.

## 3. User Scenarios

### Scenario: [ATT-001] Open and close attachment preview modal via click and dismiss button
- **User Goal**: Expand an image attachment to view it at full size and close it when done.
- **Given**: A message in the conversation timeline containing an image attachment thumbnail.
- **When**: The user clicks the image thumbnail.
- **Then**:
  - A centered modal overlay renders at the root DOM level (`document.body`) above all page contents with dimmed backdrop.
  - Body scrolling is locked (`document.body.style.overflow === "hidden"`).
  - The modal has `role="dialog"`, `aria-modal="true"`, and `aria-label="Image preview"`.
- **When**: The user clicks the close button (`.icon-btn-round.image-lightbox-close`).
- **Then**:
  - The modal overlay is removed from the DOM.
  - Body scrolling is restored to its previous value.

### Scenario: [ATT-002] Dismiss preview modal via Escape key and backdrop click
- **User Goal**: Dismiss an open image preview quickly using standard keyboard or backdrop interactions.
- **Given**: The image attachment preview modal is currently open.
- **When**: The user presses the `Escape` key.
- **Then**:
  - The modal closes immediately and body scrolling is restored.
- **When**: The user opens the preview modal again and clicks the dimmed backdrop area outside the image.
- **Then**:
  - The modal closes immediately and body scrolling is restored.

### Scenario: [ATT-003] Clicking image content does not dismiss modal
- **User Goal**: Interact with or inspect the image without inadvertently dismissing the preview modal.
- **Given**: The image attachment preview modal is currently open.
- **When**: The user clicks directly on the preview image element inside the modal.
- **Then**:
  - The click event does not propagate to the backdrop.
  - The preview modal remains open and visible.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `ATT-001` | Open and close attachment preview modal via click and dismiss button | `tests/e2e/conversation-attachments.spec.ts` | `opens and closes lightbox modal via thumbnail and close button` |
| `ATT-002` | Dismiss preview modal via Escape key and backdrop click | `tests/e2e/conversation-attachments.spec.ts` | `dismisses preview modal via Escape key and backdrop click` |
| `ATT-003` | Clicking image content does not dismiss modal | `tests/e2e/conversation-attachments.spec.ts` | `clicking image content does not dismiss modal` |
