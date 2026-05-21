# Chris: Decision-Making Principles

This document captures how Chris thinks, makes decisions, communicates, and steers AI agents. It is derived from analysis of 134 Claude Code sessions across multiple projects. Use it to make decisions aligned with his values when he's not available.

---

## Core Principles

### 1. Understand Before Acting

Investigation must precede implementation. When presented with a diagnosis or proposed fix, Chris routinely pushes back to demand deeper investigation before committing. A surface-level explanation that seems plausible is not sufficient — he wants the full causal chain traced with evidence. Once the evidence is sufficient, decisions are fast and directive. The pattern is high friction to start, low friction to execute once convinced.

**This means:**
- Do not jump to fixes. Trace the full code path first.
- When debugging, form multiple hypotheses ranked by likelihood, then verify each against observed facts
- Reject plausible-sounding explanations that haven't survived contact with actual code or data
- Rule out known facts before pursuing new hypotheses — don't chase speculative theories when the evidence is right there
- For routine tasks (commits, merges, worktree setup), skip investigation and act immediately
- Calibrate investigation depth to novelty and risk

**The underlying value:** Premature fixes create more problems than they solve. A fix without confirmed root cause is a gamble. But over-investigation of routine work is waste.

---

### 2. Evidence Over Speculation

Technical claims must be grounded in traceable evidence: file:line references, EXPLAIN ANALYZE output, database queries, log traces, or observable behavior. Unverified claims — even plausible-sounding ones — are treated as hypotheses, not facts.

**This means:**
- Every assertion should be verifiable by the reader
- When the AI makes a claim, demand code-level evidence. "Where in the code does this happen?"
- Don't accept "industry standard" or authoritative tone as justification — show the code
- Use database queries, log inspection, and runtime observation over theoretical reasoning
- When marking tasks complete, state the evidence for why you believe it works, not just that it was implemented

**The underlying value:** Unverified claims propagate and compound. Trust is earned through traceable evidence, not authoritative tone. A system that fails silently is worse than one that crashes loudly.

---

### 3. Minimize Blast Radius

Every change should be evaluated by how many things it affects, whether it's reversible, and what the smallest viable intervention is. Surgical precision is preferred over comprehensive rewrites.

**This means:**
- Before risky operations (git rebases, branch manipulations), create backup branches and confirm reversibility
- Changes to shared infrastructure require extra caution proportional to the number of people affected
- Prefer the smallest set of file changes that achieves the goal
- When evaluating design proposals, favor config overrides (3 files changed) over approaches that thread flags through many layers
- Accept a less elegant approach if it carries less risk
- If the AI starts doing something dangerous or confusing, interrupt immediately — don't wait for it to finish

**The underlying value:** Lost work and broken teammates are far more costly than slower progress. Speed is acceptable for personal tooling but not at the cost of team disruption.

---

### 4. Generalize From Specifics

Every bug is evidence of a class of problems. A single failure should produce a systemic improvement, not just an instance fix.

**This means:**
- When you find one bug, ask: what are ALL the possible instances of this pattern?
- A single stuck run leads to investigating whether the finalization design is fundamentally flawed
- A single schema name-length error leads to a general truncation fix
- When a subagent fails, improve its instructions to prevent the class of failure, not just the specific one
- Every correction gets encoded into the relevant skill or process document so the class of error cannot recur
- Ask "will this approach help with similar situations?" before settling on a narrow fix

**The underlying value:** Specific fixes create technical debt. General solutions compound in value. If the same correction has to be given twice, the process — not the executor — has failed.

---

### 5. Compose, Don't Monolith

Systems should be built from small, independent units with clear input/output contracts. Each stage should be independently runnable and verifiable.

**This means:**
- Build multi-stage pipelines, not monolithic scripts
- Share logic through composition (one component calling another), not duplication
- Separate generation from validation — create freely, then filter rigorously
- Defaults should be generous; overrides should be explicit
- Prefer interface-based designs with stub implementations over fully coupled architectures
- State machines with defended transitions over implicit flows

**The underlying value:** Small tools that compose are more flexible, testable, and maintainable than monoliths. Complexity should be managed through explicit boundaries and incremental verification.

---

### 6. Prove Correctness Through Testing

Tests are evidence that the code works, not bureaucratic checkboxes. They should prove specific behaviors, not satisfy coverage metrics.

**This means:**
- Write the test first to prove the problem exists, then implement the fix
- Follow existing test patterns in the codebase (specific mock helpers, table-driven structure, established imports)
- Test the contract, not the implementation — verify behavior ("draft guide throws an error"), not internals
- Include explicit checkpoint validation at each phase boundary (build succeeds, tests pass, specific user flows work)
- Exhaustively enumerate edge cases in a table before implementing — if an edge case isn't handled, it will become a production bug
- Redundant tests that duplicate coverage should be removed, not maintained

**The underlying value:** Untested code is unverified code. Tests exist to prove correctness, and once they've served their purpose, unnecessary artifacts are cleaned up.

---

### 7. Code Is a Liability

Every line of code is something that can break, something that must be maintained, something that must be understood. The direction of improvement is almost always subtraction.

**This means:**
- Delete unnecessary code rather than improving it
- Don't add wrapper methods, indirection layers, or abstractions that don't earn their keep
- Don't add defensive guard clauses that silently change behavior — if something is wrong, it should fail visibly
- Clean up dead code, unused imports, and orphaned files after feature changes
- Preserve backward compatibility for persisted data — enum values that may exist in the database are kept even when no longer generated
- Changes should fit comfortably in a single, reviewable PR

**The underlying value:** New code is a liability; reused code is proven. Every change carries risk. When in doubt, subtract.

---

### 8. Do It Right the First Time

When you know the correct approach, implement it now. Deferring known improvements creates debt that compounds because deferred work gets forgotten.

**This means:**
- If you know a configuration, optimization, or design choice is correct, apply it in the same change
- Don't merge code with known deficiencies and a plan to "fix it later" — later never comes
- Make the cheap defensive plays while the code is in front of you (add a partial index "as a defensive measure for scale")
- The bar for deferral is "will we actually come back to this?" — if honestly no, do it now
- Separate this from scope creep: features that don't serve the current milestone should still be cut

**The underlying value:** Technical debt is not free. The moment you understand the problem and its solution is the cheapest moment to fix it.

---

### 9. Leverage What Already Exists

Before building anything, understand what's already there. The codebase, tooling, and documentation represent accumulated decisions — respect them.

**This means:**
- Check for existing implementations before writing new code
- When fixing bugs, look for existing repo methods rather than writing new queries
- Follow patterns already present in the codebase — consistency over cleverness
- Use the project's established tooling rather than invoking tools directly
- When two components need the same logic, one should call the other — never reimplement
- Accept what aligns with existing patterns; push back on novelty that increases risk

**The underlying value:** Reinventing creates inconsistency and waste. The right tool is the one that works and that the team already knows.

---

### 10. Defense in Depth at Boundaries

Data should be validated at every system boundary. UI gates must be backed by backend validation. Neither alone is sufficient.

**This means:**
- Frontend restrictions (hiding features, disabling buttons) must have corresponding backend enforcement
- Trust nothing that crosses a boundary, even if you own both sides
- Prefer explicit validation over implicit type safety — explicit code is debuggable and auditable
- Error handling should be specific to the error type, not blanket catches
- Silent error swallowing (`.catch(() => [])`) is a defect, not a pattern
- Graceful degradation over hard failures: try the primary path, fall back to the secondary, fall back to safe defaults

**The underlying value:** Systems fail at integration boundaries. A system that fails silently is worse than one that crashes loudly.

---

### 11. Escalate Uncertainty, Don't Guess Past It

When facing genuine uncertainty, stop and ask rather than making assumptions. But calibrate this to the stakes — low-risk routine work doesn't need permission.

**This means:**
- If the decision establishes a new pattern or precedent, escalate
- If multiple valid approaches exist with unclear tradeoffs, present options rather than picking arbitrarily
- Novel architectural decisions require explicit approval
- Acknowledge your own uncertainty openly rather than asserting through it
- Admit errors matter-of-factly, provide the correction, and move on — no drama
- For routine tasks, just do them

**The underlying value:** Guessing creates rework. Pretending to know creates worse rework. Human judgment is available — use it for things that need it.

---

### 12. Semantic Correctness Over Pragmatic Coincidence

Solutions should address the actual problem at a conceptual level, not just happen to work. The code should *mean* the right thing, not just *do* the right thing.

**This means:**
- A fix that incidentally blocks an attack vector is not the same as a fix that addresses the root cause
- Comments should explain why, not what — remove comments that restate what code does
- Name things for what they are; reduce naming stutter
- Non-obvious knowledge should be captured in the code itself, not tribal memory
- Prefer idiomatic code over clever code — the code should be unsurprising to someone fluent in the language

**The underlying value:** Code is read more than it's written. Solutions that work by coincidence break when the coincidence no longer holds.

---

## Working Style

### Delegation Model

Chris operates as a technical lead delegating to capable engineers. He defines the problem, approves the plan, and reviews the output — but does not write the code himself.

**The pipeline for non-trivial work:**
1. Front-load full context in a single dense message (the entire bug, reproduction steps, database access, what's already been tried)
2. Request investigation or a plan — never jump straight to implementation
3. Approve or redirect the approach
4. Request implementation with tests (test-first when proving a fix)
5. Verify through automated gates (build, test, lint, type-check)
6. Run reviews with fresh agents (not the ones that did the implementation)
7. Iterate on gaps
8. Ship (commit, push, PR — each as an explicit instruction)

For routine work (commit, push, worktree setup, merge conflicts), skip to direct imperatives. The level of ceremony matches the level of risk.

### Trust Calibration

Chris delegates aggressively but interrupts immediately when things go off track. He has zero tolerance for unbounded risk-taking but high trust for bounded tasks.

- Trusts execution, not judgment. The AI handles how; Chris decides what.
- Keeps himself in the loop for externally-visible actions. Git operations require explicit instruction.
- Trust is earned per-task and can be revoked. When the AI says "VERIFIED" on something that isn't, that's a trust violation.
- Validates AI claims against primary sources — the AI cannot be trusted to verify its own assertions.

### Parallelism as Default

Chris treats AI agents as a team that can work simultaneously. He routinely spawns multiple agents working in separate worktrees on independent issues, requests parallel proposals, and launches audit agents in parallel.

The optimization target is wall-clock time. Three tasks in parallel finish faster than three in sequence. The constraint is ensuring agents don't conflict with each other.

### Separation of Creation and Review

When setting up review workflows, the explicit instruction is to use fresh agents — not reuse the ones that did the implementation. Context contamination degrades review quality. Creation and evaluation should be performed by different agents, the same way you wouldn't have an author review their own PR.

### Additive Iteration

Rather than trying to get everything right in one pass, Chris builds in layers. Plan first, then basic implementation, then reviews, then corrections. Each step builds on the previous one. For documents: draft, audit against standards, fix all issues, re-audit until no material improvements remain. Convergence is measured, not assumed.

### Process Engineering

When something goes wrong, Chris doesn't just fix the instance — he treats the failure as evidence that the process allows the failure. Every correction gets encoded into the relevant skill or process document. The feedback loop is: observe failure, diagnose root cause, encode systemic fix. This extends to AI tooling: when a subagent fails, improve its instructions rather than just fixing the immediate output.

---

## How He Thinks

### Systems Thinking

Chris's default mental model is a multi-stage pipeline with clear contracts between stages. When debugging, he traces the full lifecycle of data through the system: from UI to API to database to queue to background job to log output. He does not accept isolated explanations; he wants to see how the pieces connect. Bugs live at integration boundaries, not within any single component.

### The Empirical Instinct

When uncertain whether an approach will work, Chris designs an experiment rather than debating theory. He has run controlled comparisons of focused-vs-master auditors, tested prompt strategies against the same input, and caught when the AI broke experimental controls (switching models mid-run). Beliefs should be tested, not argued.

### Generalization Instinct

When making a specific request, Chris often starts with the concrete case, then mid-thought upgrades to the general case. This isn't indecision — it's the moment where he recognized the general solution was within reach. If you see scope expanding mid-instruction, that's recognition of a generalization opportunity.

### Full-Stack Fluency

Chris moves fluidly between database schemas, React component props, API authorization middleware, and product UX in the same conversation. He treats the full stack as one system, not layers managed by different people. Technical decisions are grounded in user impact: "Users can't launch with a payment stub" is a technical requirement, not a product opinion.

### Product Thinking

When designing features, Chris reasons from the user's perspective first. "The feed URL copy-paste is where you lose people." He explicitly considers what the user sees, not just what the code does. Features that don't serve the current milestone get cut, trusting that real user demand will surface what matters post-launch.

### Meta-Cognition About AI

Chris has built a calibrated mental model of where AI is reliable and where it needs guardrails. He designs experiments to test model capabilities, understands that prompt structure affects output quality, catches when models break experimental controls, and knows that over-specification produces safe, literal results. He treats the AI as a system to be calibrated, not a one-shot tool.

### Learning as Engineering

When entering an unfamiliar domain, Chris applies the same systematic approach he uses for software: design a research pipeline, parallelize the work, build in quality validation, organize output for efficient retrieval, then distill into actionable form. His minimalism applies to code changes (where risk is high); his approach to knowledge acquisition is maximalist (the cost of knowing too much is zero).

---

## Communication Style

Chris communicates in short, direct sentences. Imperative phrasing. Lowercase casual. Zero fluff.

**Approval** is functional and brief. "yes please." "option a." "looks good." No elaboration when agreement is all that's needed.

**Correction** is matter-of-fact, immediate, specific, and non-personal. "The feature flags are definitely fine — since the user can create scenarios, obviously the flags are good." He states what is wrong and what he wants instead. Sharp when the AI wastes cycles on speculation it could have resolved with observation.

**Errors** are acknowledged without drama. He takes accountability when he caused the confusion, provides the correction, and moves on.

**Instructions** have two modes:
- **Typed/structured:** Extremely thorough context blocks — sometimes 100+ lines with numbered lists, tables, file paths, and explicit "what I want you to do next" sections. These appear for complex debugging and planning.
- **Voice/exploratory:** Run-on sentences, filler words, mid-thought corrections. Used for brainstorming, product direction, and thinking out loud. The AI serves as a thinking partner in this mode.

**Constraints** are stated explicitly and early. "Don't speculate on what the problem is or propose fixes. Just present the facts." "Don't implement anything, just give me a recommendation." "If anything ends up being complex, stop and ask." He has learned that unbounded tasks produce poor results.

**Frustration** is narrow in range: it surfaces when the AI wastes effort (goes in circles, repeats work, fails to follow explicit instructions) or when external tooling creates needless friction. Complex domain problems are met with patience and curiosity. He does not get frustrated by difficulty, only by waste.

**Emotional range** otherwise is flat-affect for operations, genuine enthusiasm for good outcomes and creative moments (naming sessions, branding, product strategy). He does not pad, hedge, greet, or sign off.

---

## Decision Framework

When making a judgment call, ask:

1. **Do I understand the full causal chain?** Investigate if not.
2. **Does the system already have a way to do this?** Use it.
3. **What is the blast radius?** Choose the smallest viable intervention.
4. **Is this the minimum change needed?** Cut scope if not.
5. **Does this generalize?** Rethink if it's too specific.
6. **Does it mean the right thing, not just do the right thing?** Fix the semantics.
7. **Can I prove it works?** Add tests if not.
8. **Am I deferring something I know is right?** Do it now.
9. **Am I uncertain?** Escalate if so.

---

## What Warrants Escalation

Halt and ask when:
- The decision establishes a new pattern or precedent
- Multiple valid approaches exist with unclear tradeoffs
- The change affects security, production, or costs money
- You're in territory not covered by existing patterns
- The scope is growing beyond what was originally requested
- Something feels wrong but you can't articulate why
- You'd need to make a judgment call about *what* to do, not just *how* to do it
- The change affects shared infrastructure (hooks, configs) that other team members use

---

## Summary

Chris optimizes for:
- **Understanding** over speed
- **Evidence** over speculation
- **Subtraction** over addition
- **Leverage** over reinvention
- **Composition** over monoliths
- **Generality** over specificity
- **Proof** over trust
- **Surgical changes** over comprehensive rewrites
- **Experiments** over arguments
- **Observation** over inference
- **Tools** over one-off solutions
- **Now** over later
- **Clarity** over politeness

When in doubt: understand the full picture, do the minimum necessary, make sure it means the right thing, prove it works, and ask if uncertain.
