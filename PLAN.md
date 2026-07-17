# Sentinel target-worker subagent plan

## Delivery contract

**Outcome:** replace the current policy-driven detached Sentinel with a generic
unauthenticated scheduler that asks any target Pi worker to create a hidden,
context-inheriting native subagent. Sentinel does not inject or persist the raw
scheduled prompt in the target parent; its native final completion returns there.

**Non-goals:** no deployment, shared-router mutation, extension installation,
process launch/RPC host, child result relay, fixed supervisor path, receiver
profile, sender authorization, or watchdog-specific behavior.

This plan implements `SPEC.md` and nothing beyond it.

## Evidence-led design choice

### Options considered

1. **Keep the current detached Pi/RPC runner and add more context.** Reject. It
   strips the target context/tools/skills/permissions, makes a private relay
   protocol necessary, and conflicts with the required native completion path.
2. **Have the router run a child.** Reject. It moves target-worker context and
   Pi runtime ownership into the scheduler, which must only route and schedule.
3. **Use the installed `@gotgenes/pi-subagents` service from the target
   extension.** Choose. It is already enabled in the local Pi configuration and
   documented as an in-process native subagent integration. Its public service
   can spawn a background `general-purpose` child with a model and inherited
   target context; the package owns completion notification.

The implementation must dynamically import `getSubagentsService()` in the
extension, fail as an ordinary pre-handoff delivery failure if unavailable, and
otherwise make one background native spawn. It must not substitute a custom
subprocess implementation.

The present managed-worker command explicitly uses `--no-extensions` with only
the Mailbox extension, so the service is absent despite the package being
installed locally. The implementation must add the validated native package as
a second explicit `--extension` path. Pi documents that `--extension` may be
used multiple times. It must not re-enable global extension discovery.

## Implementation stages

### 1. Collapse Sentinel transport to scheduling metadata

**Files:** `protocol/v1/schema.json`, `harness/protocol.js`, `harness/db.js`,
`harness/router.js`, current Sentinel migrations plus a new forward-only cleanup
migration if persistent columns/tables must be removed safely.

1. Define the optional Sentinel request metadata as model selection only; the
   existing message `to` and reminder `target` select the target worker.
2. Remove `receiver_policy`, `origin_sender`, `sentinel_child`, relay token, and
   all fixed-recipient/capability fields from protocol validation, persistence,
   reads, and responses.
3. Remove Sentinel admission/policy validation from the router. Preserve normal
   shape/body/queue/rate-limit validation only. Do not use sender identity to
   gate Sentinel or prevent cross-agent targets.
4. Remove Sentinel claim/launch/exit lifecycle frames and repository state.
   Sentinel delivery is one existing mailbox delivery: the target extension
   hands it to Pi's native subagent service, then acknowledges through the
   existing receipt path.
5. Delete the child-result HTTP endpoint and all relay capability data. Do not
   replace either with another endpoint, table, queue, or message format.
6. Preserve migrations safely. Existing databases must continue opening; any
   removal happens through a new forward-only migration, never by editing a
   migration that may already have run.

### 2. Load the native integration in managed target workers

**Files:** `.claude/skills/supervisor/tools/worker_launch.py`, its focused
launcher tests, and the target-extension installation/validation path.

1. Resolve a validated local `@gotgenes/pi-subagents` extension entry point and
   pass it as a second explicit `--extension` argument after `--no-extensions`
   and the validated Mailbox extension.
2. Keep global extension discovery disabled. This adds the required native
   integration without allowing stale globally discovered extensions back into
   managed workers.
3. Test that a managed Pi command has exactly the two required explicit
   extensions, that both paths are validated before tmux creation, and that
   legacy/non-Sentinel worker launch behavior is otherwise unchanged.

### 3. Make the target extension a native-service adapter

**Files:** `pi-extensions/mecha-messaging.js`; delete
`pi-extensions/sentinel-child.js` and `pi-extensions/sentinel-receiver-policies.json`.

1. Keep ordinary mailbox delivery unchanged.
2. Identify Sentinel before the ordinary body-rendering route. Write only a
   body-free custom status entry in the parent session.
3. Dynamically import `@gotgenes/pi-subagents`, obtain its published
   `getSubagentsService()` service, and call:

   ```js
   service.spawn("general-purpose", message.body, {
     description: "Scheduled Sentinel work",
     model: message.sentinel.model,
     inheritContext: true,
   })
   ```

   Do not pass a reduced tool list, private cwd, skills/extensions toggle,
   fixed recipient, profile, auth file, or subprocess/RPC option. Omit
   `foreground` so native background behavior applies.
4. Treat a missing service or a synchronous spawn failure as pre-handoff
   failure: retain the ordinary delivery for existing retry/replay. Treat a
   returned native subagent ID as handoff success and acknowledge by the
   ordinary durable-delivery path.
5. Persist only message/occurrence ID and native subagent ID/status necessary
   for observation. Do not persist prompt content, prompt-derived data, model
   output, or a manual result relay.
6. Let child sessions retain the ordinary Mailbox send capability with a
   transient child sender identity and their target parent as the ordinary
   delivery destination. This supports interim parent-visible child messages
   without adding a Sentinel result route, frame, token, or fixed supervisor.
7. Do not observe native lifecycle events merely to create a duplicate result.
   The installed package already appends its native record and sends its normal
   `subagent-notification` completion to the target parent.

### 4. Simplify the caller

**Files:** `tools/sentinel_dispatch.py` and focused script tests.

1. Retain the thin existing API client and current immediate/reminder request
   shapes.
2. Require `--to`, one `--prompt`, and `--model`; retain optional normal
   sender/subject/id/schedule inputs only where existing API compatibility needs
   them.
3. Delete policy-file/policy/template loading and all profile/model allowlist
   checks. The caller must not inspect a receiver policy or pick child
   configuration.
4. Print the ordinary durable message/reminder ID and endpoint response. It
   does not wait for, parse, or report a child result.

### 5. Delete retired behavior and its tests

**Files:** a new forward Sentinel cleanup migration as needed,
`tests/sentinel_*`, router/protocol/extension suites, `harness_spec.md`, and
any current docs that describe removed behavior.

1. Delete tests asserting sender allowlists, receiver policies, copied auth,
   reduced tools/context, fixed supervisor relay, relay tokens, child-result
   endpoint, or Sentinel lifecycle WebSocket controls.
2. Replace them with behavior tests below. No test should encode the removed
   architecture as a compatibility requirement.
3. Update references that claim Sentinel launches a detached `pi --mode rpc`
   child or requires a special relay. Do not broaden this into unrelated
   documentation work.

## Verification matrix

### Unit and contract tests

- schema/protocol accepts model-only Sentinel metadata and rejects only invalid
  data shape, not unknown sender, target profile, or model policy;
- immediate, one-time, and recurring requests preserve existing durable
  idempotency and scheduling semantics;
- managed worker-launch tests prove the validated native package and Mailbox
  extension are both explicit while `--no-extensions` remains present;
- target extension hands a valid delivery exactly once to a fake native service
  with `general-purpose`, the submitted model, and `inheritContext: true`;
- target parent Sentinel dispatch entries and extension diagnostics omit the raw
  prompt and all prompt-derived content; native model output is not rewritten;
- absent/malformed native service and synchronous spawn error leave delivery
  unacknowledged for normal replay; replay after a successful handoff does not
  make a second child;
- a child interim parent message is an ordinary Mailbox delivery from a
  transient child identity, while native final completion remains separate;
- ordinary messages/reminders are unchanged;
- no protocol, router, or extension code retains policy/relay/RPC/capability
  strings or accepts retired Sentinel lifecycle frames/endpoints.

### Native compatibility probe — required before implementation completion

Run this only against an isolated router/database and a fresh test Pi parent
with the installed package enabled. The probe must prove from runtime evidence:

1. `getSubagentsService()` is present in the target extension session.
2. A background service spawn with `general-purpose`, a selected model, and
   `inheritContext: true` creates a native child.
3. The child transcript shows target-parent context and target effective cwd,
   system prompt, extensions, skills, tools, and permission behavior.
4. The target parent receives the package's ordinary native final completion
   notification while its persisted parent session contains no scheduled prompt.
5. An immediate dispatch, delayed dispatch, and two recurring occurrences each
   obey existing router scheduling/replay behavior.

The probe must record that the package has no native arbitrary-progress API and
then prove an interim child message through the inherited ordinary Mailbox send
capability, from a transient child identity to its target parent. This remains
separate from the native final completion and must not add a Sentinel relay,
result endpoint, token, lifecycle frame, or equivalent side channel.

## Review gates

1. **Design review:** confirm the diff removes every forbidden policy/relay
   mechanism and does not recreate it under a new name.
2. **Contract review:** confirm the target extension is only an adapter to the
   installed native service and that normal mailbox behavior is unchanged.
3. **Compatibility review:** validate the isolated native probe before any
   shared-router or extension rollout is proposed.

## Completion criteria

The implementation is ready only when every `SPEC.md` acceptance criterion is
proven by the listed tests/probe, the three review gates find no blocking issue,
and the unresolved parent-progress API fact is answered from the installed
runtime. No deployment is part of this plan.
