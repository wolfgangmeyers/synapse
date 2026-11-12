# Spec: Workflow background tool catalog

- **ID**: `SPEC-WORKFLOW-BACKGROUND-CATALOG-001`
- **Status**: Draft
- **Domain**: Workflows
- **Target E2E Suite**: `tests/e2e/workflow-background-tool-catalog.spec.ts`

## Intent & User Outcome

Users can select the available background bash controls in restricted workflow states, even when the UI must use its local catalog fallback.

## Core Invariants

1. The catalog and workflow validation recognize launch_background_bash, list_background_bash_jobs, get_background_bash_job, and kill_background_bash_job as distinct system tools.
2. wait_background_bash_job is not offered or accepted as a workflow tool. Other unknown tools remain invalid.
3. Workflow restrictions continue to admit only selected tools and existing mandatory exceptions.

## User Scenarios

### Scenario: Select and persist background controls
- **Given** a workflow and its tool catalog.
- **When** a user creates a restricted state selecting the four background controls.
- **Then** the server accepts and returns those selections, including after retrieval, and the tools are available to a worker in that state.

### Scenario: Catalog unavailable
- **Given** the workflow editor cannot fetch its tool catalog.
- **When** the user edits a restricted state.
- **Then** the fallback offers the same four background controls, but not wait_background_bash_job.

### Scenario: Invalid selection
- **Given** a workflow state request contains wait_background_bash_job or an unknown tool.
- **When** the user saves it.
- **Then** the server rejects the request rather than silently accepting the invalid selection.

## Verification

The target E2E suite exercises the router, proxy, SQLite, and browser with a deterministic local model; no external model is called. `restricted workflows expose and persist background controls including catalog fallback` passed in a focused run. This is focused evidence, not a claim of complete scenario acceptance.
