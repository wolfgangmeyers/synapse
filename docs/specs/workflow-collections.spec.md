# Spec: Multiple Workflow Collections with Per-Collection Git Sync, Move, and Copy

- **ID**: `SPEC-WORKFLOW-COLLECTIONS-001`
- **Status**: `Active`
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-collections.spec.ts`

---

## 1. Intent & User Outcome

As users build out more complex agent pipelines, workflows naturally group into domain-specific suites (e.g. CI/CD pipelines, triage workflows, code review bots, and personal automation). Users require:
1. **Organizational Collections**: Grouping workflows into distinct collections, with an automatic "Default" collection ensuring existing workflows and newly created ones have a clear, backward-compatible home.
2. **Per-Collection Git Synchronization**: Configuring individual Git remote repositories for each collection. Pushing synchronizes only that collection's workflows (`workflows.json` in the remote repo), and pulling replaces local workflows within that specific collection without altering workflows in other collections.
3. **Workflow Reorganization**: Seamlessly moving or copying workflows between collections, with deterministic duplicate name resolution when copying.
4. **Safe Collection Management**: Creating, renaming, and deleting collections, with explicit safety safeguards preventing accidental deletion of non-empty collections without user confirmation.

---

## 2. Core Invariants

1. **Default Collection Guarantee & Immutability**:
   - The system guarantees at least one collection exists: the "Default" collection (`id = "default"`).
   - Database migrations backfill any existing workflows into the "Default" collection.
   - Newly created workflows without an explicit collection specification belong to the "Default" collection (or the currently active collection in the UI).
   - The "Default" collection is immutable: it cannot be renamed (`PUT /v1/workflow-collections/default` returns HTTP 400 Bad Request) and cannot be deleted (`DELETE /v1/workflow-collections/default` returns HTTP 400 Bad Request). It can, however, be configured with its own Git sync remote via target `workflows:default`.

2. **Per-Collection Workflow Name Uniqueness**:
   - Workflow names are unique **within** each collection (`UNIQUE (collection_id, name)`).
   - Two distinct collections MAY contain workflows with identical names.

3. **Collection Identity & Ownership**:
   - Every workflow belongs to exactly one collection via `collection_id NOT NULL REFERENCES workflow_collections(id)`.
   - Workflow states, transitions, schedules, and active worker bindings continue to be scoped to their workflow.

4. **Move vs. Copy Semantics**:
   - **Move (`POST /v1/workflows/:id/move`)**: Reparents an existing workflow to a new collection specified in `{ "destination_collection_id": string }`. All state definitions, transitions, active worker bindings, and schedules are preserved in-place. If the destination collection already contains a workflow with that name, the move is rejected with HTTP 409 Conflict.
   - **Copy (`POST /v1/workflows/:id/duplicate`)**: Extended to accept optional `{ "destination_collection_id": string, "name": string }`. Clones the workflow and all of its states/transitions into the target collection with a new unique workflow ID. Active worker bindings and execution journals are NOT copied. If the name exists in the target collection, a suffix (e.g., `Copy of <Name>` or `<Name> (Copy)`) is deterministically generated to satisfy per-collection uniqueness.

5. **Per-Collection Git Synchronization via Existing `/v1/git-sync/:target` Transport**:
   - Git sync remote configurations are mapped per collection target (`workflows:<collection_id>`).
   - For backward compatibility, the target `workflows` aliases `workflows:default`.
   - `POST /v1/git-sync/workflows:<id>/push` serializes and exports **only** workflows belonging to collection `:id` into `workflows.json` and pushes to the collection's configured Git remote.
   - `POST /v1/git-sync/workflows:<id>/pull` fetches and replaces **only** workflows belonging to collection `:id` from the remote repository's `workflows.json`. Workflows belonging to other collections remain untouched.

6. **Deletion Protection & Cascading**:
   - Deleting an empty collection deletes the collection record immediately.
   - Attempting to delete a non-empty collection without an explicit cascade confirmation flag fails with HTTP 409 Conflict.
   - When cascade deletion is confirmed, deleting the collection cascades to all child workflows, their workflow states, transitions, and schedules (with active managed agent bindings set to NULL / cancelled).
   - The default collection (`id = "default"`) is protected and CANNOT be deleted under any circumstance (HTTP 400 Bad Request).

7. **Design System Conformance**:
   - The Workflows UI integrates a collection switcher/selector and a collection management modal matching the dark glassmorphic design system, compact `.icon-btn-round` actions, and clean separation between list and form views.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-COLL-001] Migration of existing workflows into Default collection
- **User Goal**: Ensure existing workflows remain accessible without data loss after collections support is introduced.
- **Given**:
  - The database has pre-existing workflows from prior schema versions.
- **When**:
  - The router executes schema migrations on startup.
- **Then**:
  - A collection named "Default" with ID `"default"` is created if not present.
  - All existing workflows are assigned `collection_id = "default"`.
  - The existing global `git_sync_targets` entry for `"workflows"` is mapped to or respected for the `"default"` collection.
  - Workflows list and canvas views load existing workflows without interruption.

#### Scenario: [SCN-COLL-002] User creates and switches between collections in UI
- **User Goal**: Organize workflows into separate projects/categories.
- **Given**:
  - The user navigates to the Workflows page.
  - The "Default" collection is currently selected.
- **When**:
  - The user opens the Collection Selector dropdown and clicks "+ New Collection".
  - The user enters "DevOps Automation" and submits.
- **Then**:
  - A `POST /v1/workflow-collections` request is sent with `{"name": "DevOps Automation"}`.
  - The new collection is created and selected as the active collection.
  - The workflows list switches to show only workflows in "DevOps Automation" (displaying an empty state initially).
  - The collection selector displays "DevOps Automation" with its workflow count.

#### Scenario: [SCN-COLL-003] User renames a collection
- **User Goal**: Update a user-created collection's title.
- **Given**:
  - User-created collection "DevOps Automation" exists.
- **When**:
  - The user selects "Edit Collection" from the collection action menu.
  - The user changes the name to "Cloud Infrastructure" and saves.
- **Then**:
  - A `PUT /v1/workflow-collections/:id` request is sent with `{"name": "Cloud Infrastructure"}`.
  - The UI updates the collection title in the switcher and headers immediately.

#### Scenario: [SCN-COLL-004] User moves a workflow from Collection A to Collection B
- **User Goal**: Transfer an existing workflow to another collection without cloning.
- **Given**:
  - Collection A ("Default") contains workflow "Deployment Pipeline" (`id = "wf-deploy"`).
  - Collection B ("Cloud Infrastructure", `id = "col-cloud"`) exists and does NOT contain a workflow named "Deployment Pipeline".
- **When**:
  - The user opens the action menu on the "Deployment Pipeline" card.
  - The user clicks "Move to Collection" and selects "Cloud Infrastructure".
- **Then**:
  - A `POST /v1/workflows/wf-deploy/move` request is sent with `{"destination_collection_id": "col-cloud"}`.
  - The workflow is reparented to Collection B.
  - "Deployment Pipeline" disappears from Collection A's list.
  - Switching to Collection B displays "Deployment Pipeline" with all of its original states, transitions, and settings intact.

#### Scenario: [SCN-COLL-005] User copies a workflow from Collection A to Collection B
- **User Goal**: Duplicate an existing workflow template into another collection for customization.
- **Given**:
  - Collection A contains workflow "Triage Bot" (`id = "wf-triage"`).
  - Collection B (`id = "col-cloud"`) exists.
- **When**:
  - The user opens the action menu on "Triage Bot" and clicks "Copy to Collection".
  - The user selects Collection B and confirms.
- **Then**:
  - A `POST /v1/workflows/wf-triage/duplicate` request is sent with `{"destination_collection_id": "col-cloud"}`.
  - A new workflow is created in Collection B with cloned states, layout positions, transitions, variables, and snooze duration.
  - Collection A's "Triage Bot" remains unmodified.
  - If Collection B already had a workflow named "Triage Bot", the cloned copy receives a unique name (e.g. "Triage Bot (Copy)").

#### Scenario: [SCN-COLL-006] User configures per-collection Git sync and pushes workflows
- **User Goal**: Sync only workflows of a specific collection to a dedicated Git repository.
- **Given**:
  - The user is viewing collection "Cloud Infrastructure" (`id = "col-cloud"`).
  - The collection currently has no Git sync remote configured.
- **When**:
  - The user enters `git@github.com:org/cloud-infra-workflows.git` in the collection's Git Sync bar and clicks "Save".
  - The user clicks the "Push to Git" button.
- **Then**:
  - The router saves the repository URL via `PUT /v1/git-sync/workflows:col-cloud`.
  - The router exports only the workflows in "Cloud Infrastructure" into `workflows.json` and pushes via `POST /v1/git-sync/workflows:col-cloud/push`.
  - The sync status confirms successful push with the count of workflows in this collection.

#### Scenario: [SCN-COLL-007] User pulls workflows for a specific collection from Git
- **User Goal**: Update a collection from a remote repository without overwriting other collections.
- **Given**:
  - Collection "Cloud Infrastructure" (`id = "col-cloud"`) has a configured Git sync remote.
  - Collection "Default" has separate workflows that are not in the remote repository.
- **When**:
  - The user clicks "Pull from Git" in "Cloud Infrastructure" and confirms the modal dialog.
- **Then**:
  - The router executes pull via `POST /v1/git-sync/workflows:col-cloud/pull`.
  - Only workflows in "Cloud Infrastructure" are replaced with the bundles from the remote repository's `workflows.json`.
  - Workflows in "Default" (and all other collections) remain untouched and intact.

#### Scenario: [SCN-COLL-008] User deletes an empty collection
- **User Goal**: Remove an unused collection.
- **Given**:
  - A user-created collection "Obsolete" exists and contains 0 workflows.
- **When**:
  - The user selects "Delete Collection" and confirms.
- **Then**:
  - `DELETE /v1/workflow-collections/:id` succeeds with HTTP 200 OK.
  - The collection is removed from the selector.
  - The UI switches active collection back to "Default".

### Edge & Error Scenarios

#### Scenario: [SCN-COLL-009] Prevention of deleting or renaming the Default collection
- **User Goal**: Prevent breaking system invariants.
- **Given**:
  - The user is viewing the "Default" collection (`id = "default"`).
- **When**:
  - The user attempts to delete or rename the default collection (via API or UI).
- **Then**:
  - The UI does not provide "Delete" or "Edit/Rename" actions for the Default collection.
  - An API request `DELETE /v1/workflow-collections/default` returns HTTP 400 Bad Request with error code `cannot_delete_default_collection`.
  - An API request `PUT /v1/workflow-collections/default` returns HTTP 400 Bad Request with error code `cannot_modify_default_collection`.

#### Scenario: [SCN-COLL-010] Deleting a non-empty collection requires explicit cascade confirmation
- **User Goal**: Prevent accidental loss of workflows inside a collection.
- **Given**:
  - Collection "Production" contains 3 workflows.
- **When**:
  - A client sends `DELETE /v1/workflow-collections/:id` without `?cascade=true`.
- **Then**:
  - The router returns HTTP 409 Conflict with error code `collection_not_empty` and details `{ "workflow_count": 3 }`.
  - In the UI, the delete confirmation modal warns the user that 3 workflows will be permanently deleted and requires explicit confirmation.
  - When confirmed with `?cascade=true`, the collection and its child workflows are deleted cleanly.

#### Scenario: [SCN-COLL-011] Conflict when moving a workflow to a collection with duplicate name
- **User Goal**: Prevent violating per-collection unique name constraint.
- **Given**:
  - Collection A has a workflow named "Nightly Build" (`id = "wf-nightly"`).
  - Collection B (`id = "col-b"`) already has a workflow named "Nightly Build".
- **When**:
  - A client attempts to move "Nightly Build" from Collection A to Collection B via `POST /v1/workflows/wf-nightly/move` with `{"destination_collection_id": "col-b"}`.
- **Then**:
  - The router rejects the request with HTTP 409 Conflict and error code `workflow_name_conflict`.
  - The workflow remains in Collection A.
  - The UI displays an error prompt: "A workflow named 'Nightly Build' already exists in the destination collection."

#### Scenario: [SCN-COLL-012] Copying a workflow to a collection with duplicate name auto-renames
- **User Goal**: Allow copying workflows without manual upfront renaming while preserving uniqueness.
- **Given**:
  - Collection A has a workflow named "Nightly Build" (`id = "wf-nightly"`).
  - Collection B (`id = "col-b"`) already has a workflow named "Nightly Build".
- **When**:
  - The user copies "Nightly Build" to Collection B via `POST /v1/workflows/wf-nightly/duplicate` with `{"destination_collection_id": "col-b"}`.
- **Then**:
  - The duplicate operation creates a new workflow in Collection B named "Nightly Build (Copy)" (or "Nightly Build (Copy 2)" if conflict persists).
  - The request succeeds with HTTP 201 Created.

#### Scenario: [SCN-COLL-013] Validation errors on collection creation and renaming
- **User Goal**: Ensure collection names are valid and non-colliding.
- **Given**:
  - The user is in the Collection modal.
- **When**:
  - The user attempts to save a collection with an empty/whitespace name, or a name identical to an existing collection.
- **Then**:
  - Empty name returns HTTP 400 Bad Request (`collection_name_required`).
  - Duplicate collection name returns HTTP 409 Conflict (`collection_name_conflict`).
  - The UI displays inline field errors and prevents invalid submission.

#### Scenario: [SCN-COLL-014] Moving or copying to a nonexistent collection
- **User Goal**: Handle invalid destination collection references gracefully.
- **Given**:
  - A workflow exists in Collection A (`id = "wf-1"`).
- **When**:
  - A client posts a move (`POST /v1/workflows/wf-1/move`) or duplicate (`POST /v1/workflows/wf-1/duplicate`) request referencing `destination_collection_id = "nonexistent-id"`.
- **Then**:
  - The router returns HTTP 404 Not Found with error code `collection_not_found`.
  - No database state is altered.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-COLL-001` | Migration of existing workflows into Default collection | `tests/e2e/workflow-collections.spec.ts` | `migrates pre-existing workflows into default collection` |
| `SCN-COLL-002` | Create and switch between workflow collections in UI | `tests/e2e/workflow-collections.spec.ts` | `creates and switches active workflow collection in UI` |
| `SCN-COLL-003` | Rename a workflow collection | `tests/e2e/workflow-collections.spec.ts` | `renames an existing workflow collection` |
| `SCN-COLL-004` | Move workflow from Collection A to Collection B | `tests/e2e/workflow-collections.spec.ts` | `moves workflow between collections and verifies isolation` |
| `SCN-COLL-005` | Copy workflow from Collection A to Collection B | `tests/e2e/workflow-collections.spec.ts` | `copies workflow between collections with state cloning` |
| `SCN-COLL-006` | Configure per-collection Git sync and push workflows | `tests/e2e/workflow-collections.spec.ts` | `configures collection git sync target and pushes collection workflows` |
| `SCN-COLL-007` | Pull workflows for a specific collection from Git | `tests/e2e/workflow-collections.spec.ts` | `pulls collection workflows replacing only targeted collection` |
| `SCN-COLL-008` | Delete an empty workflow collection | `tests/e2e/workflow-collections.spec.ts` | `deletes empty collection and returns to default collection` |
| `SCN-COLL-009` | Prevent deletion and modification of Default collection | `tests/e2e/workflow-collections.spec.ts` | `rejects deletion or renaming of default collection via API and hides UI actions` |
| `SCN-COLL-010` | Non-empty collection deletion requires cascade confirmation | `tests/e2e/workflow-collections.spec.ts` | `rejects deletion of non-empty collection without cascade confirmation` |
| `SCN-COLL-011` | Move conflict when destination has workflow with duplicate name | `tests/e2e/workflow-collections.spec.ts` | `prevents moving workflow when name conflicts in destination collection` |
| `SCN-COLL-012` | Copy auto-renames on duplicate name in destination collection | `tests/e2e/workflow-collections.spec.ts` | `auto-renames copied workflow when name exists in destination collection` |
| `SCN-COLL-013` | Validation errors on empty or duplicate collection name | `tests/e2e/workflow-collections.spec.ts` | `validates collection name requirements on create and update` |
| `SCN-COLL-014` | Move or copy to nonexistent collection returns 404 | `tests/e2e/workflow-collections.spec.ts` | `returns 404 when moving or copying to nonexistent collection` |
| `SCN-COLL-015` | Browser UI card action menu: interactive move and copy | `tests/e2e/workflow-collections.spec.ts` | `UI interaction: moves and copies workflow via card action buttons and verifies modal flow` |
