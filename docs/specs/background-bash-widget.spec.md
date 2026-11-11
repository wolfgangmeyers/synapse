# Spec: Background shell widget

- **ID**: `SPEC-BB-WIDGET-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/background-bash-widget.spec.ts`

## 1. Intent & User Outcome

The selected agent's shell widget exposes active work and retained results without changing job lifetime or persistence.

## 2. Core Invariants

1. The count includes running and unresolved uncertain jobs only, and is visible at zero; completed jobs remain in the popup while retained.
2. Details are fetched on demand, not in agent-list or live projections, without debiting tool polling credits. Only the current fenced owner's jobs are exposed.
3. All available output is shown as preformatted code, with truncation indicated. Output is decoded as UTF-8; invalid byte sequences are replaced with the Unicode replacement character. Known exit codes are shown; unknown codes are not invented.
4. Existing in-memory bounds apply: 65,536 bytes shared output, one hour and 256 records across the manager; no restart persistence. Unavailable results are described honestly.

## 3. User Scenarios

### Scenario: [BBW-001] Empty and active jobs
- **Given**: A managed agent is selected with zero jobs.
- **When**: The user opens the widget, starts jobs, and jobs finish.
- **Then**: The widget remains visible at zero, counts only running and uncertain jobs, and the popup shows retained jobs and live statuses. When the count is nonzero, the header control visibly pulses like the subagent control; at zero it does not pulse. The popup first lists jobs; selecting one opens a separate detail view with a back control. Only the selected job's detail is fetched. While the popup is open, a selected running job refreshes approximately every two seconds; uncertain and terminal jobs are not polled. A terminal or uncertain result is fetched on selection and again when its projected status changes. Failed reads require explicit retry. Requests time out; closing cancels them. Reads do not consume tool polling credits. The count and displayed status use the list projection.

### Scenario: [BBW-002] Inspect output and retention
- **Given**: Jobs produce output, including nonzero exit and output exceeding the capture bound.
- **When**: The user opens the popup or a result expires or becomes unavailable.
- **Then**: The popup shows all available captured output as preformatted code, known exit codes and truncation warnings, and explains that results expire or are unavailable without displaying a router restart warning; no output or code is fabricated.

### Scenario: [BBW-003] Isolation and errors
- **Given**: Different agents or routers have different jobs, or the detail request fails.
- **When**: The user switches selection or opens the popup during an error.
- **Then**: Only the selected owner's details are shown, even immediately when the agent or client changes while the dialog stays open and job ID and status match. In-flight responses from the previous agent or client cannot replace the current detail. A missing or expired job is shown as unavailable; other failures are shown as load errors with a retry action, including for finished jobs. The list projection supplies the displayed status, consistent with the header count.

## 4. Automated E2E Verification Matrix

| Scenario | Test | Status |
| --- | --- | --- |
| BBW-001 | `tests/e2e/background-bash-widget.spec.ts` | Pending |
| BBW-002 | `tests/e2e/background-bash-widget.spec.ts` | Pending |
| BBW-003 | `tests/e2e/background-bash-widget.spec.ts` | Pending |

This supplements BB-009 of `background-bash.spec.md`; both specifications remain Draft. The widget E2E passed on a subsequent isolated run after an earlier run failed selecting another agent because the conversation intercepted the click. The matrix remains pending: that single test does not cover every scenario and invariant above.
