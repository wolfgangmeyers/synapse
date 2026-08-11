---
name: unramble
description: Compress verbose Claude output into concise Wolfgang voice. Use when Claude sends a wall of text that needs condensation before forwarding to a human. Loads the voice persona, extracts the signal, drops the noise, and outputs information-dense prose.
user_invocable: true
---

# Unramble

Compress verbose Claude output into concise, information-dense Wolfgang voice.

Claude's default output style is padded: long explanations, balanced sentence structures, "rather than" contrasts, trailing emphatic fragments, and process narration. The information is valuable but buried. This skill extracts the signal and delivers it in Wolfgang's compact style.

## Usage

```
/unramble [text or reference to text]
```

Or when receiving Claude output via mailbox: read the message, unramble it, and send the compressed version back.

## Process

1. **Load the voice source.** Read `personas/wolfgang-voice.md` from the mecha-developer repo. This is the authoritative style guide for the output.

2. **Extract the actual claims.** Read through the verbose text and identify:
   - What conclusions were reached
   - What evidence supports them
   - What decisions are being proposed or requested
   - What blockers or concerns exist

3. **Drop the noise.** Remove:
   - Process narration ("I checked X", "I ran Y", "Looking at Z")
   - Balanced sentence structures ("Not X. It is Y.")
   - "rather than" and "instead of" padding
   - Trailing emphatic fragments ("Six words." "Zero.")
   - Clauses tacked onto finished sentences
   - Thorough-but-unnecessary proofs when the claim is clear
   - Hedging stacks ("I think maybe we could possibly")

4. **Compress to Wolfgang voice.** Apply the rules from `wolfgang-voice.md`:
   - Start with the point
   - Median sentence: 9 words
   - Use fragments ~25% of the time
   - Hedge opinions with "I think", state facts bluntly
   - Transitions: "but", "so", "ok" — never "however", "furthermore"
   - End when the thought ends
   - No greetings, no sign-offs

5. **Preserve the technical claim.** Never change what a sentence asserts. If the original says "Nick's instinct isn't wrong" but "the evidence doesn't back it", keep both pieces—don't flatten into a false agreement or disagreement.

6. **Output the compressed text.** The result should be roughly 20-40% of the original length, information-dense, and read like a human who understands the code.

## Example

**Verbose Claude input (600+ words):**

> Findings are in. Short version: Nick's instinct isn't wrong in the abstract, but the evidence in this repo doesn't back treating it as a real gap. I'd push back on the review rather than go add a framework.
>
> **What I checked in the repo:**
> - No service in this monorepo uses a migration framework, for any database. This isn't VAN-758 going off-pattern — it's the only pattern that's ever existed here.
> - Pulumi provisions the Spanner instance/database, not the schema. All table DDL lives in `clients.ts`. The RFC (VAN-756) documented this split and pre-flagged the exact fork VAN-758 had to resolve.
> [... 400+ more words ...]

**Compressed output (Wolfgang voice, ~150 words):**

> I think Nick's instinct isn't wrong but the repo doesn't support it. No service here uses a migration framework — this isn't VAN-758 going off-pattern, it's the only pattern that exists. Pulumi provisions the instance, DDL lives in `clients.ts`. The RFC already called this out.
>
> Checked five tools (wrench, Liquibase, Atlas, Bytebase, Flyway). None solve the actual problem: Spanner rejects `ALTER COLUMN ... NOT NULL` on indexed columns. That's a database restriction, not a tooling gap. All of them would need the same drop/alter/rebuild sequence.
>
> Google's own guidance cuts against the premise. Long linear migration histories replayed on deploy are "not recommended for Spanner" due to schema-version throttling. What they recommend — batch DDL, validate before NOT NULL — is what the script already does.
>
> I'd propose a README section documenting the four safety properties the two migration scripts share (dry-run by default, `--apply` guard, project resolution guard, self-run guard). That turns an invisible convention into a checklist. Documentation, not a framework.
>
> Want me to draft that section, reply to Nick, or something else?

## Output Format

For mailbox workflow, the compressed text is the reply body itself. For interactive use, output plain text.

## Rules

- **Never change what a sentence claims.** If Claude says X is true but evidence is weak, keep both. Don't rewrite into a false confidence or a false uncertainty.
- **Preserve dispositions.** If Claude recommended pushing back, keep that. If it proposed three options, keep them in compressed form.
- **Keep the ask.** If Claude ends with "your call" or "let me know", that's part of the message — compress it but don't drop it.
- **Drop thoroughness theater.** A survey of five tools that all lead to the same conclusion needs one sentence, not five bullets.
- **Keep concrete evidence that matters.** The RFC number, the existing pattern, the Google guidance — those stay. The list of what was "checked" narration goes.

## Anti-Patterns

- Don't compress so much that the reasoning is lost. The reader should understand *why* Claude reached its conclusion.
- Don't strip the technical detail. File names, ticket numbers, API specifics stay.
- Don't make it conversational if the original wasn't asking for input. If Claude was just reporting, keep it as a report.
- Don't add opinions Claude didn't express. If Claude was uncertain, stay uncertain.
