# Sentinel target-worker subagents

## Status

This document is authoritative for the next implementation. It supersedes the
policy-driven Sentinel contract in this repository. This PR changes this
specification and its implementation plan only. It does not deploy, install,
restart, migrate, or otherwise mutate a router or Pi session.

## Scope

**Observable outcome:** any local client can schedule immediate, one-time, or
recurring work for any target Pi worker by supplying `target`, `prompt`, and
`model`. When the existing delivery rules can offer it, the target's Mailbox
extension starts a hidden native Pi subagent. The extension never injects or persists the raw scheduled
prompt in the target parent chat. The subagent receives the target worker's
current effective context and returns completion through Pi's ordinary subagent
integration.

**Non-goals:** Sentinel is not an authorization system, an execution-profile
registry, a child-message protocol, a supervisor relay, a watchdog feature, or
a second scheduler/queue. It does not deploy anything in this phase.

## Required behavior

### 1. Scheduling and routing

1. A caller submits exactly these Sentinel execution inputs: target worker ID,
   opaque prompt, and model. It may submit immediately, at one future time, or on an
   interval through the existing messages and reminders APIs.
2. The router remains loopback transport and durable scheduling only. It stores
   the opaque prompt in the existing message/reminder lifecycle and delivers it
   to `target` when that worker is connected.
3. Sentinel admission is unauthenticated. The router must not use sender
   allowlists, receiver profiles, fixed supervisor recipients, model allowlists,
   child capabilities, test identity headers, or cross-agent restrictions.
   Any syntactically valid local sender may schedule any syntactically valid
   target.
4. The existing queue, reminder occurrence identity, connected-target,
   no-overlap, no-catch-up, retry, expiry, and idempotency semantics remain the
   only delivery/scheduling controls. They apply equally to Sentinel.
5. `from` and `subject`, where the existing API requires or accepts them, remain
   ordinary transport metadata. They do not authorize the dispatch and do not
   select child behavior.

### 2. Target-parent delivery

1. An ordinary message/reminder without `sentinel` preserves existing behavior.
2. For a Sentinel delivery, the target extension must not inject or persist the
   raw dispatch prompt, preview, hash, derived error text, or a status containing
   any of those in the target parent session, UI, logs, or router events. Native
   child output remains ordinary model output and is not rewritten by Sentinel.
3. The target extension records only body-free dispatch status needed to make
   durable delivery/replay observable: message/occurrence identity and native
   subagent identity/status when available.
4. After it has handed the prompt to the native subagent integration, the target
   acknowledges the ordinary delivery using the existing mailbox acknowledgement
   path. A failure before handoff leaves the message subject to the existing
   retry/replay behavior. Sentinel does not add a custom claim, launch, exit,
   tombstone, acknowledgement barrier, or recovery protocol.

### 3. Native Pi subagent handoff

The implementation must use the installed Pi-native subagent integration,
`@gotgenes/pi-subagents`, through its documented `getSubagentsService()` API.
It must call the service from the target extension, not spawn `pi`, not use RPC,
and not create a private result endpoint.

The current managed worker launcher uses `--no-extensions` and loads only the
Mailbox extension, so it does not publish that service today. The implementation
must explicitly load the validated native subagent extension alongside the
validated Mailbox extension. It must retain `--no-extensions`; this is not
permission to restore global extension discovery.

For every accepted Sentinel delivery the extension requests a background native
subagent with:

- `type: "general-purpose"`;
- the opaque scheduled prompt;
- caller-selected `model`;
- `inheritContext: true`; and
- the package's normal background behavior.

This is deliberate. The installed package creates the child from the target
session snapshot, preserves its cwd and effective system prompt, and always
loads the target parent's extensions and skills. Its general-purpose type is a
parent twin with all standard tools. Any installed permission extension remains
the authority for the same parent/child permission flow. Sentinel must not
strip tools, skills, context, extensions, permissions, cwd, or model access,
and must not replace them with fixed configuration.

The subagent package's ordinary lifecycle is authoritative for child execution,
background queueing, notification, transcript persistence, and final result.
The Sentinel extension must not parse, relay, rewrite, or persist the result.
A successful native completion therefore reaches the target parent as the
package's normal `subagent-notification`, not as a router message or Sentinel
status.

### 4. Parent communication

The final completion requirement is satisfied only by the package's native
parent notification path. The implementation must prove that a background child
created through `getSubagentsService().spawn()` produces that notification in
the target parent session.

The public service currently documents spawning, observation, aborting, and
steering, but not arbitrary child-to-parent progress messages. Before adding
any progress-message behavior, implementation must establish a supported
Pi-native parent-message seam. It must not invent a Mailbox child-result route,
relay token, fixed recipient, child capability, or supervisor-specific path. If
there is no supported native seam, implementation stops after delivering the
normal native completion and reports that fact for a product decision.

## Transport shape

`sentinel` is optional on existing message and reminder requests. Its final
field spelling is an implementation detail, but its only execution field is
`model`. The existing envelope `to`/reminder `target` is the target worker;
there is no receiver-policy, origin-sender, sentinel-child, capability, or
supervisor field.

The caller tool exposes:

```text
sentinel_dispatch.py --to <target> --prompt <text> --model <model> [schedule]
```

It may retain ordinary sender/subject/id inputs because the existing APIs use
them. It must remove policy-file, policy, template, fixed-supervisor, and child
configuration arguments. No caller input may choose tools, skills, permissions,
cwd, extensions, relay recipients, or Pi process flags.

## Acceptance criteria

### Contract and compatibility

- [ ] Immediate, delayed, and recurring Sentinel requests accept target,
  prompt, and model without a receiver policy, sender authorization, fixed
  receiver profile, capability, or supervisor recipient.
- [ ] A valid Sentinel request from one ordinary local agent to a different
  target is accepted; an unknown sender is not rejected merely for being
  unknown.
- [ ] Normal messages and reminders continue unchanged when `sentinel` is
  absent.
- [ ] Existing recurrence, connected-target, no-overlap, no-catch-up, queue,
  retry, expiry, and idempotency tests continue to pass for Sentinel delivery.

### Target extension and child

- [ ] The extension never injects or persists the raw Sentinel prompt in target
  parent dispatch entries, custom-message content/details, UI notification,
  extension log, router event, or body-free status. Native child output is
  outside Sentinel's control and is not rewritten.
- [ ] The managed target runtime explicitly loads the validated Mailbox and
  native subagent extensions while retaining `--no-extensions`.
- [ ] The extension calls the native subagent service once per successfully
  handed-off delivery with `general-purpose`, the requested model,
  `inheritContext: true`, and background execution.
- [ ] The native child receives the scheduled prompt and target-parent
  conversation context. It uses the target cwd, effective system prompt,
  extensions, skills, standard tools, and installed permission flow rather than
  a Sentinel-specific reduced profile.
- [ ] Duplicate offers/replay after an acknowledged handoff do not create a
  second native child. A failure before handoff uses ordinary delivery replay;
  no Sentinel lifecycle frames are introduced.
- [ ] The child final completion appears in the target parent through the
  native `subagent-notification` behavior. Sentinel neither sends nor
  transforms a router child-result message.

### Removal proof

- [ ] Production code and the shared protocol contain no Sentinel sender
  allowlist, receiver-policy file/field, fixed receiver/supervisor, child auth
  copy, dedicated child process/RPC host, result endpoint, relay token,
  child-message provenance, or Sentinel lifecycle WebSocket frame.
- [ ] Router and extension tests demonstrate that no removed mechanism is needed
  for a cross-agent target dispatch.

### Live proof

- [ ] On an isolated test router/database and fresh target Pi session with the
  installed `@gotgenes/pi-subagents` package enabled, prove one immediate,
  delayed, and recurring dispatch.
- [ ] Capture the target parent session to show native child completion and that
  no Sentinel dispatch entry contains the scheduled prompt.
- [ ] Capture child transcript/session evidence showing inherited target context
  and effective tool/skill/permission configuration.
- [ ] No shared-router reload, supervisor restart, extension installation, or
  deployment occurs during this proof.

## Evidence and unresolved API facts

This specification is grounded in the locally installed Pi `0.80.10` docs and
`@gotgenes/pi-subagents` `18.0.3` package:

- Pi's own README says core Pi has no built-in subagents and directs extensions
  or packages for this behavior.
- The installed package documents an in-process native subagent core,
  background completion notifications, `inherit_context`, and the
  `getSubagentsService().spawn()` extension API.
- Its implementation creates children using the target cwd and parent system
  prompt, loads parent extensions and skills, and emits native parent completion
  notifications. Its public service exposes no arbitrary child-to-parent message
  method. The current managed launcher does not load that package, so explicit
  dual-extension loading is a required implementation change.

The unresolved fact is therefore narrow: whether the installed Pi runtime has a
supported native API for arbitrary progress messages from a background child to
its parent, beyond the documented final completion notification. This must be
resolved with an isolated compatibility probe before implementation adds that
behavior. There is no approved fallback relay protocol.
