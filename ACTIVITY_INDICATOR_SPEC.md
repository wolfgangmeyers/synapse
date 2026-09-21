# Regular worker activity indicator regression

## Behavior

When a connected regular worker publishes an `agent_activity` event, the React router UI updates that worker from the router's committed `/v1/agents` projection without waiting for the periodic reconciliation poll.

An active event makes the worker row show its existing Working indicator. A settled event removes the Working indicator. Bursts of activity events do not create overlapping HTTP requests, and a state transition that arrives while a refresh is in flight still causes the UI to reconcile to the latest committed state. The existing periodic refresh remains as recovery for missed events and reconnect gaps.

Managed-worker UI-live projection behavior, activity rendering, router storage, protocol schemas, and activity semantics remain unchanged.

## High-level design

The router remains authoritative for activity state. The WebSocket `agent_activity` frame is a notification that the committed HTTP projection changed; it triggers the existing bounded legacy-agent refresh path instead of being reduced directly into UI state.

The transport owns refresh coalescing and overlap control. It must preserve one trailing refresh when an event arrives during an in-flight request so active-to-settled transitions cannot remain stale. The existing React state and `AgentRow` continue rendering the refreshed `activity_state`.

Regression coverage exercises the transport with real-shaped `agent_activity` frames and delayed HTTP responses. It proves active appears promptly, settled clears promptly, event bursts stay bounded, and an in-flight transition converges to the latest snapshot. Functional coverage uses the real Go router and managed-worker fixture where it can assert the shared row indicator without changing managed UI-live semantics.

## Alternatives considered

1. Apply `agent_activity` frames directly to the React agent store. This duplicates router projection logic and can diverge from the router's durable state, fencing, and merged activity sources.
2. Shorten the periodic poll interval. This adds continuous load and still leaves a guaranteed stale window after every lifecycle transition.
3. Restore an event-triggered, coalesced HTTP refresh while keeping the periodic poll. This uses the router's authoritative projection, updates promptly, and retains recovery for missed events.

Chosen approach: option 3. It restores the established transport pattern with the smallest behavior change and keeps projection ownership in the router.
