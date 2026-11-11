# Spec: Sidebar Synapse Branding

- **ID**: `SPEC-UI-BRAND-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/sidebar-brand.spec.ts`

## 1. Intent & User Outcome

The sidebar displays the supplied transparent Synapse wordmark instead of the old sigil, Mecha-Developer heading, and tagline. This changes only sidebar branding, not application names or icons elsewhere.

## 2. Core Invariants

1. The logo is the original, unmodified 2172×724 RGBA PNG served at `/synapse-logo.png` with accessible name `Synapse`.
2. The entire wordmark fits the sidebar content width at its original 3:1 aspect ratio, without cropping, added filters, shadows, or backgrounds.
3. The old header sigil, heading, and tagline are absent. Existing sidebar controls remain available.
4. No data is accumulated; retention does not apply.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [BRAND-001] Desktop sidebar branding
- **User Goal**: Recognize Synapse in the sidebar.
- **Given**: The application is connected to a router on a desktop viewport.
- **When**: The user opens the application.
- **Then**: One accessible Synapse image appears in the sidebar, using the original served PNG with transparency intact, fitting the sidebar at 3:1 without visual treatments. The old header branding is absent and router controls remain visible.

### Scenario: [BRAND-002] Narrow viewport
- **User Goal**: See the complete wordmark on a small screen.
- **Given**: The application is connected to a router on a 375px-wide viewport.
- **When**: The user opens the application.
- **Then**: The same original transparent logo remains visible, fits within the sidebar and viewport without cropping or distortion, and does not restore the old branding.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `BRAND-001` | Desktop sidebar branding | `tests/e2e/sidebar-brand.spec.ts` | `desktop: original transparent wordmark fits sidebar` |
| `BRAND-002` | Narrow viewport | `tests/e2e/sidebar-brand.spec.ts` | `narrow: original transparent wordmark fits sidebar` |
