---
name: intake
description: Turn an idea into validated requirements before supervisor handoff. Use when a request needs product, technical, scope, or acceptance decisions made explicit.
user_invocable: true
---

# Intake

Turn a rough idea into a small, validated requirement packet. Intake does not implement the work or launch workers.

## Intake loop

1. **Receive the idea.** Restate the intended outcome in plain language. Identify the likely product area, users, and target codebase. Treat the first description as a starting point, not an approved specification.
2. **Find the gaps.** Build only the questions that can change the work. Cover:
   - product outcome, users, workflow, and priority
   - technical boundaries, integrations, data, permissions, and failure behavior
   - in-scope and out-of-scope work
   - acceptance criteria, examples, edge cases, and proof needed
3. **Research before guessing.** Check `~/.mecha-developer/kb/start-here.md` and its always-read links. Inspect relevant local repositories, code, docs, history, and existing patterns. Use the web when it can answer a current or external question. Keep the research targeted.
4. **Separate evidence from assumptions.** For each important claim, record its source: user statement, local file or code path, KB entry, or web source with its date. Mark anything unverified as an assumption. Do not turn an assumption into a requirement.
5. **Ask a clarification round.** Ask the smallest useful group of questions in plain prose. Ask Wolfgang to close the remaining gaps. Include the evidence or assumption when it explains why a question matters. Do not ask Wolfgang to choose between invented menu options. Apply his answers, then repeat only for remaining gaps.
6. **Write the packet.** Once the gaps are closed, present a concise packet and ask Wolfgang to validate it explicitly. Silence, an implied approval, or a reply that does not clearly validate the packet is not validation. If he changes anything, update the packet and ask again.
7. **Hand off only after validation.** Send the entire validated packet through Mailbox to the supervisor. The message must include the packet in full, its evidence and assumptions, and this instruction:

   > Launch the normal managed target worker for this requirement. Pass the full validated requirement packet to it. Launch a same-worktree, read-only companion named `<target>-anxiety` alongside it. Give that companion the full packet too. Tell it to load the anxiety skill, question the target agent's progress against the packet, and create its own recurring ten-minute monitor reminder containing the full packet and monitoring instructions. The companion must not modify files, runtime state, or the target work.

   Use the native `mecha_mailbox_send` tool when available. Use the Mailbox V2 fallback only when the native tool is unavailable. Include the full packet in the message. Do not send a summary that requires the supervisor to reconstruct the requirements.

## Requirement packet

Use this shape. Keep each section short and concrete.

```markdown
# Validated requirements: <name>

## Outcome
<What should be true for the user and why it matters>

## Users and workflow
<Who uses it and the path they take>

## Scope
### In
- <item>
### Out
- <item>

## Technical requirements
- <existing codebase or path>
- <data, integrations, permissions, and failure behavior>
- <explicit constraints or required solution shape>

## Acceptance criteria
- [ ] <observable behavior>
- [ ] <edge case or failure behavior>
- [ ] <validation or live-proof requirement, when applicable>

## Evidence
- <claim>: <source and date, when relevant>

## Assumptions and decisions
- <assumption or unresolved item>: <how it was closed, or say it remains an explicit decision>
```

The packet is not complete while a product, technical, scope, or acceptance decision can materially change the work. If a gap remains, ask about it before requesting validation.

## Compaction rule

Keep a concise current intake snapshot throughout the loop. It must contain the idea, decisions made, open questions, evidence and assumptions, the current packet, and the next action. Save it in a task-local intake state file that survives context compaction. Keep it short enough to reread quickly.

After every completed research round or clarification round:

1. Update the snapshot with what was learned or decided.
2. Decide the next required action: ask the next questions, request explicit packet validation, or send the validated handoff.
3. Send that question, validation request, or handoff before compacting. A pending action means an action that has not been sent yet. Do not compact while that action or handoff is unsent, while the Mailbox send is in progress, or after a failed send that still needs action.
4. Once the outgoing action is sent and any send result is recorded, use `mecha_context` with `action: compact`.

The ten-minute duration belongs only to the anxiety companion's recurring monitor reminder. It is not an intake compaction interval. Do not create a ten-minute intake reminder or compact on a clock. After the validated Mailbox handoff is accepted, intake is complete; do not compact before acceptance.

## Boundaries

- Research is for resolving questions. It does not authorize implementation.
- A source-backed fact, an inferred assumption, and Wolfgang's validated decision must stay distinguishable.
- Do not send the supervisor an unvalidated packet.
- Do not launch the target worker or anxiety companion from intake.
- Do not let the supervisor or companion receive a shortened packet. The target and companion need the same full requirements, with the companion's read-only role added.
