# Complete activity timeline implementation

The selected managed agent’s **Complete** tab is now an operational timeline rather than a generic activity log.

## What changed

- The router stores a small closed presentation object with each meaningful Complete entry. It carries only event kind, safe state/outcome metadata, optional safe summaries, and a bounded error code. Tool arguments, output, request content, process/session data, credentials, and paths are not part of this projection.
- Parent lifecycle/state changes, model changes, tool results, reminder delivery, direct-delivery failures, child lifecycle/recovery, and parent/child compaction produce recognizable canonical facts. Retry keys and source-fenced child/compaction writers keep those facts idempotent.
- Complete keeps ordinary conversation messages as message bubbles. It does not add duplicate activity cards for the same mailbox message. Bubble metadata now makes received mail and sent mail visually distinct.
- Timeline cards use the agreed mappings: bash terminal/teal, goal flag/purple, reminder clock/yellow, fallback wrench/blue, state arrow/green, compaction/purple, child/recovery treatments, and danger treatment for failures.
- Entries are ordered by timestamp and stable sequence, grouped only with date dividers, scoped to the selected managed agent, and deduplicated by their durable ID. The timeline uses a small polite live region only for newly arrived high-value events; selection and retained-history replay are silent.
- Complete history remains capped at 200 rows. A managed agent’s Complete entries and private child/compaction state are deleted with the agent so a later agent using the same name cannot inherit them.

## Accessibility and safety

Timeline entries are list items with readable labels, hidden decorative icons, localized `<time>` values, non-color failure text, and a named timeline. There is no unsafe error-details control: current producers expose no independently proven-safe diagnostic detail, so failure cards state that additional safe detail is unavailable.

Both router and browser validation reject unbounded or sensitive presentation values before they reach the timeline. Older rows without presentation metadata continue to use a conservative legacy card.

## Validation

Focused router history, termination, UI-live, and managed-control tests pass. The focused React component, transport, and protocol suites pass; TypeScript typecheck, lint, and production build pass. Focused React coverage is 77.55% across the four changed presentation/transport modules (MessageList 100%, CompleteTimelineEntry 91.95%). Browser Console Bridge confirmed the existing local router’s Complete tab renders its message bubble and retained activity cards with the named timeline and no console errors. That router only has legacy persisted rows, so the structured icon paths are covered by component tests rather than local historical data.

The broader local router-native tests remain blocked by the pre-existing installed Pi source-lock mismatch, and unrelated existing frontend tests have independent failures in the legacy compacting header and managed-agent dialog timing paths.
