---
name: roger-report-implementation
description: Create a new RogerEMR fixed report from evidence and specification through an independently verified implementation. Use for Census, Schedule, Patient Diagnoses, Open OASIS, Worklog, Wound Status, and similar RogerEMR report work where correctness depends on source-report parity, row grain, filters, exports, permissions, stacking, generated API contracts, and a usable isolated demo. Do not use for Agency Dashboard widgets or generic reporting infrastructure alone.
---

# Roger Report Implementation

Use this skill for the report-specific parts of creating a RogerEMR report. It supplements rather than copies the canonical development process.

## Required process and model gates

1. Load the `workflows` skill, retrieve the canonical `dev-loop` workflow with `get_workflow.py dev-loop`, and follow it as the procedural source of truth. Do not paste or recreate the workflow in a worker handoff.
2. Use managed Pi workers/subagents on OpenAI Codex GPT-5.6 Sol for report implementation and repo-local research unless Wolfgang explicitly requests another model. Use GPT-5.5 for the authoritative specification and implementation plan. Also require a GPT-5.5 research pass when the report needs Kinnser/WellSky/browser access, credentialed source-report parity, or other external evidence Sol cannot inspect directly. Complete the evidence-gathering pass before creating the final specification and plan from the researched report contract, source evidence, comparable code, field lineage, stack/base recommendation, and known open questions. For a complex report, run a separate independent pass over the specification and plan for omissions, unsupported assumptions, and spec-to-plan coverage before the normal plan-review gate.
3. “Worker” means a persistent tmux/Pi worker. If Wolfgang asks for a worker, do not substitute a native subagent. Use background subagents only for bounded lanes that do not need durable ownership.
4. Load the RogerEMR repository `AGENTS.md` and `TESTING_GUIDANCE.md`, plus any domain skill triggered by the report. Before final readiness, load and follow `roger-report-review` as the report-specific independent review gate.

## Report evidence and specification

The dev-loop specification must make the report contract explicit before planning:

- user question and product location;
- every mode/view the report supports, with its own columns, filters, row grain, date semantics, sorting, totals/counts, and export shape;
- row grain and stable row identity;
- inclusion/exclusion population, date field, boundaries, and business timezone;
- source and lineage for every displayed/exported field;
- filters, search, sorting, pagination, counts, null/unavailable states, and export behavior;
- every result-affecting query parameter, including hidden/default/deep-link parameters, with an explicit decision to expose, clear, ignore, or preserve it;
- agency isolation, permission, feature/navigation visibility, query/index posture, and known dependencies;
- shared synthetic reporting scenarios needed for tests, local demo, and cross-report comparison; and
- alternatives and deliberate parity exceptions.

Start with Linear, accepted specs/decisions, current repository code, comparable fixed reports, generated API conventions, and relevant KB entries. Treat old report specs as evidence, not current policy: explicitly reconcile stale permissions, bases, and unmerged assumptions. Do not hand the work to Sol for the final specification or implementation plan until this research is complete enough to state the report contract and the remaining unknowns; otherwise the Sol output is only a preliminary scaffold and must be revisited after research.

### WellSky/Kinnser parity

When the request references a WellSky/Kinnser report or workbook, or parity depends on it:

- use the `browser-console` skill and authorized Kinnser/WellSky access to inspect the actual report and export/download the workbook; a missing ticket attachment is not proof that the workbook is unknowable;
- record only PHI-safe structure: route, controls/defaults, row grain, headers/order, sheet shape, sorting, formatting, totals, and relevant edge behavior;
- keep downloads in an ignored local artifact directory and never print, mail, commit, or summarize credentials, PHI, or raw rows;
- map every retained source column to an authoritative RogerEMR field or derivation; document unsupported fields instead of guessing or silently returning blanks; and
- do not claim full parity until the export/header and behavior comparison is complete. If authorized access is genuinely unavailable, state the exact limitation and obtain a product disposition rather than treating guessed parity as complete.

## Canonical RogerEMR report decisions

### Permission

Every RogerEMR fixed report must be gated by `roger-emr-reports:access`. Use that permission for RogerEMR Census/fixed-report access across backend endpoints, frontend routes, report cards, navigation, and local/demo role setup. Do not use RogerDocs' `reports:access`, invent granular slugs such as `reports:patients` or `reports:episodes`, add report-specific roles, or auto-grant report access to admin/external-admin. If an older spec says otherwise, correct the spec/plan and keep moving unless Wolfgang explicitly changes the permission model.

Apply the same access decision to backend endpoints, direct routes, reporting cards/navigation, and frontend visibility. Backend enforcement remains authoritative. Before implementation handoff and again before push/PR readiness, explicitly verify that every open report in the stack still uses `roger-emr-reports:access`, including any base report the new work depends on.

### Population, filters, and display

- Build one shared report service/relation so list rows, filtered counts, sorting, pagination, options, and export cannot drift.
- Explicitly constrain every tenant-owned join; ambient agency filtering is defense in depth. Preserve the declared grain and do not hide fan-out with `DISTINCT`.
- Validate every filter across UI value, URL state, generated client, OpenAPI, API parsing, query semantics, and export. Define All/omitted and null/Unknown/Unclassified behavior.
- All filters should include nullable/unassigned rows unless the approved contract explicitly excludes them. Filter options should come from canonical backend contracts and use the same labels shown in the table/export, not partial frontend-only lists.
- Validate bounded fields at both UI and API boundaries: blank, malformed, negative, below-minimum, above-maximum, equal-bound, reversed-range, and boundary values. Make inclusive/exclusive minimum and maximum semantics explicit.
- Debounce every freeform text or text-like filter before it changes URL state, sends a backend query, or reloads the report. Use the existing debounce/query-state pattern. Export/print must use the latest visible debounced query values or stay disabled while the pending input has not yet become the visible report state, so exports are never broader than what the user sees.
- Patient search must match the names users actually see, including the displayed full-name order(s), as well as any explicitly supported MRN/code fields.
- Sort derived columns by what users see. Keep null/Unknown/Unavailable values last unless the approved contract says otherwise, and do not expose sorting for placeholder or unavailable-only fields. OpenAPI sort enums, frontend options, and backend validation must match exactly.
- Format structured or nullable values deliberately. Never let arrays/objects render as `[object Object]`; never let a blank look like a meaningful “none.” Use short, clinician-friendly labels and existing Roger design-system components.
- If a report defines unavailable reasons or helper-derived values, expected missing helper inputs/dependency failures should produce intentional unavailable rows when that is the contract, not abort the whole report/export. Exclude invalid or missed source records before ranking or selecting the source row.

### Export parity

The export must use the same filters, population, sort, labels, values, null semantics, and user-visible columns as the report unless the approved spec explicitly documents a source-workbook difference. A reviewer should be able to compare the screen and workbook column by column.

Export the full filtered result, not the visible page. Preserve identifier text, real date/number cells, safe formula-leading text handling, documented row limits, and source-workbook formatting that matters to parity. Test export headers/order and representative formatted values, not only the response status.

Do not cap, page, or truncate rows before computed filters/sorting unless the response includes an explicit overflow state and every surface handles it. List total/count, filtered result, and export must agree.

### API, queries, and migrations

Use generated frontend API types and regenerate the client after backend route/schema changes. Review the generated diff rather than hand-editing drift. Report pages must handle generated-client calls that resolve 4xx/5xx with `data` undefined as well as calls that throw; preserve safe status/detail handling for list, options, and export calls. Confirm agency isolation, deterministic tie-breakers, count/pagination agreement, and safe object-level 404 behavior.

Inspect indexes for selective filters, joins, and ordering. Add only justified indexes, with matching ORM metadata and an Alembic migration following RogerEMR conventions; verify the current migration head. Measure representative paths when query shape or volume makes the need uncertain.

## Stack and branch discipline

Determine dependencies from the current remote heads before implementation. Before sibling report work, inspect whether an open report branch split shared routing or report files such as `backend/app/api/reports.py` into a reports package. A shared split report-router/file change can require a report PR to be stacked **directly on Patient Diagnoses**. When that dependency exists, do not base on main and do not chain through Schedule, Open OASIS, Worklog, Wound Status, or another sibling report merely because it was implemented earlier. Add another report layer only for a concrete code dependency.

Before any authorized push or base update, verify all of the following against the current remote base, not a stale recorded SHA:

- intended PR base and branch chain;
- merge-base equals the current direct base head;
- the PR diff contains only this report and its justified shared changes;
- generated files and migrations belong to the correct layer; and
- restacking will not disturb another report's worktree/runtime.

Repeat this check whenever the base moves.

## Isolated local demo

Use the repo-local Roger worktree/bootstrap skill and scripts. Preserve every existing worktree, Compose project, database, tmux window, and port. Do not manually reuse or redirect another environment, allocate an unauthorized port, or change WorkOS callbacks as a shortcut. Check the WorkOS local-callback KB when login fails; the callback uses the API port.

Treat local auth, report permission assignment, shared synthetic fixtures, dependency rebuilds, WorkOS API callback checks, and service restarts as owned local setup. Prefer the first-class shared synthetic reporting dataset/loader when it exists; extend it deliberately instead of creating isolated one-off seeds. Seed unmistakably synthetic, local-only data sufficient to exercise every meaningful filter bucket, null/unavailable state, search mode, date boundary, sort, pagination where applicable, and at least one multi-filter combination. The same fake patients/admissions/episodes/visits/tasks/diagnoses should support cross-report consistency checks where reports overlap. Do not use PHI or commit temporary seed hacks.

Before handoff, freshly prove runtime ownership: Vite's cwd/command is this worktree, API/DB containers belong to its Compose project, and listeners match its recorded ports. Use Browser Console Bridge to exercise the report and export. A `200` is insufficient: verify rows, controls, visible/export parity, no console errors, and no Vite import-analysis overlay.

## Report-specific completion gate

In addition to every canonical dev-loop gate, do not request git authorization until an independent pass confirms:

- the implementation matches the approved report contract and any validated WellSky/Kinnser evidence;
- every mode/view has its own verified contract for columns, filters, grain, sorting, and export shape;
- every report input, hidden/default parameter, bounded value, and meaningful null/unavailable state has focused backend/frontend coverage;
- list/count/options/export and generated contracts agree;
- caps/overflow behavior cannot make list, count, and export disagree;
- search, display formatting, sorting/null behavior, and export parity are correct;
- permission, agency isolation, stack/base/diff, indexes/migrations, and local runtime ownership are correct;
- focused backend/frontend checks and useful coverage have passed;
- `roger-report-review` and the canonical robot-review/pr-validate loops are clean on the current diff; and
- the synthetic browser walkthrough is meaningful and free of console/Vite overlay failures.

Map every changed file to the approved spec or a validated review finding. Remove unrelated changes.

## Authorization boundary

Commit authorization is not push authorization. Never commit, push, force-push, change a PR base/state, open/update a PR through a push, merge, or enqueue without the corresponding explicit authorization. State exactly which operation is requested and wait when authorization is required. Preserve draft/ready state and keep the isolated review environment alive until merge/close or explicit teardown authorization.
