---
name: test-coverage
description: "Run code coverage analysis and identify gaps with concrete test suggestions. Language-agnostic design with Go as the first supported language."
---

# Test Coverage Skill

Runs the test suite with coverage instrumentation, parses per-function coverage output, and uses a subagent to identify gaps and suggest specific missing tests. Returns an overall coverage percentage, a ranked list of undertested functions, and concrete test case names with assertions to write.

## Usage

This is a SKILL, not a CLI command or binary. You run it by reading this `SKILL.md` and following the Process below yourself — there is no `test-coverage` executable to invoke and no `/test-coverage` slash command. The skill takes three conceptual parameters, described below; they are inputs you apply while following the process, not command-line flags:

| Parameter | Type | Default | Description |
|---|---|---|---|
| package | string | `./...` | Package pattern to test, e.g. `./pkg/llm/...` |
| focus | string | *(none)* | Substring match against full file path. When set, only files whose full path contains this substring are passed to the subagent, regardless of coverage percentage. E.g. focus `agentrun` matches `pkg/llm/agentrun.go`. |
| lang | string | auto-detect | Language. Auto-detect checks for `go.mod` → Go. Currently only `go` is supported. If both `go.mod` and other language markers exist, Go takes precedence. |

### Examples (how the parameters map to behavior)

- No parameters → cover the whole module (`./...`)
- package `./pkg/llm/...` → cover just that package pattern
- package `./pkg/llm/...` + focus `agentrun` → cover that pattern, force-include files whose path contains `agentrun`
- focus `pkg/operations/glueai` → whole module, force-include files under that path

---

## What it does

1. **Run coverage** — execute the test suite with coverage instrumentation
2. **Parse results** — extract per-file and per-function coverage percentages from the output
3. **Spawn a subagent** — pass it the coverage report and relevant source files; have it identify gaps and suggest specific missing tests
4. **Report** — return overall %, a ranked list of uncovered/undertested functions, and concrete test suggestions

The subagent is used (rather than direct in-context analysis) because source files for the selected packages may exceed context limits.

---

## Process

### 1. Determine language

- If the `lang` parameter is specified, use it.
- Otherwise, auto-detect: if `go.mod` exists in the current directory → Go. Go takes precedence if multiple language markers exist.
- If language cannot be determined or is not `go`, print:
  `Error: unsupported language '<lang>'. Currently supported: go`
  and stop.

### 2. Run coverage

**Go:**
```bash
go test -coverprofile=/tmp/mw-coverage.out -covermode=atomic ./...
go tool cover -func=/tmp/mw-coverage.out
```

Replace `./...` with the `package` parameter if provided. (`-covermode=atomic` is used to correctly handle concurrent tests.)

If the test run exits non-zero but `/tmp/mw-coverage.out` was written, print:
`Warning: some tests failed — coverage data may be incomplete`
and continue.

If `go test` fails and no coverage file was written, print the error and stop.

### 3. Parse the `-func` output

The `go tool cover -func` output has one line per function:
```
pkg/foo/bar.go:42:    FunctionName    67.3%
```
And a final `total:` line:
```
total:                                (statements)    71.2%
```

- Extract the `total:` line's percentage as the overall coverage figure.
- For every other line, parse: `{file, function, pct}`.
- Build a coverage map: `file → [{function, pct}]`.

### 4. Select files for the subagent

Include a file in the subagent context if **any** of the following apply:

- Any of its functions has coverage below 50%
- the `focus` parameter is set and the file's full path contains the `focus` substring (regardless of coverage %)

**Exclude regardless** (see "What to skip" below for the full list).

Read the full source of each selected file. Also read any corresponding `_test.go` file for the same base name (e.g., `bar_test.go` for `bar.go`) and include it — this lets the subagent avoid suggesting tests that already exist.

### 5. Spawn subagent

Use the Agent tool with `subagent_type: general-purpose`.

Pass the subagent prompt below, substituting:
- `<LANGUAGE>` → the detected/specified language (e.g., `Go`)
- `<PACKAGE>` → the package pattern used (e.g., `./pkg/llm/...`)
- `<COVERAGE_REPORT>` → the full `go tool cover -func` output
- `<SOURCE_FILES>` → the source and test file contents for selected files, labeled with filename headers

### 6. Output

Print the subagent's structured report verbatim. If the subagent deviates from the expected format, print as-is — do not reformat.

---

## Subagent prompt template

```
You are a test coverage analyst. You have been given a <LANGUAGE> coverage report and source files.

Coverage report:
<COVERAGE_REPORT>

Source files (each preceded by a filename header):
<SOURCE_FILES>

Your job:
1. Identify functions/methods with 0% or low coverage (< 50%)
2. For each gap, read the source code and suggest 1-3 specific test cases by name and what they should assert
3. Flag Blocking gaps (exported functions with 0% coverage, error paths untested)
4. Flag Should-Add gaps (important logic branches missing coverage)
5. Skip trivial gaps — but only these specific categories: getters/setters with no conditional logic; struct constructors that only assign literal field values with no logic; String()/Error()/MarshalJSON()/UnmarshalJSON() with no branching; generated code (// Code generated / // DO NOT EDIT); init() functions; pure delegation functions that only call one other function with no transformation, error wrapping, or branching. Do NOT skip a function just because it is short, private, or "obvious" — if it has branching, error returns, or transforms data, it needs a test.
6. If a suggested test case already exists in the provided test file, skip it
7. Default to flagging: a gap is real unless the function clearly meets the skip criteria in step 5. When in doubt, flag it. Trivial exceptions are narrow; meaningful behavior needs coverage.

Output a structured report in this exact format:

## Coverage Report: <PACKAGE>
Overall: <pct>%

### Blocking Gaps
- `file.go` — `FunctionName` (0%) — suggest: TestFunctionName_validInput (asserts X), TestFunctionName_nilContext (asserts returns error when ctx is nil)

### Should Add
- `file.go` — `functionName` (33%) — suggest: TestFunctionName_maxRetries (asserts loop exits after N attempts)

### Well Covered
- file.go — 87%

Rank blocking gaps by: exported > error paths > core logic.
Skip functions that clearly meet the trivial criteria in step 5 — but default to flagging. Do not skip without a specific reason matching the criteria above.
For each suggestion, name the test and describe in one clause what it asserts.
```

---

## What to skip

**File-level exclusions** (Step 4 does not select these):

- `vendor/` directory
- `_test.go` files (excluded from selection; they are read as context but not analyzed for gaps)
- Files containing `// Code generated` or `// DO NOT EDIT` in the first 10 lines

**Function-level exclusions** (subagent does not flag these as gaps):

- `init()` functions
- `String()`, `Error()`, `MarshalJSON()`, `UnmarshalJSON()` with no branching logic
- Simple getters and setters with no conditional logic
- Struct constructors that only assign literal field values with no logic
- Pure delegation functions: only call one other function with no transformation, error wrapping, or conditional logic

**Default to flagging**: a function qualifies as trivial only if it clearly matches one of the above categories. Do not skip a function because it is short, private, or "obvious". Any function with branching logic, error returns, or data transformation belongs in the report.

---

## Notes

- **Empty package**: If `go test` produces no coverage output (e.g., all files have build constraints that exclude the current platform), print: `No coverage data produced for <package>` and stop.
- **No test files**: If a package has no `_test.go` files, `go test` will succeed with 0% coverage. This is valid input — the subagent will flag all exported functions as Blocking Gaps.
- **Large packages**: When running `./...`, the number of selected files may be large. The 50% threshold and exclusion rules naturally limit the set. If the `focus` parameter is set, only matching files are included regardless of package size.

---

## Adding support for new languages

To extend this skill to a new language:

1. Add a detection rule in Step 1 (e.g., `package.json` → Node.js)
2. Add a "Run coverage" variant in Step 2 with that language's commands
3. The subagent prompt is language-agnostic — substitute the `<LANGUAGE>` placeholder
