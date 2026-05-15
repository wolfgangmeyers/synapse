---
name: voice
description: "Generate a voice profile from writing samples so an LLM can reproduce how someone speaks and writes. Source-agnostic: works with Facebook exports, chat logs, email archives, plain text, or any writing samples."
user_invocable: true
---

# Voice

Reverse-engineer a person's linguistic fingerprint from writing samples and write the result to `personas/<name>-voice.md`.

The output is not a biography or decision-making persona. It is a prompt-oriented voice profile focused on sentence rhythm, diction, punctuation habits, transitions, tone shifts, emphasis patterns, and recurring phrases. Complementary to `generate-persona` (which captures how someone *thinks and decides*) — this skill captures how someone *speaks and writes*.

## Usage

```
/voice --name <name> --source <source> <paths...>
```

- `--name`: Required. Used for the output file path `personas/<name>-voice.md`
- `--source`: Required. One of `facebook-json`, `chat-export`, `email-archive`, or `text-files`
- `<paths...>`: One or more files or directories containing the source material

Examples:
```
/voice --name wolfgang --source facebook-json ~/exports/facebook/
/voice --name wolfgang --source chat-export ~/exports/slack/
/voice --name wolfgang --source email-archive ~/mail/archive.mbox
/voice --name wolfgang --source text-files notes/ drafts/
```

## Output

Write the final profile to:

```text
personas/<name>-voice.md
```

## Process

### 1. Extract Source Material

Create a working directory:

```bash
mkdir -p /tmp/voice-analysis
```

Use the source-specific extractor to normalize writing into JSONL records. The skill is source-agnostic — any writing samples work. Use whichever extractor matches the input format.

#### Facebook JSON exports

```bash
python3 .claude/skills/voice/tools/extract_facebook_export.py <paths...> \
  [--author-name "Full Name"] \
  > /tmp/voice-analysis/chunks.jsonl
```

Handles Facebook's mojibake encoding (latin-1 bytes stored as unicode escapes) automatically. Use `--author-name` when the export contains multiple authors, especially message threads.

#### Chat exports

```bash
python3 .claude/skills/voice/tools/extract_chat_export.py <paths...> \
  [--author-name "Full Name"] \
  > /tmp/voice-analysis/chunks.jsonl
```

#### Email archives

```bash
python3 .claude/skills/voice/tools/extract_email_archive.py <paths...> \
  [--author-email person@example.com] \
  [--author-name "Full Name"] \
  > /tmp/voice-analysis/chunks.jsonl
```

#### Plain text files

```bash
python3 .claude/skills/voice/tools/extract_text_files.py <paths...> \
  > /tmp/voice-analysis/chunks.jsonl
```

All extractors emit JSONL records with this shape:

```json
{
  "text": "normalized writing sample",
  "metadata": {
    "source_type": "facebook-json",
    "source": "/path/to/file.json",
    "date": "2026-04-04T12:34:56Z",
    "context": {
      "author": "Example Name",
      "field": "content",
      "json_path": "$.messages[0].content"
    }
  }
}
```

### 2. Compute Quantitative Fingerprint

Run the voice statistics tool on the extracted data:

```bash
python3 .claude/skills/voice/tools/voice_stats.py /tmp/voice-analysis/chunks.jsonl \
  -o /tmp/voice-analysis/stats.json
```

This produces a JSON file with:
- Sentence length distribution (min, P25, median, P75, max)
- Vocabulary richness (type-token ratio, hapax legomena ratio)
- Punctuation frequency per 1000 words (em dash, ellipsis, exclamation, question mark, parenthetical, semicolon, colon)
- Fragment ratio (sentences without verbs)
- Question ratio
- Paragraph length distribution
- Top opening and closing bigrams
- Top content words (stopwords excluded)

### 3. Read the Corpus Directly

Do not rely only on the quantitative stats. Read the JSONL chunks themselves and look for:
- rhythm that statistics miss
- contrast between short and long forms
- register shifts across contexts
- topic transitions
- sarcasm, understatement, or humor
- pet phrases and phrasing tics

### 4. Qualitative Voice Analysis

Read the stats JSON and the JSONL chunks. For large corpora, split chunks into batches for parallel analysis.

**Batching rule:** Group records by total text size, aiming for roughly equal work per batch (50-100 records each). Launch one subagent per batch, all in parallel.

**Pass the quantitative stats to every subagent** as grounding context.

**Subagent prompt:**

---

You are analyzing writing samples to reverse-engineer someone's **voice** — the specific way they write, not what they think. The quantitative stats below ground your analysis in measurable patterns. Your job is to identify the qualitative rules that explain *why* the numbers are what they are.

**Quantitative fingerprint (from voice_stats.py):**
```
[paste stats JSON here]
```

Analyze these **voice dimensions**:

**Sentence Rhythm**
- Length distribution patterns — do they mix short punchy sentences with long ones? Or stay consistent?
- Fragment usage — when and why do they use sentence fragments?
- Run-on tendencies — do they chain clauses with commas, dashes, or conjunctions?
- Sentence openers — do they vary, or lean on particular patterns?

**Vocabulary Signature**
- Preferred words and phrases — what words come up repeatedly that aren't just stopwords?
- Jargon and domain language — specialized vocabulary and when they deploy it
- Formality level — colloquial vs. formal, and when they shift
- Intensifiers and qualifiers — "really", "super", "kind of", "literally", etc.

**Punctuation Habits**
- Em dash vs. parenthetical vs. comma for asides — which do they reach for?
- Ellipsis usage — trailing off, pausing, or implying?
- Exclamation marks — frequency and what triggers them
- Question marks — rhetorical questions vs. genuine
- Semicolons, colons — do they use them at all?

**Opening and Closing Patterns**
- How do they start messages, posts, comments? Cold start vs. greeting vs. context-setting?
- How do they sign off? Abrupt ending vs. summary vs. call to action?
- Does this change by context (casual post vs. long form)?

**Hedging vs. Directness**
- Where on the spectrum: "I think maybe we could" vs. "Do X"
- When do they hedge vs. assert? (topic familiarity? audience? stakes?)
- Confidence markers — how do they signal certainty or uncertainty?

**Humor and Tone**
- Sarcasm, irony, understatement, or hyperbole — which is their mode?
- Self-deprecation vs. outward-directed humor
- Emotional range — how wide, and what triggers shifts?

**Emphasis Patterns**
- ALL CAPS — frequency and triggers
- Italics or bold (if medium supports it)
- Repetition for emphasis ("very very", "no no no")
- Strategic profanity or exclamation

**Transition Style**
- How do they shift between topics within a single message?
- Paragraph break patterns — frequent short paragraphs or longer blocks?
- Connective language — "anyway", "so", "but yeah", "on another note"

**Pet Phrases and Idioms**
- Recurring expressions that are distinctive to this person
- Catchphrases, verbal tics, filler patterns
- Metaphors or analogies they reach for

**Register Shifts**
- How does their voice change between contexts? (social media post vs. private message vs. comment reply)
- What stays constant across registers (the core voice)?
- What shifts (formality, length, humor)?

For each pattern you identify, state the **generalized rule**, not a specific example. The goal is rules an LLM can follow to write new text in this voice. Include 1-2 brief illustrative quotes where they make the pattern concrete, but the rule itself must stand alone.

Write your analysis to: `/tmp/voice-analysis/notes-batch-<N>.md`

---

### 5. Select Exemplar Quotes

After the qualitative analysis completes, scan the JSONL chunks and select **3-5 representative quotes per register** (casual, formal, emotional, etc.) that best exemplify the person's core voice.

Selection criteria:
- Characteristic of the person's voice (not generic)
- Diverse in topic (not all about the same thing)
- Show the voice dimensions identified in analysis (rhythm, vocabulary, tone)
- Short enough to be useful as examples (1-3 sentences each)

### 6. Write the Voice Profile

Read all batch analysis files and the stats JSON. Synthesize into the voice profile using this template:

```markdown
# <Name>: Voice Profile

Brief description of this person's overall writing voice in 2-3 sentences.
What makes them immediately recognizable?

---

## Quantitative Fingerprint

| Metric | Value |
|--------|-------|
| Sentence length (median) | N words |
| Sentence length range (P25-P75) | N-N words |
| Vocabulary richness (TTR) | 0.XX |
| Fragment usage | N% of sentences |
| Questions per 100 sentences | N |
| Em dashes per 1000 words | N.N |
| Ellipses per 1000 words | N.N |
| Exclamation marks per 1000 words | N.N |
| Parentheticals per 1000 words | N.N |

---

## Core Voice Rules

- 6-12 high-confidence rules stated as instructions an LLM can follow

## Sentence Rhythm

- Average length tendencies
- Fragment/run-on behavior
- Cadence notes

## Vocabulary And Diction

- Preferred words
- Technicality/formality
- Repeated constructions

## Punctuation And Formatting

- Dashes, parentheses, ellipses, exclamation marks, question marks
- Lists, sentence fragments, one-line paragraphs
- Emphasis patterns

## Openings, Transitions, And Closings

- Common entry points
- How topic shifts happen
- How messages end

## Tone And Interpersonal Stance

- Directness vs hedging
- Warmth vs detachment
- Correction style
- Humor style

## Register Shifts

### Casual
### Professional
### Review / critique
### Long-form writing

## Signature Phrases

- Distinctive phrases or recurring constructions

---

## Anti-Patterns

Things this person NEVER does — equally important for voice accuracy:
- [e.g., "Never uses semicolons"]
- [e.g., "Never starts a sentence with 'Furthermore'"]
- [e.g., "Never uses formal transitions like 'In conclusion'"]

---

## Usage Instructions

When writing as <Name>, follow these rules in order:

1. Match the sentence length distribution: median N words, mix short (N) and long (N)
2. Use fragments N% of the time — especially for [context]
3. [Punctuation rules]
4. [Vocabulary rules]
5. [Tone rules]
6. [Opening/closing rules]
7. [Register-specific adjustments]
8. Avoid the anti-patterns listed above

---

## Register Examples

### Casual (social media, chat)
[2-3 representative quotes]

### Professional (documents, code review)
[2-3 representative quotes]

### Emotional (strong reactions)
[2-3 representative quotes]

---

## Imitation Prompt

A compact instruction block for future prompting.
```

Rules:
- Keep it optimized for LLM imitation, not literary criticism.
- State uncertainty where evidence is thin.
- Separate high-confidence traits from weak guesses.
- Do not pad the profile with generic style advice.
- Preserve contradictory behavior when it is context-dependent instead of flattening it away.

### 7. Quality Check

Before finishing, verify:

- [ ] Quantitative fingerprint table is populated from actual stats (not estimated)
- [ ] Every voice dimension has concrete, actionable rules — not vague descriptions
- [ ] Pet phrases are listed with usage context, not just as a flat list
- [ ] Register examples are real quotes from the source data, not fabricated
- [ ] Anti-patterns are genuine observations, not generic writing advice
- [ ] Usage instructions are ordered and specific enough for an LLM to follow mechanically
- [ ] The profile covers what stays constant (core voice) AND what shifts (register)
- [ ] Opening/closing patterns are documented per context type
- [ ] Humor/tone rules distinguish between types (sarcasm vs. understatement vs. hyperbole)
- [ ] Output path is correct (`personas/<name>-voice.md`)
- [ ] The document is usable as a system prompt — structured for LLM consumption

## Extensibility

### Adding a new source

1. Create `tools/extract_<source>.py` following the JSONL contract
2. Use `_common.py` for shared helpers (`chunk_text`, `emit_record`, `iter_files`)
3. Add a subsection under **Step 1: Extract Source Material**

### Updating the stats module

The `voice_stats.py` module is self-contained. Add new metrics by extending the `analyze()` function and updating the quantitative fingerprint table in the profile template.

## Reference

- See `generate-persona` skill for the complementary persona generation process
- Voice profiles complement personas: a persona tells the LLM *what to think*, a voice profile tells it *how to say it*
