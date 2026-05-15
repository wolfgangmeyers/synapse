---
name: decompose
description: Break large tasks into phased dev-loop iterations with clear boundaries, ordering, and inter-phase contracts.
---

# Decompose

Break a large task into multiple sequential dev-loop iterations (phases), each independently shippable with its own spec, plan, implementation, and review.

## Usage

```
/decompose [task description or ticket reference]
```

Takes a large task and produces a phased breakdown where each phase is a single dev-loop iteration.

---

## When to Decompose

Decompose when:
- The task touches 3+ distinct areas of the codebase
- The task has natural intermediate milestones that provide value independently
- A single PR would be too large to review effectively (300+ lines, 10+ files)
- The task has dependencies that benefit from sequential delivery (schema first, then API, then UI)

Do NOT decompose when:
- The task is a single logical change that can't be split without creating incomplete states
- Splitting would require temporary scaffolding that gets removed in the next phase
- The total work fits comfortably in one dev-loop iteration

---

## Phase Boundary Identification

A good phase boundary has these properties:

1. **Independently shippable**: The codebase is in a valid, deployable state after the phase merges. No broken functionality, no dangling references, no required follow-ups to restore correctness.

2. **Testable in isolation**: Each phase's changes can be verified without depending on later phases. Tests pass, behavior is correct for the current scope.

3. **Clear input/output contract**: The phase produces artifacts (schema, API, types, interfaces) that later phases consume. The contract between phases is explicit, not implicit.

4. **Minimal cross-phase coupling**: Changes in a later phase should not require modifying code shipped in an earlier phase. If they do, the boundary is in the wrong place.

### Boundary Heuristics

| Pattern | Natural boundary |
|---|---|
| Schema + API + UI | Phase 1: schema/migration, Phase 2: API/business logic, Phase 3: UI/frontend |
| New capability | Phase 1: core types + interfaces, Phase 2: implementation, Phase 3: integration |
| Refactor + feature | Phase 1: refactor (behavior-preserving), Phase 2: new feature on clean foundation |
| Migration | Phase 1: add new path (dual-write), Phase 2: migrate data, Phase 3: remove old path |
| Infrastructure + application | Phase 1: infrastructure/config, Phase 2: application code using it |

---

## Phase Document Structure

For each phase, produce a section with:

```markdown
### Phase N: [Short descriptive name]

**Goal**: One sentence — what this phase delivers.

**Depends on**: Phase(s) that must be complete first, or "None" for Phase 1.

**Produces**: Artifacts, interfaces, or capabilities that later phases consume.

**Scope**:
- Included: [specific items in this phase]
- Excluded: [items explicitly deferred to later phases]

**Acceptance criteria**: How we know this phase is done (maps to dev-loop SPEC.md criteria).

**Estimated complexity**: Simple | Moderate | Complex (guides dev-loop spec-vs-skip decision)
```

---

## Ordering Rules

1. **Dependencies flow forward**: Phase N may depend on Phases 1 through N-1, never on Phase N+1. If you find a circular dependency, the boundaries are wrong — re-cut.

2. **Risk-first**: Place the highest-risk or most uncertain phase earliest. If Phase 3 might invalidate Phase 2's approach, Phase 3's exploration should come first.

3. **Schema before logic**: Database migrations and type definitions come before the code that uses them. The data model is the foundation.

4. **Infrastructure before application**: Config, permissions, external service setup, and deployment changes come before the application code that depends on them.

5. **Refactor before feature**: If a feature requires restructuring existing code, the refactor ships first as a behavior-preserving change. The feature builds on the clean structure.

6. **Smallest viable first**: When ordering is flexible, prefer the smallest phase first. Early phases build confidence and establish patterns for later ones.

---

## How Phases Feed into Dev-Loop

Each phase is a complete dev-loop iteration:

1. **Phase document → SPEC.md**: The phase's goal, scope, acceptance criteria, and constraints become the SPEC.md for that dev-loop iteration. For simple phases (estimated "Simple"), skip the spec and write requirements directly in PLAN.md per dev-loop Phase 1 skip guidance.

2. **"Produces" → Constraints for later phases**: What this phase produces becomes a constraint or dependency for the next phase's spec. The next phase's SPEC.md should reference what it consumes from the previous phase.

3. **"Depends on" → Pre-conditions**: Before starting a phase, verify its dependencies are actually shipped and working — not just planned. The supervisor confirms the previous phase's PR is merged before launching the next.

4. **Completion → Next phase launch**: When a phase's dev-loop completes (implementation reviewed, PR merged), the supervisor launches the next phase as a new worker task.

---

## Output Format

When invoked, produce:

```markdown
# Task Decomposition: [Task Name]

## Overview
[1-2 sentences: what the full task achieves and why it's being decomposed]

## Phases

### Phase 1: [Name]
[Phase document structure as above]

### Phase 2: [Name]
[Phase document structure as above]

...

## Phase Dependency Graph
[Simple text showing: Phase 1 → Phase 2 → Phase 3, or branching if phases are independent]

## Notes
[Any cross-cutting concerns, risks, or decisions that affect multiple phases]
```

---

## Anti-Patterns

- **Phases that can't merge independently**: If Phase 1 leaves the build broken and Phase 2 fixes it, that's one phase, not two.
- **Phases split by file rather than by concern**: "Phase 1: models, Phase 2: handlers" is wrong if both are needed for a single feature to work. Split by capability, not by layer.
- **Too many phases**: If you have 6+ phases, re-scope the task or merge phases that share a concern. Each phase has overhead (spec, plan, review); the overhead must be justified by the value of an independent delivery checkpoint.
- **Phases without acceptance criteria**: Every phase must be independently verifiable. If you can't write acceptance criteria, the phase isn't well-defined.
- **Backward dependencies**: A later phase requiring changes to an earlier phase's code means the boundary is wrong. Re-cut.
