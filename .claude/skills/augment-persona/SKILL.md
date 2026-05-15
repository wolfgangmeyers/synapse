---
name: augment-persona
description: "Analyze a persona file for gaps, generate targeted scenario-based questions, and integrate answers back into the persona. Trigger: user asks to augment, improve, fill gaps in, or add depth to a persona file. Do NOT use for generating personas from scratch — use generate-persona for that."
---

# Augment Persona

Analyze an existing persona file for blind spots, generate scenario-based questions to fill them, and integrate the answers as generalized rules that match the persona's existing style.

## Usage

```
/augment-persona <persona-file>
```

Examples:
```
/augment-persona personas/wolfgang.md
/augment-persona personas/evan-reviewer.md
```

## Process

### 1. Detect Persona Type and Analyze Gaps

Read the persona file. Detect the type from structural markers:

- **Supervisor persona**: Contains sections like "Decision-Making Principles", "Working Style", "Delegation Model", "How They Think", "Decision Framework", "What Warrants Escalation". Structured around principles with "This means:" bullets.
- **Reviewer persona**: Contains sections like "Core Review Philosophy", "Technical Areas of Focus", "Communication Style", "Levels of Severity", "Patterns You Commonly Identify", "Technical Preferences". Structured around review patterns with example quotes.

Score each dimension below as one of:
- **WELL-COVERED**: Multiple principles, concrete rules, clear behavioral guidance
- **THIN**: Mentioned or implied but lacks depth — only a sentence or two, no actionable rules
- **MISSING**: Not addressed anywhere in the persona

#### Supervisor Dimensions (25)

Typically well-covered after generation:
1. Priority framework
2. Risk calibration
3. Scope control
4. Instruction style
5. Correction approach
6. Delegation model
7. Trust calibration
8. Iteration style
9. Verification approach
10. Systems thinking
11. Generalization instinct

Typically thin or missing:
12. Conflict handling — how they mediate disagreements between people or competing priorities
13. Values under pressure — what they sacrifice first and what they protect when time/resources are constrained
14. Mentoring philosophy — how they develop junior engineers vs. senior engineers
15. Hiring/team philosophy — what they optimize for when building or evaluating teams
16. Long-term technical vision — where they want the architecture/platform to be in 2-3 years
17. Engineering culture values — what kind of engineering org they want to build
18. Cross-domain thinking — how they apply patterns from one domain to another
19. Failure post-mortem philosophy — how they analyze failures beyond the immediate fix
20. Non-negotiables list — bright lines that cannot be crossed regardless of context
21. Tradeoff philosophy — how they reason about tradeoffs when no option is clearly better
22. Relationship to deadlines — how they handle externally imposed time constraints
23. Knowledge sharing expectations — how they expect knowledge to flow through a team
24. Tool/process adoption philosophy — how they evaluate and adopt new tools or processes
25. Autonomy calibration — how much independence they grant based on what signals

#### Reviewer Dimensions (23)

Typically well-covered after generation:
1. Architecture preferences
2. Performance awareness
3. Error handling philosophy
4. Type safety stance
5. Testing expectations
6. API design opinions
7. Security awareness
8. Code organization preferences
9. Tone and voice
10. Severity markers
11. Characteristic phrases

Typically thin or missing:
12. Philosophical stance on ambiguity — how they review code when requirements are unclear
13. Mentoring vs. gatekeeping balance — how they adjust review depth for junior vs. senior authors
14. Non-negotiables vs. flexible items — explicit bright lines vs. negotiable preferences
15. Cross-team review approach — how they review code outside their primary domain
16. Review scope philosophy — where they draw the line on out-of-scope feedback
17. Design review vs. code review — how they distinguish architectural feedback from implementation feedback
18. Response to disagreement — how they handle pushback on their review comments
19. Time pressure behavior — how their review standards change under deadline pressure
20. Testing philosophy beyond coverage — what they believe tests should prove, not just cover
21. Documentation expectations — when they require docs and what form they should take
22. Onboarding perspective — what they think a new team member needs to understand first
23. Technical debt triage — how they decide which debt to pay now vs. defer vs. accept

Present the full inventory to the user, grouped by coverage level (MISSING first, then THIN, then WELL-COVERED), before proceeding to question generation.

### 2. Generate Questions

Determine question count based on the number of gaps:

| Gaps (THIN + MISSING) | Questions |
|------------------------|-----------|
| 1-3                    | 4-6       |
| 4-6                    | 7-10      |
| 7+                     | 10-15     |

If more than 60% of dimensions are MISSING, suggest re-generating the persona from source data first — augmentation works best when there's a solid foundation to build on.

**Question design rules:**

1. **Scenario-based over direct.** Ask "A junior engineer pushes back on your review comment, saying the current approach is simpler. How do you respond?" — not "What's your mentoring philosophy?"
2. **Reference the existing persona.** Ground questions in what's already known: "Your persona shows you value X. When that conflicts with Y, which wins?"
3. **One dimension per question.** Don't combine gaps into compound questions.
4. **Answerable by the person or a close colleague.** The scenario should be concrete enough that someone who works with them could answer.
5. **Grouped by dimension** with a brief rationale for why it's a gap (one sentence explaining what the persona doesn't currently cover).
6. **MISSING dimensions before THIN dimensions.** Prioritize filling complete blind spots over deepening existing coverage.

Present all questions in a single message. Tell the user they can answer all at once or in multiple responses, and can skip any question or stop early.

### 3. Collect Answers

Accept answers in one or multiple responses. Track which questions have been answered. When the user indicates they're done (or all questions are answered), proceed to synthesis.

If the user answers only some questions, proceed with what's available — partial augmentation is better than none.

### 4. Synthesize and Integrate

For each answered question:

1. **Distill into generalized rules.** Extract the principle, not a paraphrase of the answer. The rule should apply to novel situations, not just the specific scenario asked about.
2. **Map to the correct section.** Determine where in the persona structure this rule belongs — an existing section that needs strengthening (THIN) or a new subsection (MISSING).
3. **Strengthen or add.**
   - For THIN dimensions: add bullets, sub-principles, or depth to the existing section.
   - For MISSING dimensions: create a new subsection in the appropriate parent section, following the persona's existing heading hierarchy and formatting.
4. **Match formatting conventions exactly.** Study the persona's patterns:
   - Heading levels and styles (markdown `###` vs. `ALL CAPS` vs. bold)
   - Bullet point style (dashes vs. numbered)
   - Whether principles use "This means:" blocks, "Patterns:" blocks, example quotes, or other structures
   - Voice (first person, third person, imperative)
   - Length and density of existing sections
5. **Update the Summary section** if new value pairs emerged (e.g., "**Mentoring** over gatekeeping"). Only add pairs that represent genuine value priorities surfaced by the answers.
6. **Check for contradictions.** If a new rule conflicts with an existing one, flag it to the user rather than silently overwriting.

**Show the full diff to the user before writing.** Present what will be added/changed, section by section. Get explicit approval before modifying the file.

### 5. Quality Checklist

Before presenting the diff, verify:

- [ ] Every addition is a generalized rule, not a paraphrase of the user's answer
- [ ] Each addition is placed in the correct section of the persona
- [ ] Voice and formatting match the persona's existing conventions exactly
- [ ] No contradictions with existing principles (or contradictions are flagged)
- [ ] Summary section is updated if new value pairs emerged
- [ ] New subsections use the same heading hierarchy as existing ones
- [ ] The augmented persona reads as a coherent document, not a document with bolted-on additions
