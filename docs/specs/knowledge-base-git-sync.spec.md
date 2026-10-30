# Spec: Knowledge Base Git Synchronization, kb_delete, & UI Markdown Browser

- **ID**: `SPEC-KB-001`
- **Status**: `Active`
- **Domain**: `KnowledgeBase`
- **Target E2E Suite**: `tests/e2e/knowledge-base-git-sync.spec.ts`

---

## 1. Intent & User Outcome

The Knowledge Base (KB) serves as persistent, reusable organizational memory across agent sessions. Users and autonomous agents require:
1. Seamless Git synchronization of the Knowledge Base directory (`~/.mecha-developer/kb/`) with a configured remote Git repository, enabling version control, backup, and cross-workstation sharing.
2. An interactive, dark-glassmorphic Web UI modal to configure the Git remote, trigger manual pull/push actions, and browse documentation starting from `INDEX.md`.
3. First-class agent tooling (`kb_delete`, `kb_write`, `kb_read`, `kb_list`, `kb_search`) that automatically synchronizes file changes to and from the remote repository while gracefully falling back to local storage if Git sync is unconfigured.

---

## 2. Core Invariants

1. **Target-Based Git Synchronization**: Knowledge Base Git sync operates on the `"kb"` target via `/v1/git-sync/kb`, implementing full directory synchronization (`PullDirectory` and `PushDirectory`) between the remote repository and the local KB storage directory.
2. **Graceful Local Fallback**: When no Git repository URL is configured for target `"kb"`, all KB tools (`kb_write`, `kb_delete`, `kb_read`, `kb_list`, `kb_search`) must operate normally against the local filesystem without error or warning interruptions.
3. **Automated Pull-Before and Push-After Hooks**: When a remote repository is configured, modifying KB tools (`kb_write` and `kb_delete`) opportunistically pull remote changes prior to applying file/index modifications and push the committed modifications up to the remote repository.
4. **Index Consistency on Deletion**: Calling `kb_delete` removes the target markdown file and removes its reference from `INDEX.md`, maintaining index integrity.
5. **Path Traversal Protection**: Neither HTTP file serving endpoints (`/v1/kb/file`) nor agent tools (`kb_read`, `kb_write`, `kb_delete`) permit path traversal outside the KB root directory (`SanitizeKBPath`).
6. **In-Dialog Markdown Browsing**: The Web UI dialog provides a lightweight markdown viewer that defaults to rendering `INDEX.md`, intercepts internal markdown links to navigate between topics within the dialog, and provides breadcrumb navigation ("Back to Index").
7. **Design System Conformance**: The modal dialog adheres to the design system (dark glassmorphism, compact `.icon-btn-round` action buttons, and clear section separation).

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-KB-001] User configures Git repository for Knowledge Base
- **User Goal**: Connect the local Knowledge Base to a GitHub or remote Git repository.
- **Given**:
  - The Web UI is open and the user clicks the actions menu trigger.
  - The user selects "Knowledge Base" from the menu.
- **When**:
  - The user enters a valid Git repository URL (e.g. `https://github.com/org/knowledge-base.git`) into the Repository URL input.
  - The user clicks the "Save" button.
- **Then**:
  - A `PUT /v1/git-sync/kb` request is submitted with `{"repo_url": "..."}`.
  - The UI displays a success confirmation or updated status indicator.
  - The target URL is stored durably in the router configuration.

#### Scenario: [SCN-KB-002] User browses Knowledge Base documentation in the UI
- **User Goal**: Read KB topics directly inside the Web UI without leaving the dashboard.
- **Given**:
  - The Knowledge Base dialog is open.
- **When**:
  - The dialog loads `GET /v1/kb/file` (defaulting to `INDEX.md`).
- **Then**:
  - The rendered Markdown displays the table of contents and links to topics.
  - When the user clicks a topic link (e.g. `architecture.md`), the dialog intercepts the click, fetches `GET /v1/kb/file?path=architecture.md`, and renders the topic content in-place.
  - A "Back to Index" button appears, allowing the user to return to the root index.

#### Scenario: [SCN-KB-003] Agent deletes a Knowledge Base topic using `kb_delete`
- **User Goal**: Allow an autonomous agent to remove obsolete documentation cleanly.
- **Given**:
  - The agent is executing an embedded session.
  - A topic file `outdated-spec.md` exists in the KB directory and is referenced in `INDEX.md`.
- **When**:
  - The agent invokes `kb_delete` with `{"topic": "outdated-spec"}` (or `{"path": "outdated-spec.md"}`).
- **Then**:
  - The file `outdated-spec.md` is removed from disk.
  - The link entry for `outdated-spec` is removed from `INDEX.md`.
  - If a Git remote is configured, the changes are committed and pushed.
  - The tool returns a success confirmation string indicating the file and index entry were deleted.

#### Scenario: [SCN-KB-004] User executes manual Git Pull or Push from Web UI
- **User Goal**: Force an immediate synchronization with the remote repository.
- **Given**:
  - The Knowledge Base dialog is open and a valid Git repository URL is configured.
- **When**:
  - The user clicks "Pull" or "Push".
- **Then**:
  - The frontend sends `POST /v1/git-sync/kb/pull` or `POST /v1/git-sync/kb/push`.
  - A progress spinner indicates active synchronization.
  - On completion, the sync status is updated and content refreshed.

### Edge & Error Scenarios

#### Scenario: [SCN-KB-005] Graceful fallback when Git remote is unconfigured
- **Given**:
  - No Git repository URL is configured for `kb`.
- **When**:
  - An agent invokes `kb_write` or `kb_delete`.
- **Then**:
  - Local file creation, modification, or deletion succeeds without failure.
  - The missing Git remote does not throw an error or block the agent.

#### Scenario: [SCN-KB-006] Path traversal attack rejected
- **Given**:
  - A malicious or malformed request attempts to access `GET /v1/kb/file?path=../../etc/passwd` or an agent calls `kb_delete` with `{"path": "../secret.txt"}`.
- **When**:
  - The server processes the request.
- **Then**:
  - `SanitizeKBPath` rejects the relative path traversal.
  - The HTTP endpoint returns HTTP 400 Bad Request with an error description.
  - The agent tool returns a descriptive error without modifying or disclosing external files.

#### Scenario: [SCN-KB-007] Deleting a non-existent topic returns clear error
- **Given**:
  - An agent calls `kb_delete` with `{"topic": "non-existent-topic"}`.
- **When**:
  - Neither `non-existent-topic` nor `non-existent-topic.md` exists in the KB storage.
- **Then**:
  - The tool returns a clear error message stating the topic or file was not found.
  - `INDEX.md` is left unmodified.
