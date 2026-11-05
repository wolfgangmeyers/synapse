# Spec: Launch model picker visibility

- **ID**: `SPEC-LAUNCH-MODEL-VISIBILITY-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/launch-model-discovery.spec.ts`

## Intent & User Outcome

Users can read, find, and select a model in the launch dialog, even when the catalog is long or the window is short.

## Core Invariants

1. Model names and identifiers have visible, nonzero-height text; options remain individually clickable.
2. The picker stays within the visible launch dialog and viewport, with a scrollable list when the catalog exceeds available space.
3. The picker tracks the trigger when the dialog scrolls, accounting for dialog borders and both scroll offsets. Its horizontal and vertical bounds stay inside the visible dialog interior and viewport. If neither side has useful room, it may overlap the trigger to preserve usable search and options.
4. Searching filters the catalog without preventing selection; an empty result is reported. An above-placed picker remains anchored to the trigger as its contents shrink.
5. If scrolling moves the trigger fully outside the visible dialog interior or viewport, the picker closes rather than appearing outside those bounds.

## User Scenarios

### Scenario: Read and select from a long catalog
- **Given** the launch dialog and a catalog longer than the available picker space.
- **When** the user opens the model picker and scrolls to a model.
- **Then** its name and identifier are visible within the dialog, and clicking it selects the model.

### Scenario: Scrolled and narrow launch dialog
- **Given** a launch dialog scrolled away from its origin and narrower than the picker’s preferred width.
- **When** the user opens the picker or scrolls the dialog while it is open.
- **Then** the picker stays within the visible inner dialog and viewport, its labels remain readable, and a model can be selected. If space above and below is insufficient, the picker overlaps the trigger rather than collapsing.

### Scenario: Trigger scrolls out of view
- **Given** the picker is open and tracking its trigger.
- **When** scrolling moves the trigger fully outside the visible dialog interior or viewport.
- **Then** the picker closes without scrolling the trigger back into view.

### Scenario: Filter an above-placed picker
- **Given** the picker opens above its trigger.
- **When** a search reduces the number of results.
- **Then** the bottom of the picker remains adjacent to the trigger and within the visible bounds.

### Scenario: Search and no results in a short window
- **Given** the launch dialog is open in a short window.
- **When** the user searches for a model and then for a nonexistent name.
- **Then** matching models are shown first, followed by a visible no-results message for the nonexistent name; the picker remains inside the dialog.

## Test Mapping

`launch-model-discovery.spec.ts` exercises the real launch dialog, browser geometry, scrolling, selection, and search through the router and proxy with a deterministic local provider fixture.
