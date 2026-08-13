# Specification: native subagent activity parity

## Behavior

When a Pi parent session has native subagents running, the mailbox activity published for that parent must represent the parent’s native subagent manager, even when child sessions load or replace a process-global service handle. The published count and summaries must converge after native start/finish events are missed, including while the parent is settled or waiting for native results. Terminal native statuses, including `steered`, must stop contributing to the running count.

The router must continue to expose the received activity through `/v1/agents` and live `agent_activity` frames, with its existing TTL and in-memory semantics. The web UI must render the count and summaries it receives without inventing a second source of truth.

No mailbox delivery, lease, or message persistence behavior changes.

## Design alternatives

1. **Only increase router TTL or add a browser refresh.** Rejected: this cannot recover a wrong or missing count produced by the Pi extension.
2. **Treat terminal UI rows as authoritative in the router.** Rejected: the router cannot observe Pi-native manager state and would create a second, stale source of truth.
3. **Keep a parent-bound native service handle, poll it continuously, and classify all terminal statuses consistently.** Chosen: it preserves the native manager as the source of truth, recovers missed lifecycle events, and limits the change to the extension’s reporting path while keeping router/UI contracts intact.

## High-level design

The Pi extension captures the parent’s native subagent service and uses that handle for reconciliation. Its activity heartbeat remains available while the parent is settled so it can discover native state transitions that did not produce lifecycle events. Reconciliation removes terminal records and reports active records with stable summaries. Existing router and web UI paths remain the transport and presentation layers.

Tests cover parent/child service-handle isolation, missed start and finish discovery while settled, terminal status handling, router snapshots/live activity, and web rendering of received summaries. Runtime validation compares native activity with `/v1/agents` during a bounded parent/subagent run.
