# Spec: Scratchpad Entry Truncation Tool & Storage

- **ID**: `SPEC-SCRATCHPAD-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/scratchpad-truncation.spec.ts`

---

## 1. Intent & User Outcome

Long-lived managed agents accumulate thousands of scratchpad entries across many sessions, turns, and context resets. This unconstrained growth causes database row accumulation, slows pagination and note search, and makes cleanup difficult without issuing dozens of manual itemized `delete` calls.

This feature adds a safe, deterministic `truncate` action to the embedded `scratchpad` tool and a corresponding router REST API endpoint. Agents and supervisor automations can prune older unpinned notes in a single operation either before a given entry ID or by retaining only the $N$ most recent unpinned entries. Critical decisions, milestone notes, and pinned items (`pinned = 1`) are strictly preserved and never deleted by truncation under any condition.

---

## 2. Core Invariants

1. **Pinned Preservation**: Entries marked as pinned (`pinned = 1`) MUST NEVER be deleted by a truncation operation under any circumstance, regardless of creation timestamp or `keep_last` threshold.
2. **Mutual Exclusivity**: Exactly one threshold parameter must be provided: either `before_id` (non-empty string) OR `keep_last` (integer > 0). Supplying both, neither, or a non-positive `keep_last` MUST fail with an invalid parameter error before any database mutation.
3. **Threshold Entry Safety**: When truncating by `before_id`, the entry referenced by `before_id` itself MUST NOT be deleted (whether it is pinned or unpinned). Only unpinned entries created strictly prior to `before_id`'s creation timestamp are deleted.
4. **Agent Isolation**: Truncation operations are strictly isolated to the calling agent's scratchpad. No cross-agent entry targeting is permitted in the tool schema (`target_agent_id` is omitted). Supplying a `before_id` that belongs to another agent or does not exist MUST fail with a not found error without deleting any rows.
5. **Exact Reporting**: Truncation operations MUST return the exact count of unpinned rows deleted.
6. **Atomic KeepLast Operation**: When truncating by `keep_last = N`, pruning must be performed atomically in a single query retaining the $N$ newest unpinned entries and deleting all older unpinned entries. If fewer than or equal to $N$ unpinned entries exist, zero entries are deleted.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-SP-001] Truncation by before_id deletes older unpinned entries and preserves pinned
- **User Goal**: Prune older scratchpad entries up to a specific milestone entry while preserving pinned notes and the milestone itself.
- **Given**:
  - Agent has 10 unpinned scratchpad entries (E1 through E10) and 2 pinned entries (P1, P2) created in chronological order (E1..E5, P1, E6..E8, P2, E9..E10).
- **When**:
  - The agent or caller requests scratchpad truncation with `action: "truncate"` and `before_id: E6.id`.
- **Then**:
  - Entries E1 through E5 (unpinned, created before E6) are deleted.
  - Entry E6 itself remains intact in the scratchpad.
  - Subsequent unpinned entries E7 through E10 remain intact.
  - Both pinned entries P1 and P2 remain intact (P1 was created before E6, P2 after E6).
  - The operation reports that 5 entries were deleted.

### Scenario: [SCN-SP-002] Truncation by keep_last retains N newest unpinned entries and all pinned entries
- **User Goal**: Bound scratchpad size by keeping only the most recent unpinned notes.
- **Given**:
  - Agent has 50 unpinned scratchpad entries and 4 pinned entries interspersed throughout history.
- **When**:
  - The agent or caller requests scratchpad truncation with `action: "truncate"` and `keep_last: 15`.
- **Then**:
  - The 35 oldest unpinned entries are deleted.
  - The 15 most recent unpinned entries are preserved.
  - All 4 pinned entries are preserved intact.
  - The operation reports that 35 entries were deleted.

### Scenario: [SCN-SP-003] Pinned entries are never deleted regardless of cutoff or limit
- **User Goal**: Guarantee that architectural decisions, secrets, or milestone markers flagged as pinned are never pruned by truncation.
- **Given**:
  - Agent has 5 pinned entries, including entries created at the very beginning of the agent's lifetime.
  - Agent has 20 unpinned entries.
- **When**:
  - Truncation is called with `keep_last: 1` or `before_id` pointing to the newest entry.
- **Then**:
  - All 5 pinned entries remain in the scratchpad.
  - Only unpinned entries meeting the threshold are deleted.

### Scenario: [SCN-SP-004] Mutual exclusivity validation rejects invalid parameter combinations
- **User Goal**: Receive clear feedback when arguments are missing or conflicting without partial deletes.
- **Given**:
  - An agent has multiple scratchpad entries.
- **When**:
  - Truncation is attempted with:
    - Case A: Both `before_id` and `keep_last` specified.
    - Case B: Neither `before_id` nor `keep_last` specified.
    - Case C: `keep_last` specified with value `0` or a negative integer.
- **Then**:
  - The request is rejected with a validation error (`ErrInvalidParameter`).
  - Zero scratchpad entries are modified or deleted.

### Scenario: [SCN-SP-005] Missing or foreign before_id entry returns error without deleting
- **User Goal**: Ensure invalid or cross-agent IDs fail cleanly without corrupting scratchpad state.
- **Given**:
  - Agent `worker-alpha` has scratchpad entries.
  - Agent `worker-bravo` has scratchpad entry `bravo-entry-1`.
- **When**:
  - Agent `worker-alpha` requests truncation with `before_id: "non-existent-id"`, OR
  - Agent `worker-alpha` requests truncation with `before_id: "bravo-entry-1"`.
- **Then**:
  - The operation fails with an entry not found error.
  - Zero scratchpad entries are deleted for `worker-alpha` or `worker-bravo`.

### Scenario: [SCN-SP-006] Cross-agent isolation guarantees independent scratchpads
- **User Goal**: Ensure truncation for one agent does not affect any other agent's scratchpad notes.
- **Given**:
  - Agent A has 10 unpinned entries.
  - Agent B has 10 unpinned entries.
- **When**:
  - Truncation is executed for Agent A with `keep_last: 3`.
- **Then**:
  - Agent A has 7 unpinned entries deleted (3 remaining).
  - Agent B still has all 10 unpinned entries intact and untouched.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E / Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-SP-001` | Truncate by `before_id` prunes older unpinned | `tests/e2e/scratchpad-truncation.spec.ts` | `truncates unpinned entries before target id preserving pinned and target` |
| `SCN-SP-002` | Truncate by `keep_last` retains N newest unpinned | `tests/e2e/scratchpad-truncation.spec.ts` | `truncates unpinned entries to keep_last limit` |
| `SCN-SP-003` | Pinned entries preserved unconditionally | `tests/e2e/scratchpad-truncation.spec.ts` | `preserves all pinned entries during deep truncation` |
| `SCN-SP-004` | Mutual exclusivity validation | `tests/e2e/scratchpad-truncation.spec.ts` | `rejects invalid truncation parameters` |
| `SCN-SP-005` | Missing or foreign before_id error | `tests/e2e/scratchpad-truncation.spec.ts` | `returns error when before_id is missing or foreign` |
| `SCN-SP-006` | Cross-agent scratchpad isolation | `tests/e2e/scratchpad-truncation.spec.ts` | `isolates scratchpad truncation between agents` |
