# Spec: Web UI Typography — Oxanium

- **ID**: `SPEC-UI-TYPE-001`
- **Status**: Active
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/ui-typography-oxanium.spec.ts`

## 1. Intent & User Outcome

The Web UI uses locally hosted Oxanium for interface text, providing its intended visual identity without a runtime font-service dependency. Code and technical values remain monospaced, and ordinary interface copy remains readable at the existing sizes and contrast.

## 2. Core Invariants

1. UI typography loads Oxanium from a same-origin bundled font asset; no external font service is required.
2. Code and preformatted technical content retain a monospace font.
3. The interface retains its existing text sizes, line heights, and colors unless specifically needed for legibility.
4. If the font asset cannot load, a system sans-serif fallback remains usable.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [TYPE-001] Oxanium is used by the interface
- **User Goal**: See the Web UI's interface in Oxanium.
- **Given**: The user opens the Web UI with network access to its own origin.
- **When**: The application is rendered and its fonts are ready.
- **Then**: The body uses Oxanium and the font is loaded from the UI origin.
- **And**: A preformatted code sample uses a monospace family.

### Scenario: [TYPE-002] Font asset is unavailable
- **User Goal**: Continue using the interface if the bundled font cannot load.
- **Given**: The UI font request fails.
- **When**: The application renders text.
- **Then**: Interface text falls back to a system sans-serif font and remains visible.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `TYPE-001` | Local font and monospace code | `tests/e2e/ui-typography-oxanium.spec.ts` | `uses the local Oxanium face while preserving monospace code` |
| `TYPE-002` | Font load failure fallback | `tests/e2e/ui-typography-oxanium.spec.ts` | `falls back to system sans-serif when the font request fails` |

This visual-only contract is verified at the browser/CSS asset boundary; it does not alter router, WebSocket, or SQLite behavior.
