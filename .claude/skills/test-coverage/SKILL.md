---
name: test-coverage
description: "Run language-agnostic code coverage analysis and identify gaps with concrete test suggestions."
---

# Test Coverage Skill

Runs a project's test suite with coverage instrumentation, normalizes the coverage output, and uses a subagent to identify gaps and suggest specific missing tests. Returns an overall coverage percentage, a ranked list of undertested files/functions, and concrete test case names with assertions to write.

## Usage

This is a SKILL, not a CLI command or binary. You run it by reading this `SKILL.md` and following the Process below yourself; there is no `test-coverage` executable to invoke and no `/test-coverage` slash command. The skill takes three conceptual parameters, described below. They are inputs you apply while following the process, not command-line flags.

| Parameter | Type | Default | Description |
|---|---|---|---|
| package | string | language default | Package, module, test path, or project selector to test. Examples: Go `./pkg/llm/...`, Python `tests/unit`, Node `src/foo`, Gradle `:app`. |
| focus | string | *(none)* | Substring match against full file path. When set, files whose full path contains this substring are passed to the subagent regardless of coverage percentage. |
| lang | string | auto-detect | Language/toolchain. Supported values: `go`, `node`, `python`, `gradle`. Auto-detect by manifest files when omitted. |

### Examples

- No parameters -> detect the project and cover the default test target.
- package `./pkg/llm/...` -> cover that Go package pattern.
- package `tests/api` -> run Python coverage for that test path.
- package `src/widgets` + focus `retry` -> run the Node test command for that path and force-include source files whose path contains `retry`.
- focus `pkg/operations/glueai` -> cover the default target and force-include matching files.

---

## What it does

1. **Detect language/toolchain** from project manifests or the explicit `lang` parameter.
2. **Run coverage** using the project's existing coverage command/script when available; otherwise use the language fallback documented below.
3. **Parse and normalize results** into `{file, symbol, pct}` rows plus an overall coverage percentage.
4. **Spawn a subagent** with the coverage report and selected source/test files so it can identify missing behavioral tests.
5. **Report** the subagent's structured output verbatim.

The subagent is used because selected source files may exceed the main agent's context limits.

---

## Process

### 1. Determine language and toolchain

If the `lang` parameter is specified, use it. Otherwise inspect manifests in the repository root and nearest relevant subdirectory:

| Manifest/config | Language/toolchain |
|---|---|
| `go.mod` | `go` |
| `package.json` | `node` |
| `pyproject.toml`, `setup.py`, `setup.cfg`, `requirements*.txt`, `tox.ini`, `Pipfile`, `poetry.lock` | `python` |
| `settings.gradle`, `settings.gradle.kts`, `build.gradle`, `build.gradle.kts`, `gradlew` | `gradle` |
| `Cargo.toml`, `Gemfile`, `composer.json`, `.csproj`, `.sln`, or other known manifests | known toolchain, use generic fallback guidance unless a local coverage command is obvious |

When multiple manifests exist, choose the toolchain that owns the requested `package`/`focus` path. If there is no path clue, prefer the manifest nearest the changed files under review. Do not blindly prefer one language across a polyglot repo.

If no known toolchain is detected:

1. Report the manifest files you looked for.
2. Search project docs and scripts for coverage commands:
   ```bash
   rg -n "coverage|coverprofile|jacoco|pytest-cov|nyc|c8|vitest|jest|lcov|cobertura" .
   ```
3. If no trustworthy command is found, stop with:
   `No supported coverage toolchain detected. Tried: go.mod, package.json, Python manifests, Gradle manifests, and common coverage command references. Add a project coverage command or run the appropriate language coverage tool manually, then feed the report into the parsing/subagent steps.`

### 2. Run coverage

Always prefer an existing project coverage script over a generic command, because it may set required environment, test filters, build tags, or report formats. If the test command exits non-zero but a coverage report was produced, print `Warning: some tests failed - coverage data may be incomplete` and continue. If no coverage report was produced, print the error and stop.

Use a fresh report path under `/tmp` unless the project command requires its own output location.

For tools that include only files imported by tests unless configured otherwise, first identify the production source roots. Use existing coverage config when it defines them; otherwise infer roots from project layout (`src/`, package/module directories, `lib/`, or the requested `package`/`focus` path). The coverage run must include unimported production files so never-tested files appear as 0% rather than disappearing from the report.

#### Go

Preserve the existing behavior:

```bash
go test -coverprofile=/tmp/mw-coverage.out -covermode=atomic ./...
go tool cover -func=/tmp/mw-coverage.out
```

Replace `./...` with the `package` parameter if provided. `-covermode=atomic` correctly handles concurrent tests.

Parse `go tool cover -func` output. It has one line per function:

```text
pkg/foo/bar.go:42:    FunctionName    67.3%
```

and a final total line:

```text
total:                                (statements)    71.2%
```

Normalize every function line to `{file, symbol, pct}` and the `total:` line to the overall percentage.

#### JavaScript / TypeScript

Read `package.json` first.

1. If a script named `coverage`, `test:coverage`, or similar exists, run it with the package manager used by the repo:
   - `pnpm run coverage`
   - `yarn coverage`
   - `npm run coverage`
2. Otherwise detect the test runner from scripts and dependencies. If the repo lacks configured source inclusion (`collectCoverageFrom`, Vitest `coverage.include`, nyc/c8 include config, or equivalent), pass inferred source roots explicitly:
   - Jest: `npx jest --coverage --coverageReporters=text --coverageReporters=lcov --collectCoverageFrom='<source-root>/**/*.{js,jsx,ts,tsx}'`
   - Vitest: `npx vitest run --coverage --coverage.reporter=text --coverage.reporter=lcov --coverage.include='<source-root>/**/*.{js,jsx,ts,tsx}'`
   - c8: `npx c8 --all --include '<source-root>/**/*.{js,jsx,ts,tsx}' --reporter=text --reporter=lcov npm test`
   - nyc: `npx nyc --all --include '<source-root>/**/*.{js,jsx,ts,tsx}' --reporter=text --reporter=lcov npm test`
3. If the repo has `pnpm-lock.yaml`, `yarn.lock`, or `package-lock.json`, use the matching package manager when invoking scripts. Prefer local binaries through that package manager (`pnpm exec`, `yarn`, `npx`) rather than global installs.

If `package` is provided, pass it only in the runner-supported way. Examples: `jest <path> --coverage`, `vitest run <path> --coverage`. Do not append arbitrary paths to an existing project script unless the script is known to forward arguments.

Prefer text output for immediate parsing and `lcov.info` for file/function detail:

- Common locations: `coverage/lcov.info`, `coverage/coverage-final.json`, `coverage/clover.xml`.
- Parse LCOV `SF:` file records plus line records (`DA:`) for file percentages. LCOV `FN:`/`FNDA:` records are function names and execution counts, not percentages; normalize functions with `FNDA` count `0` as `0%`, count `>0` as `hit` with no invented percentage, and include the containing file's line percentage as context. Do not turn a hit count into a fake partial percentage.
- If only summary table output exists, normalize file-level rows as `{file, symbol: "(file)", pct}` and still run the subagent on low-coverage files.

#### Python

Prefer existing coverage configuration in `pyproject.toml`, `setup.cfg`, `.coveragerc`, or `tox.ini`.

Use one of these commands, in this order:

1. Existing project command from docs, Makefile, tox, nox, or scripts, if it clearly runs coverage.
2. If `pytest-cov` is configured or installed:
   ```bash
   pytest --cov=<source-root> --cov-report=term-missing --cov-report=xml:/tmp/mw-coverage.xml
   ```
3. Otherwise use coverage.py:
   ```bash
   coverage run --source=<source-root> -m pytest
   coverage report -m
   coverage xml -o /tmp/mw-coverage.xml
   ```

If existing config already defines `source`, `include`, or `omit`, respect it instead of adding a conflicting `--source`/`--cov` flag. If no source root can be inferred, report that the run may omit unimported files and stop for clarification rather than producing a misleading "no gaps" report.

If `package` is provided, pass it as the pytest test target unless project config indicates it is a source package selector.

Parse `coverage report -m` for file-level rows. Prefer `/tmp/mw-coverage.xml` (Cobertura XML) when available for line-rate and branch-rate by file. Python coverage does not normally produce per-function rows; normalize file-level rows as `{file, symbol: "(file)", pct}` unless the project has a richer report plugin. The subagent must inspect selected source files to identify functions and branches behind low file coverage.

#### Kotlin / Android / JVM (Gradle + JaCoCo)

Use the checked-in wrapper when present:

```bash
./gradlew test jacocoTestReport
```

For Android projects, prefer the project-specific debug unit-test task and JaCoCo task when configured, for example:

```bash
./gradlew testDebugUnitTest jacocoTestReport
```

If `package` is a Gradle project path, scope the tasks:

```bash
./gradlew :app:test :app:jacocoTestReport
```

Parse JaCoCo XML reports from common locations:

- `build/reports/jacoco/test/jacocoTestReport.xml`
- `*/build/reports/jacoco/*/*.xml`
- Android variant-specific `*/build/reports/jacoco/**/jacoco*.xml`

Normalize method counters to `{file, symbol, pct}` where possible. If the XML only yields class/file counters, normalize file-level rows as `{file, symbol: "(file)", pct}`.

### 3. Generic fallback for known but unsupported toolchains

For known manifests without first-class instructions in this skill, do not claim the language is unsupported and stop silently. Instead:

1. Report the detected manifest and language/toolchain.
2. Search for an existing coverage command:
   ```bash
   rg -n "coverage|cover|lcov|cobertura|jacoco|simplecov|tarpaulin|grcov|kcov|llvm-cov|dotCover|coverlet" .
   ```
3. If an obvious project command exists, run it and parse any LCOV, Cobertura XML, JaCoCo XML, or text summary it produces using the normalization rules below, then continue through source selection, subagent analysis, and output.
4. If no command exists, provide specific next-step guidance for the detected ecosystem, for example:
   - Rust: add/run `cargo llvm-cov` or `cargo tarpaulin`.
   - Ruby: add/run SimpleCov through the test suite.
   - PHP: run PHPUnit with PCOV/Xdebug coverage.
   - .NET: run `dotnet test --collect:"XPlat Code Coverage"` or Coverlet.

Only stop at this fallback step when no usable coverage command or report is available. The stop message must include what was tried and which report formats this skill can consume.

### 4. Normalize coverage data

Convert the tool-specific output into:

```text
Overall: <pct>%
Rows:
- file=<path> symbol=<function|method|class|(file)> pct=<pct|0|hit>
```

If the tool gives line coverage but no function coverage, keep file-level rows and let the subagent inspect the source. If the tool gives function execution counts rather than percentages, use `0` for never-hit functions and `hit` for executed functions, with the file's line percentage beside it for context. If branch coverage is available, include it in the report text even if the normalized row uses line coverage as `pct`.

### 5. Select files for the subagent

Include a production source file in the subagent context if any of the following apply:

- Any normalized row for that file has coverage below 50%.
- The `focus` parameter is set and the file's full path contains the `focus` substring.
- The report only has package/module totals and the file is under the requested package/focus path.

Exclude regardless:

- Dependency/build output directories: `vendor/`, `node_modules/`, `.venv/`, `venv/`, `dist/`, `build/`, `.gradle/`, `target/`, `coverage/`.
- Test files as primary gap targets. They may be read as context.
- Generated files: files containing `// Code generated`, `# Code generated`, `/* Code generated`, `// DO NOT EDIT`, `# DO NOT EDIT`, or equivalent generated markers in the first 10 lines.

Read the full source of each selected production file. Also read corresponding test files using the ecosystem's conventions:

- Go: `foo_test.go`
- JS/TS: `foo.test.ts`, `foo.test.tsx`, `foo.spec.ts`, `foo.spec.tsx`, equivalent `.js/.jsx`, and files under `__tests__/`
- Python: `test_foo.py`, `foo_test.py`, and matching tests under `tests/`
- JVM/Kotlin: matching `*Test.java`, `*Test.kt`, `*Spec.kt` under `src/test`, `src/androidTest`, or variant-specific test directories

### 6. Spawn subagent

Use the Agent tool with `subagent_type: general-purpose`.

Pass the subagent prompt below, substituting:

- `<LANGUAGE>` -> detected/specified language and toolchain, e.g. `Python (coverage.py)`.
- `<PACKAGE>` -> package/test target used, e.g. `./pkg/llm/...`, `tests/api`, `:app`.
- `<COVERAGE_REPORT>` -> the normalized coverage report plus the original tool output summary.
- `<SOURCE_FILES>` -> selected production and test file contents, labeled with filename headers.

### 7. Output

Print the subagent's structured report verbatim. If the subagent deviates from the expected format, print as-is; do not reformat.

---

## Subagent prompt template

```text
You are a test coverage analyst. You have been given a <LANGUAGE> coverage report and source files.

Coverage report:
<COVERAGE_REPORT>

Source files (each preceded by a filename header):
<SOURCE_FILES>

Your job:
1. Identify functions/methods/files with 0% or low coverage (< 50%).
2. For each gap, read the source code and suggest 1-3 specific test cases by name and what they should assert.
3. Flag Blocking gaps (exported/public functions or public API behavior with 0% coverage, security/auth/data-integrity behavior untested, or error paths untested).
4. Flag Should-Add gaps (important logic branches, edge cases, or integration behavior missing coverage).
5. Skip trivial gaps, but only these specific categories: getters/setters/properties with no conditional logic; constructors that only assign literal field values with no logic; String()/Error()/toString()/repr()/serialization methods with no branching; generated code; init/module-load functions with no behavior; pure delegation functions that only call one other function with no transformation, error wrapping, side effects, or branching. Do not skip a function just because it is short, private, or "obvious" - if it has branching, error returns, side effects, or transforms data, it needs a test.
6. If a suggested test case already exists in the provided test files, skip it.
7. Default to flagging: a gap is real unless the function clearly meets the skip criteria in step 5. Trivial exceptions are narrow; meaningful behavior needs coverage.

Output a structured report in this exact format:

## Coverage Report: <PACKAGE>
Overall: <pct>%

### Blocking Gaps
- `file.ext` - `FunctionName` (0%) - suggest: TestFunctionName_validInput (asserts X), TestFunctionName_nilContext (asserts returns error when context/input is nil)

### Should Add
- `file.ext` - `functionName` (33%) - suggest: TestFunctionName_maxRetries (asserts loop exits after N attempts)

### Well Covered
- file.ext - 87%

Rank blocking gaps by: public API > security/auth/data integrity > error paths > core logic.
Skip functions that clearly meet the trivial criteria in step 5, but default to flagging. Do not skip without a specific reason matching the criteria above.
For each suggestion, name the test and describe in one clause what it asserts.
```

---

## Notes

- **Empty target**: If the coverage tool produces no coverage data because the target has no source files or all files are excluded by platform/build constraints, print `No coverage data produced for <package>` and stop.
- **No test files**: A tool may succeed with 0% coverage when there are no tests. This is valid input; the subagent should flag public/exported behavior as Blocking gaps.
- **Large projects**: When running whole-repo coverage, the number of selected files may be large. The 50% threshold and exclusion rules should limit the set. If `focus` is set, prioritize matching files even if the full report is large.
- **Polyglot repos**: Run this process separately for each relevant toolchain when the requested package/focus spans multiple languages. Report each language separately rather than merging incompatible coverage percentages.
- **Report formats**: Prefer LCOV, Cobertura XML, JaCoCo XML, or a stable text summary. Avoid parsing ephemeral terminal UI output when a machine-readable report is available.
