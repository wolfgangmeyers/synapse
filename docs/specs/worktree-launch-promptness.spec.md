# Spec: Worktree Launch Option Promptness and Reliability

- **ID**: `SPEC-WORKTREE-LAUNCH-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/managed-worktree-launch.spec.ts`

## 1. Intent & User Outcome

When launching a managed worker from a Git repository's main worktree, the "Create a worktree" option must appear immediately, reliably, and without race-condition dropouts. Wolfgang should experience instantaneous feedback when selecting or re-selecting repository folders and an agile, non-laggy response when typing paths, without background UI activity or streaming router events discarding pending Git checks. Furthermore, launch checks must not block on expensive full-repository status inspections, while termination confirmation flows continue to reliably detect uncommitted working tree changes.

## 2. Core Invariants

1. **Promptness on Folder Selection**: Selecting a folder via "Recent folders" dropdown, directory browser modal, drag-and-drop, or initial dialog opening with a preselected directory MUST trigger the worktree check immediately (0ms delay), without input debouncing.
2. **Responsive Typing Debounce**: Manually typing or editing paths in the working directory input field MUST use a short, responsive debounce of ~150ms rather than a sluggish half-second (500ms) delay.
3. **Parent Re-render Stability**: Re-renders of parent UI components (such as `App`), incoming WebSocket events, log streams, or state updates while the launch dialog is open MUST NEVER cancel, discard, or restart in-flight worktree verification requests.
4. **Client-Side Worktree Check Caching**: In-memory caching by directory path MUST provide instantaneous (0ms) rendering of the "Create a worktree" option when re-selecting or re-entering previously checked paths during the session.
5. **Stale Response Protection**: When switching rapidly between working directories, out-of-order check responses for superseded paths MUST be discarded and MUST NEVER overwrite the state of the currently active path.
6. **Bypass Expensive Status on Launch**: Launch dialog worktree checks MUST pass `include_status=false` (or equivalent query parameter) to skip sequential `git status --porcelain` execution, returning repository and main worktree presence immediately.
7. **Preserve Status Check on Termination**: Worker termination confirmation dialogs MUST pass `include_status=true` (or use the default) so uncommitted changes in worktrees are accurately detected and reported before directory cleanup.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [WTL-001] Immediate Worktree Option Display on Folder Selection
- **User Goal**: Launch a managed worker from the main worktree of a repository selected from recent folders or folder browser.
- **Given**:
  - The launch dialog is open.
  - A Git repository main worktree path exists in the "Recent folders" dropdown or folder browser.
- **When**:
  - The user selects the main worktree folder from the recent folders dropdown or chooses it in the folder browser.
- **Then**:
  - The UI dispatches the worktree check immediately without a 500ms debounce delay.
  - The "Create a worktree" toggle option appears promptly in the launch dialog.
  - The worktree branch and directory name fields become configurable when the toggle is enabled.

### Scenario: [WTL-002] In-Flight Check Resilience Across Parent Re-renders
- **User Goal**: Ensure worktree checks finish and the option displays even when background system events cause parent component re-renders.
- **Given**:
  - The launch dialog is open with a main worktree path entered.
  - A worktree check HTTP request is currently in flight.
- **When**:
  - Parent components (e.g. `App`) re-render due to incoming WebSocket messages, arrival checks, log stream updates, or agent polling.
- **Then**:
  - The in-flight check promise is NOT cancelled or discarded by effect cleanup.
  - The check resolution successfully updates the dialog state.
  - The "Create a worktree" toggle option renders reliably rather than remaining hidden.

### Scenario: [WTL-003] Short Responsive Debounce on Manual Path Typing
- **User Goal**: Experience snappy UI feedback when typing a directory path manually into the working directory field.
- **Given**:
  - The launch dialog is open.
- **When**:
  - The user manually types or pastes a main worktree path into the working directory input.
- **Then**:
  - The check triggers after a responsive debounce (~150ms).
  - The user is not subjected to noticeable 500ms lag before the worktree toggle is presented.

### Scenario: [WTL-004] Client-Side Cache for Previously Checked Paths
- **User Goal**: Instant toggle rendering when toggling between or returning to previously verified paths.
- **Given**:
  - Path `/repos/project-a` has already been checked as a main worktree during the current UI session.
- **When**:
  - The user switches to `/repos/project-b` and then re-selects `/repos/project-a`.
- **Then**:
  - The client-side cache immediately provides the worktree state (0ms delay).
  - The "Create a worktree" option appears instantly without waiting for a new backend round-trip.

### Scenario: [WTL-005] Stale Response Discarding on Rapid Directory Changes
- **User Goal**: Avoid displaying incorrect worktree controls if an earlier slow check finishes after selecting a different directory.
- **Given**:
  - The user selects directory A (which takes time to verify).
- **When**:
  - Before directory A completes, the user changes the working directory to directory B (a non-git directory).
- **Then**:
  - When directory A's response eventually arrives, it is discarded as stale.
  - Directory B correctly shows no "Create a worktree" option.

### Scenario: [WTL-006] Fast Path Check Bypasses Git Status
- **User Goal**: Fast dialog response even in massive repositories with thousands of untracked or modified files.
- **Given**:
  - A repository with a large working tree where `git status --porcelain` takes significant execution time.
- **When**:
  - The launch dialog checks worktree status for the path.
- **Then**:
  - The request is sent with `include_status=false` (e.g., `GET /v1/git/worktree-check?path=...&include_status=false`).
  - The backend verifies repository presence and main worktree status without executing `git status --porcelain`.
  - The response returns promptly with `is_git_repo: true`, `is_main_worktree: true`, and `has_uncommitted_changes: false`.

### Scenario: [WTL-007] Termination Confirmation Retains Status Verification
- **User Goal**: Prevent accidental loss of uncommitted work when terminating a worker and cleaning up its worktree.
- **Given**:
  - A managed worker running in a worktree with uncommitted changes is about to be terminated.
- **When**:
  - The terminate confirmation dialog opens and requests worktree verification.
- **Then**:
  - The request includes `include_status=true` (or relies on the default).
  - The backend runs `git status --porcelain` to accurately evaluate `has_uncommitted_changes`.
  - The confirmation dialog warns the user about uncommitted changes before allowing destructive cleanup.

## 4. Automated E2E & Component Verification Matrix

| Scenario ID | Scenario Description | Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `WTL-001` | Immediate worktree option on folder selection | `tests/e2e/managed-worktree-launch.spec.ts` | `displays create worktree toggle immediately when selecting main worktree` |
| `WTL-002` | Parent re-renders do not discard check | `web/router-ui-react/tests/component/managed-agent-dialog.test.tsx` | `preserves in-flight worktree check during parent component re-renders` |
| `WTL-003` | Responsive typing debounce | `web/router-ui-react/tests/component/managed-agent-dialog.test.tsx` | `debounces manual path input responsively at 150ms` |
| `WTL-004` | Instant toggle display via client cache | `web/router-ui-react/tests/component/managed-agent-dialog.test.tsx` | `instantly renders worktree toggle for cached paths without network delay` |
| `WTL-005` | Discard stale check responses | `web/router-ui-react/tests/component/managed-agent-dialog.test.tsx` | `discards out-of-order check response when directory changes` |
| `WTL-006` | Launch check bypasses git status | `router-proxy/internal/transport` | `TestWorktreeCheckIncludeStatusFalseBypassesGitStatus` |
| `WTL-007` | Termination check includes git status | `router-proxy/internal/transport` | `TestWorktreeCheckIncludeStatusTrueDetectsUncommittedChanges` |
