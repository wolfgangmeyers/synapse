# ADR 0007: Composer Draft State Isolation

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

Users frequently reported typing lag, stutter, and dropped keystrokes in the web UI when composing long prompts or interacting with active conversations:

1. **Root-Level State Pollution**: The prompt input draft state (`composerDraft`) and cursor selection (`composerSelection`) were hoisted to the top-level application root (`web/router-ui-react/src/App.tsx`).
2. **Cascading Tree Re-Renders**: Every single keystroke in the message composer triggered `setComposerDraft(...)` in `App.tsx`. Because `App.tsx` (over 900 LOC) is the parent of the navigation sidebar, agent lists, active conversation headers, and the message feed, every character typed forced React to re-render the entire component tree.
3. **Feed Rendering Pressure**: When conversations grew to hundreds of entries, re-evaluating 500+ un-memoized feed DOM nodes on every keystroke consumed 30–80ms of main-thread CPU time, dropping frame rates well below 60fps and causing noticeable typing freezes.

Composer text input must feel instantaneous regardless of how large the conversation timeline is or how many agents are running.

---

## Decision

Completely isolate composer draft state from the root React application tree:

1. **Localize Composer State**:
   - `composerDraft` and `composerSelection` state are moved down into the localized composer component tree (`Composer.tsx` or a dedicated composer state hook).
   - Typing into the input textarea must never trigger state updates or re-renders in `App.tsx`, `Sidebar.tsx`, `ConversationHeader.tsx`, or `MessageList.tsx`.
2. **Submit-Only Escalation**:
   - The root application tree is notified only upon explicit message submission (hitting `Enter` or clicking the Send button) or explicit draft persistence triggers.
3. **Draft Persistence via Dedicated Storage Engine**:
   - Autosaving drafts across agent switches or browser refreshes uses debounced background persistence (e.g. `localStorage` or `useDebouncedCallback`) decoupled from the React rendering loop.

---

## Invariants & Guarantees

- **60fps Keystroke Responsiveness**: Keystroke latency must remain under 16ms regardless of timeline length or agent count.
- **Render Boundary Invariant**: Typing in the prompt box must produce zero re-renders in `MessageList` or `App.tsx`.
- **Draft Durability**: Switching between agents preserves unsent drafts without mutating global conversation state.

---

## Consequences

### Positive
- Completely eliminates typing freezes and input lag.
- Vastly improves UI performance on lower-power laptops and mobile devices over Tailscale.
- Simplifies `App.tsx` props drilling, removing composer state handlers from intermediate containers.

### Negative
- Components outside the composer that previously peeked directly into `composerDraft` must now access draft state through an isolated store or explicit callbacks.

---

## References

- Architectural Diagnosis: `docs/master-conversation-architecture-simplification.md` (Issue 5: Typing Lag & Freezes)
- Behavioral Spec: `docs/specs/conv-feed-rendering.spec.md`
- Frontend Components: `web/router-ui-react/src/App.tsx`, `web/router-ui-react/src/components/Composer.tsx`
