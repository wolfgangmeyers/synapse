# Spec: Default model save feedback

- **ID**: `SPEC-SETTINGS-DEFAULT-MODEL-001`
- **Status**: `Draft`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/claude-sonnet-default-provider-settings.spec.ts`, `tests/e2e/default-model-save-errors.spec.ts`, `tests/e2e/claude-selector-supplement.spec.ts`

**Related**: [`claude-router-default.spec.md`](./claude-router-default.spec.md) owns the Claude default contract (CD-001, CD-002). This spec owns the save feedback and help copy in Provider settings.

## 1. Intent & User Outcome

When Wolfgang picks a default model in Provider settings, the dialog reports the save accurately: a saved default is never reported as failed, a model-list refresh problem is not reported as a save problem, a rejected save is reported as not saved, and the help text is short.

## 2. Core Invariants

1. A save succeeded when the router's `PUT /v1/settings/default-model` response is received and parsed. Only then does the status say the default was set, and the selector shows the value from that response.
2. A Claude model (`provider: "claude-code"`) is valid in the save response and in `effective_fallback`. Response parsing accepts it.
3. A failed catalog refresh after a successful save does not change the success status or the selector value.
4. A save the router rejects leaves the previous selection in place and reports that the default was not saved.
5. Clearing to Automatic keeps the CD-002 semantics: it chooses the first available Pi model or the built-in Pi fallback, never a Claude model.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [DM-001] Save a default
- **Given**: Claude aliases, Pi models, or both are available.
- **When**: The user selects an option (a Claude alias, a Pi model, or Automatic).
- **Then**: The status reads `New sessions will use <option name>.` (for example, `New sessions will use Claude Sonnet.`). The trigger shows the option. After reload, the trigger still shows it.

### Scenario: [DM-002] Accept a Claude response
- **Given**: The router commits a Claude default and returns `effective_fallback` with `provider: "claude-code"`.
- **When**: The client parses the response.
- **Then**: Parsing succeeds and the status is the success status from DM-001. This is the regression for the false failure: the save had already committed, and the client rejected a valid provider name.

### Scenario: [DM-003] Save succeeds but the catalog refresh fails
- **Given**: The PUT succeeds with a valid response, and the follow-up model or provider-auth request fails.
- **When**: The save completes.
- **Then**: The status is the success status from DM-001 with no failure wording. The selector shows the saved value. A separate neutral note reads `Could not refresh the model list. Reload to check availability.` The note does not replace or alter the save status.

### Scenario: [DM-004] Save is rejected
- **Given**: The router returns an error for the selected option, such as an unavailable model, an invalid model, no available fallback when clearing, or an internal error.
- **When**: The user selects that option.
- **Then**: The status reads `Default model could not be saved.` (error styling applies). The previous selection stays selected. After reload, the previously saved default is shown.

### Scenario: [DM-005] Help text is concise
- **Given**: The Provider settings dialog with the Default model section visible.
- **When**: The user reads the section.
- **Then**: The section has one heading (`Default model`) and one paragraph of two sentences:
  - `Used for new worker launches when no model is chosen.`
  - `Automatic uses a Pi model; Claude models need the Claude CLI installed and signed in on the router host.`

  The section contains no paragraph about embedded subagents. That behavior is governed by CD-006 and CD-007 in `claude-router-default.spec.md`.

## 4. E2E Verification Matrix

| Scenario | Executable coverage | Status |
|----------|---------------------|--------|
| DM-001 | `claude-sonnet-default-provider-settings.spec.ts` (save Sonnet, status, reload); `claude-selector-supplement.spec.ts` (save Haiku, clear to Automatic, reload) | Covered |
| DM-002 | None of the three files. Response parsing lives in `web/router-ui-react/src/transport/http-client.ts` (`savedModel`, default-model response parser). Needs a unit test. | **Gap** |
| DM-003 | `default-model-save-errors.spec.ts`, test "successful default save is not reported as failed when the provider-auth refresh fails" | Covered |
| DM-004 | `default-model-save-errors.spec.ts`, test "rejected default save reports not saved and keeps the previous selection" | Covered for a 503 `INTERNAL` rejection. Not covered: unavailable model, invalid model, no-fallback-on-clear. |
| DM-005 | `claude-selector-supplement.spec.ts` asserts help copy | **Stale**: its regexes do not match the current UI copy (`install the Claude CLI and sign in on the router host; provider sign-in is not required` and `Automatic uses the first available Pi model, or the built-in Pi fallback`). The test must be updated to the copy in DM-005. |

This spec stays `Draft` because DM-002 has no executable coverage and the DM-005 assertion in `claude-selector-supplement.spec.ts` is out of date.

## 5. Maintenance

When the help copy changes, update the existing assertions on that copy in the same change. This spec stays `Draft` until section 4 has no gaps and DM-001 through DM-005 have executable coverage.
