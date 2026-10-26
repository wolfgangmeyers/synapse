---
name: roger-advanced-report-implementation
description: Implement RogerEMR advanced, self-serve, OLAP reports and presets on the governed dataset/Parquet/DuckDB-WASM Report Builder architecture. Use for dataset columns, lineage, extracts, joined datasets, system reports, saved/custom reports, client-side grouping, pivots, measures, filters, and charts. Do not use for fixed server-side RogerEMR reports; use roger-report-implementation for those.
---

# Roger Advanced Report Implementation

Use this skill when the report belongs in RogerEMR's advanced reporting architecture: engineer-governed datasets are materialized as permission-scoped Parquet extracts, DuckDB-WASM compiles and runs the report in the browser, and the product exposes a system preset or a user-authored saved report.

This skill supplements the canonical development process. It does not replace it.

## Choose the right report architecture first

Use this skill for advanced/self-serve/OLAP/report-builder work, including:

- adding a source-backed field or relationship to a governed reporting dataset;
- creating a code-shipped system report in `backend/app/reporting/system_reports.py`;
- enabling users to build, group, aggregate, pivot, filter, chart, save, copy, share, or export a report in the browser; or
- changing Parquet extraction, manifests, partition pruning, DuckDB-WASM registration, report compilation, or the `/reports` advanced reporting surfaces.

Use `roger-report-implementation` instead when the product contract is a dedicated server-side report endpoint/page with server pagination, a fixed table/export schema, or source-workbook parity that should not be editable in Report Builder. A report appearing on the shared `/reports` landing page does not by itself make it advanced: trace how its rows are computed. Do not build both architectures unless the approved specification explicitly requires both and defines their parity contract.

## Required process and subagent gates

1. Reference the `workflow-management` skill, follow the canonical router-managed `dev-loop` workflow as the procedural source of truth. Load the target RogerEMR checkout's `AGENTS.md` and `TESTING_GUIDANCE.md` before planning or editing. State the RogerEMR pre-edit domain gate, target behavior/artifact, and planned verification.
2. Inspect the current remote/base and the actual task worktree. Reporting has evolved quickly; do not infer frontend state from a stale checkout, an old planning document, or another report branch.
3. Use separate background subagents for the heavy read-only lanes below. Give each the smallest observable outcome and explicit non-goals:
   - **architecture audit:** current registry → build/invalidation → manifest/serve → browser runner path;
   - **dataset and lineage research:** authoritative source fields, tenant-safe joins, row grain, mutation/invalidation paths, null/date semantics;
   - **permission and gate audit:** backend feature gate, dataset/column grants, route/sidebar visibility, saved-definition authorization;
   - **independent specification/plan review:** omissions, alternatives, and requirement-to-plan coverage before implementation; and
   - **validation review:** current diff against the approved contract, focused tests, performance posture, and local browser behavior.
4. Use OpenAI Codex GPT-5.6 Terra for repo-local architecture, lineage, permission, validation, implementation, specification, and planning subagents. Use another model only when the task explicitly requires it. Never treat external research as necessary for facts already authoritative in the repository.
5. Before implementation, obtain the canonical dev-loop specification approval and independent plan sign-off. Before readiness, run actual focused coverage, implementation review, and validation with a different reviewer persona. Run a follow-up Robot Review only when the immutable target or evidence changed materially and the canonical maximum of three total rounds has not been reached. Stop Robot Review when valid feedback is addressed and the target is clean; continue ordinary focused validation as required.

## Reconstruct the current reporting path

Read the relevant current files rather than relying on this summary. Start with:

- `backend/app/reporting/registry.py`, `datasets.py`, `catalog_export.py`, `extracts.py`, `builder.py`, `partitioning.py`, `manifest.py`, `serve.py`, `pruning.py`, `invalidation.py`, `reconcile.py`, `system_reports.py`, and the scaling design/plan;
- `backend/app/api/reporting.py`, the reporting section of `backend/app/schemas.py`, report-definition CRUD/model code, reporting event handlers, and reporting Temporal activities/workflows;
- reporting tests under `backend/tests/reporting`, `backend/tests/api`, `backend/tests/events`, and `backend/tests/temporal`;
- `frontend/src/types/reporting.ts`, `services/reportingApi.ts`, `reporting/compileReport.ts`, `duckdbEngine.ts`, `useReportRunner.ts`, definition/join helpers, exports, Report Builder components, `ReportBuilder.tsx`, `ReportsLanding.tsx`, routes, Sidebar, and their tests; and
- generated OpenAPI/frontend client output when a backend route or schema changes.

Confirm these contracts as they exist on the target base:

- **Dataset registry:** datasets are engineer-governed schema and query definitions. Columns have declared types and roles; dataset versions are extract cache/schema identities.
- **Lineage and invalidation:** every source column read by `build_select` belongs in the source dependency ledger with effects and an explicit dirty strategy. Build-time background paths cannot rely on ambient agency filtering.
- **Parquet path:** extracts are agency-scoped, streamed, typed, bounded, cached/versioned, and may be partitioned into immutable generations described by a manifest.
- **Serve path:** the backend decides accessible datasets and served columns, applies caller scope, evaluates freshness, starts/coalesces builds, and returns Parquet. The browser-authored filter remains authoritative for report semantics; server partition pruning is an I/O optimization and must fail open.
- **Browser path:** Reports pages lazy-load DuckDB-WASM, register Parquet in memory, validate the actual extract schema, compile definitions to SQL, and execute grouping/filtering/pivot/chart interactions locally.
- **Definitions:** system reports are code-shipped and read-only with “Save a copy.” Saved custom reports persist the question/definition, not result rows, and may be private or agency-visible.

## Specification contract

The dev-loop specification must make all of the following explicit before planning:

- the user's question, product location, and why advanced reporting is the right architecture;
- whether the deliverable is a dataset capability, a system preset, a custom-builder capability, or a deliberate combination;
- the primary dataset's row grain and stable identity, plus the grain of every detail and aggregate result;
- source lineage for every new column, including enum-to-label choices and whether values are stored, joined, or derived at serve time;
- inclusion/exclusion rules, soft-delete behavior, agency and clinic scope, business timezone, date boundaries, and null/unknown semantics;
- selected columns, filters, group-bys, measures, pivots, sort, view/chart behavior, export shape, row/category limits, and overflow disclosure;
- the expected editable path: what users can change after opening a system report or saving a copy;
- required dataset relationships and their cardinality/fan-out proof;
- feature flags, dataset permissions, permission-tagged columns/join keys, navigation visibility, direct-route behavior, and saved-definition authorization;
- extract invalidation and rebuild behavior for every source mutation;
- dataset/catalog/system-report version changes and backward behavior for already-saved definitions;
- partition axis and zone-map choices with representative volume/performance assumptions;
- synthetic scenarios needed for tests and browser QA; and
- alternatives considered, including a fixed server report, extending an existing dataset, adding a new dataset, and shipping only a builder capability versus a system preset.

Do not hide a grain mismatch with `DISTINCT`. If the source relationship can fan out, either choose a different primary grain, prove and enforce uniqueness, or model a supported aggregate at the correct layer.

## Dataset, lineage, and extraction rules

### Preserve row-local, tenant-safe data

- Keep every dataset's declared grain stable. A visits dataset change must remain one row per visit unless the specification deliberately creates and names a new dataset.
- Explicitly filter the primary entity by `agency_id` and constrain every agency-owned join in the join clause. Reporting builders run without an `ExecutionContext`; the ordinary ORM filter is defense in depth, not the security boundary.
- Constrain same-agency provenance to the row's own patient/episode graph where needed. A corrupt foreign key must drop or null the enrichment, never surface another patient's value.
- Prefer a unique, row-preserving join for row-local enrichment. Respect the dataset module's structural restrictions on aggregates, subqueries, and cross-row values; do not introduce a correlated hybrid expression when a reviewed one-to-one outer join is the correct shape.
- Define nulls deliberately. A boolean such as “Missed Visit?” should distinguish false from unknown only if the source can actually be unknown. A reason on a non-missed row will normally be null and must not be rendered as a meaningful reason.
- Use Pacific business-date conventions for date semantics. Do not materialize a value based on “today” into a frozen extract; use the existing derived-column/serve-time pattern when it must advance with the clock.

### Keep the registry and cache identities coherent

When adding or changing a dataset field, review all coupled contracts:

- `build_select` projection and `DatasetColumn` name, label, type, role, filterability, defaults, and derivation metadata;
- `unique_keys`, `relationships`, local/target join keys, and cardinality;
- `partition_column`, partition granularity, and `zone_map_columns`;
- every source dependency's OUTPUT/JOIN/FILTER/ROW_EXISTENCE/PARTITION_KEY/CLINIC_SLICE effects and `OWN_ROW`/`EPOCH`/`DEFERRED` choice;
- insert, update, move, soft-delete, and restore invalidation producers, including old and new buckets when a partition key moves;
- the dataset version, because adding a Parquet column changes the schema and cache key even when it is backward-compatible to definitions;
- `CATALOG_VERSION` and the catalog golden hash whenever the rendered catalog changes;
- system-report `dataset_version` pins and joined report `related_versions`; and
- permission-tagged column ledgers/tombstones when a tagged field changes or is removed.

A deploy-time version bump rebuilds old extracts; it does not keep later source edits fresh. Wire the ongoing invalidation path and prove it.

### Joins and permissions

`joined_datasets` is explicit report intent. A supported join must be a declared many-to-one relationship whose target key is unique and non-null in the full pre-slice extract. The compiler uses a left join; unmatched related rows remain primary rows with null related values. Related-dataset filters then exclude those nulls under ordinary SQL semantics.

Keep join keys hidden from ordinary pickers/exports and permission-tagged as the current registry requires. Holding a primary dataset permission does not imply permission to its related dataset. Validate definitions and listing/mutation access against all primary, related, explicit-join, and tagged-column requirements, including drift/tombstones. Related extracts must remain full-scope when the primary extract is windowed, or legitimate joins outside the primary date window silently become null.

## Current access model: audit, do not substitute

At the time this skill was written, advanced reporting uses:

- frontend product flag `advanced-reporting` (`FeatureFlag.ADVANCED_REPORTING`);
- a transitional backend `/reporting/*` gate accepting `advanced-reporting` **or** legacy `report-builder`, with super-user bypass;
- per-dataset permissions `reports:visits`, `reports:episodes`, `reports:patients`, and `reports:claims`; and
- Sidebar visibility requiring `advanced-reporting` plus at least one dataset permission.

This is intentionally different from fixed RogerEMR reports' `roger-emr-reports:access` and RogerDocs' legacy `reports:access`. Do not replace the advanced dataset permissions with either one, auto-grant them to admin roles, invent a new permission, or “simplify” the transitional feature-flag OR without an explicit migration decision.

Re-audit the current target base before every implementation. Backend enforcement is authoritative: verify manifest filtering, extract authorization, served-column projection, system/saved-report listing, create/update/delete, direct routes, Report Builder, Reports landing, and Sidebar together. Use validated RBAC grants, not untrusted permission strings carried in request context.

## System reports and custom reports

For a system report:

- add a stable key, plain name/description, primary dataset, and complete `ReportDefinition` in `system_reports.py`;
- pin the current dataset version and, when joined, the current related versions/schema shape;
- ensure every referenced field is available through the caller-specific manifest and permissions;
- keep the preset read-only and verify “Save a copy” produces a valid editable custom definition;
- decide whether the preset should open as detail, grouped table, pivot, or chart; and
- test semantic results, not only registry presence.

For custom/self-serve capability, verify FieldPanel/filter/sort/editor affordances, definition validation and drift repair, compiler support, DuckDB result shape, exports, save/update/copy/visibility behavior, and landing-page discoverability. Do not hand-write SQL in UI components; route all report SQL through the validated compiler.

## Compiler, WASM, and serving gates

- Keep backend definition validation and frontend compiler compatibility in sync for operators, transforms, measures, schema versions, and joined references.
- Test compiled behavior against real DuckDB where the repository already does so. Prefer result assertions over fragile SQL-string mirrors, while retaining exact SQL assertions only for security or ordering contracts that require them.
- Preserve SQL identifier/literal escaping and type compatibility. Mid-edit or stale definitions must degrade visibly or become actionable invalid states, never execute a broader/different report silently.
- Preserve row-grain branches: detail rows, scalar aggregates, grouped aggregates, and pivots must not fall through into one another when fields drift.
- Respect current limits and disclosures: extract row cap, detail result/export cap, pivot cardinality cap, chart category cap, and date-window behavior.
- Keep DuckDB-WASM lazy-loaded and browser-only, with no persistent PHI cache. Clean up failed workers, cancellation/polling, file registrations, and in-memory scope caches according to existing patterns.
- Scope/ETag/cache identities must include every representation-changing input. A manual refresh must rebuild/re-register the intended dataset(s), while concurrent loads must not swap a narrower view under a wider query.
- If backend reporting routes or schemas change, regenerate the frontend API client/types with the repo command and review the generated diff. Keep any intentional reporting wrapper synchronized rather than replacing generated contracts ad hoc.

## Partitioning, performance, and bounded retention

Choose the physical partition axis from how rows are built and commonly filtered, not from the report title. Preserve immutable monthly bucket semantics, Pacific conversion, and old/new bucket invalidation. Add zone maps only for useful, correctly typed semantic filter axes; a stale or ambiguous zone map must fail open so it can over-serve but never drop matching rows.

Measure changes that affect extract size, build time, browser download, registration memory, join latency, or query latency at representative and near-cap volumes. Use the repository's browser DuckDB benchmark path when join/compiler performance is material. A fast unit test is not evidence that a multi-million-row browser path meets its budget.

Any new ledger, history, cache, manifest generation, or stored result must ship with bounded retention. Preserve and test the reporting engine's orphan-generation garbage collection/grace protections when adding new immutable objects; define an owner, cadence, age/size bound, failure direction, and focused retention test for any new accumulating store. Never delete an object still referenced by a manifest or an in-flight reader.

## EMR-440: Episode Missed Visit Count example

EMR-440 is the reference shape for this skill, not a fixed-report template. The likely architecture is:

1. Extend the **visits** dataset while preserving one row per visit.
2. Source “Missed Visit?” and “Missed Visit Reason” from the active, agency-matched `missed_visit_data` row. A row-preserving outer join is likely safer than `Visit.is_missed`'s correlated `EXISTS`, because the dataset's clinic-purity checks prohibit arbitrary subqueries and the source table has a unique `visit_id`. Explicitly constrain `agency_id` and `deleted_at`; an unmarked/soft-deleted record must yield false/null.
3. Add every `missed_visit_data` source column/join/filter/row-existence dependency and wire create, update, approval/reason change, soft-delete/unmark, and restore invalidation to the visit's correct partition.
4. Keep reason values and user-facing labels consistent with the canonical missed-visit enum/label source. Do not guess from task status or `billable_status`; the active missed-visit row is the source of truth.
5. Support visit-detail filtering (`Missed Visit? = true`, optionally by reason) and episode-level count aggregation. Verify how the builder exposes a human-meaningful episode dimension: hidden join IDs are not picker fields, so use a declared relationship and suitable episode fields, or add an approved visible episode identifier/label without exposing a raw internal key as UI.
6. Decide with product whether one grouped system preset is enough, whether a detail preset is also needed, or whether the grouped preset plus “Save a copy” adequately exposes both detail and aggregation. Verify the actual `/reports` location and navigation instead of assuming “OLAP playground” means a separate page. The supplied product context favors known presets first, with the standalone playground/self-serve experience as a broader capability.

Seed at least two episodes and multiple visits: missed and not missed, two reasons, a null reason on non-missed rows, an unmarked/soft-deleted missed record, date boundaries, and another agency/clinic. Prove detail filters return the expected visits and grouped counts equal the underlying missed rows without fan-out.

## Implementation and validation checklist

Select the relevant checks based on the approved plan; do not add superficial tests just to touch every module.

### Backend

- dataset select and row-grain/unique-key behavior, including cross-agency and corrupt-provenance negatives;
- source dependency ledger fidelity and all required invalidation event producers;
- registry invariants, permission-tagged columns/join keys, relationships, and version rolls;
- catalog shape, version, serialization, and golden hash;
- whole/partition extract schema and values, conversion/null/date behavior, row caps, and rebuild invalidation;
- builder, manifest publication/freshness, pruning/zone maps, serve scoping/column projection, ETag/cache identity, and build failure behavior when touched;
- system report definition validity, version pins, permissions, semantic result shape, and Save-a-copy round trip;
- joined definition refs, `joined_datasets`, schema/related versions, drift, create/update/list visibility, agency isolation, and owner/private/shared behavior; and
- Temporal build/reconcile/garbage-collection paths when changed.

### Frontend

- manifest/types and generated client synchronization;
- definition updates, explicit joins, filter/operator compatibility, group-by/measure/pivot behavior, null/date semantics, sorting, and exports;
- compiler execution against real DuckDB for the new detail and aggregate cases;
- runner extract requests, primary-only windowing, related full scope, schema drift, refresh, cancellation, and user-safe errors;
- Report Builder controls/results and system-report open/Save-a-copy behavior;
- Reports landing, `/reports/builder` route, Sidebar feature/permission gates, direct flag-off access, and no unwanted fetch while dark; and
- WASM asset lazy loading and no Vite import-analysis overlay after dependency changes.

### Local demo

Use the repo's isolated worktree/bootstrap flow and representative synthetic data. Preserve other worktrees, runtimes, databases, and ports. Freshly prove Vite/API/database ownership, migrations, Temporal/reporting workers where required, and HTTP readiness.

Use Browser Console Bridge to exercise the real `/reports` path: open the preset, inspect detail and aggregate results, change filters/grouping, save a copy, reopen it, export the visible result, refresh data, and verify permission/feature-denied behavior. Check console errors and Vite overlays. Compare counts to the seeded source facts; a rendered table and HTTP 200 alone are not correctness evidence.

## Completion gate

Do not request git authorization until an independent validation pass confirms:

- the advanced architecture choice and product location match the approved specification;
- source lineage, agency/clinic isolation, row grain, join cardinality, null/date semantics, and aggregate counts are correct;
- every source mutation has a live invalidation/rebuild path;
- dataset/catalog/system-report versions and saved-definition drift behavior are coherent;
- exact current feature flags and dataset permissions are enforced across backend, routes, navigation, and saved definitions;
- compiler, DuckDB-WASM, serve/extract, and builder behavior agree;
- performance, partitioning, cache identity, and bounded-retention obligations are satisfied;
- focused backend/frontend tests, meaningful coverage, and the synthetic browser walkthrough pass; and
- the canonical Robot Review cap is followed: stop when valid feedback is addressed and the current diff is clean; if valid Blocking or Should Address findings remain after round 3, report them as unresolved instead of calling the diff clean.

Map every changed file to the approved specification or a validated review finding and remove unrelated changes. Commit authorization is not push authorization; do not commit, push, change PR state/base, merge, enqueue, or tear down the review environment without the corresponding explicit authorization.
