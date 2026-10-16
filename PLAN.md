# PLAN: Embedded Worker Context Thresholds & Compaction Hint Removal

## 1. Overview & Architecture Mapping

This plan implements Phase 2 dev-loop engineering for embedded worker context thresholds and compaction hint cleanup as specified in `SPEC.md`.

### Component Architecture & Interactions
```
┌─────────────────────────────────────────────────────────────┐
│ Web UI (React)                                              │
│ - ContextThresholdDialog.tsx (DialogShell, round icon btns) │
│ - Sidebar menu: "Context Thresholds"                        │
│ - ConversationHeader: Clickable context bar / controls      │
│ - http-client.ts: contextThresholds() & save...             │
└──────────────────────────────┬──────────────────────────────┘
                               │ HTTP GET/PUT /v1/settings/context-thresholds
┌──────────────────────────────▼──────────────────────────────┐
│ HTTP Transport Router                                       │
│ - internal/transport/context_threshold_http.go              │
│ - Input validation (0 < warning < reset <= 100)             │
└──────────────────────────────┬──────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────┐
│ Store & SQLite Migration                                    │
│ - migrations/103_context_threshold_settings.sql             │
│ - managed_settings columns:                                 │
│     context_warning_percent (default 10)                    │
│     context_reset_percent (default 20)                      │
│     context_model_overrides (JSON, default '{}')            │
│ - internal/store/managed_context_thresholds.go              │
└──────────────────────────────┬──────────────────────────────┘
                               │ Cached / Atomic Read
┌──────────────────────────────▼──────────────────────────────┐
│ Managed Execution Engine                                    │
│ - embedded_context_reset.go (dynamic threshold resolution)  │
│ - embedded_compaction_tool.go -> context status tool        │
│ - In-band reminder: 10% warning prompt to scratchpad        │
│ - Fallback auto-reset: 20% hard boundary                    │
│ - Compaction string cleanup in mecha_context & session      │
└─────────────────────────────────────────────────────────────┘
```

---

## 2. Requirement-to-Task & File Mapping

| SPEC Requirement | Module / Component | Files to Change / Create | Exact Deliverable |
| :--- | :--- | :--- | :--- |
| **REQ-1**: Schema Migration | Store / SQLite | `router-proxy/internal/store/migrations/103_context_threshold_settings.sql` | Add `context_warning_percent`, `context_reset_percent`, `context_model_overrides` to `managed_settings`. |
| **REQ-2**: Store Interface & Persistence | Store | `router-proxy/internal/store/managed_context_thresholds.go`<br>`router-proxy/internal/store/managed_context_thresholds_test.go` | `ContextThresholdConfig`, `ModelThresholdOverride`, `GetContextThresholdConfig`, `SetContextThresholdConfig`. |
| **REQ-3**: Dynamic Threshold Resolution | Managed Engine | `router-proxy/internal/managed/embedded_context_reset.go`<br>`router-proxy/internal/managed/models.go` | `ResolveContextThresholds(model Model, cfg ContextThresholdConfig)`, evaluate per-model override matching exact ID or Provider/ID, fallback to global (10/20). |
| **REQ-4**: In-band Warning & Auto-Reset | Managed Engine | `router-proxy/internal/managed/embedded_context_reset.go` | Update `checkContextResetAfterTurn` to evaluate resolved thresholds; format reminder with scratchpad instructions and active reset % boundary; enforce single-epoch reminder delivery. |
| **REQ-5**: Compaction Hint Removal | Managed Tools / Prompts | `router-proxy/internal/managed/embedded_compaction_tool.go`<br>`router-proxy/internal/managed/embedded_session.go`<br>`router-proxy/internal/managed/root_control_manager.go` | Remove legacy "compaction is deprecated" and "compaction" from `mecha_context` guidance, tool descriptions, policy denial messages, and unsupported action responses. |
| **REQ-6**: REST API Endpoints | Transport | `router-proxy/internal/transport/context_threshold_http.go`<br>`router-proxy/internal/transport/context_threshold_http_test.go`<br>`router-proxy/internal/transport/http.go`<br>`router-proxy/internal/transport/managed_http.go` | Implement `GET /v1/settings/context-thresholds` and `PUT /v1/settings/context-thresholds` with payload validation ($1 \le \text{warning} < \text{reset} \le 100$). |
| **REQ-7**: React Web UI Dialog | Web UI | `web/router-ui-react/src/components/ContextThresholdDialog.tsx`<br>`web/router-ui-react/src/transport/http-client.ts`<br>`web/router-ui-react/src/components/Sidebar.tsx`<br>`web/router-ui-react/src/components/ConversationHeader.tsx`<br>`web/router-ui-react/src/App.tsx`<br>`web/router-ui-react/src/app.css` | `ContextThresholdDialog` with `DialogShell`, global warning/reset inputs, per-model overrides table (add/edit/delete), model dropdown from `/v1/models`, circular icon buttons, click trigger on context bar and sidebar menu. |
| **REQ-8**: Behavioral Functional Tests | Tests | `router-proxy/internal/managed/embedded_context_reset_test.go`<br>`router-proxy/internal/managed/embedded_behavioral_lifecycle_test.go` | Mock LLM integration test: warning at 10%, auto-reset at 20%, per-model override precedence, scratchpad preservation, zero compaction strings. |
| **REQ-9**: Web UI Component Tests | UI Tests | `web/router-ui-react/tests/component/context-threshold-dialog.test.tsx` | Vitest component tests: modal open/render, validation error handling, override addition/deletion, successful save. |

---

## 3. Detailed Implementation Steps

### Task 1: Database Migration
- **File**: `router-proxy/internal/store/migrations/103_context_threshold_settings.sql`
- **Schema Changes**:
  ```sql
  -- Add context threshold configuration columns to managed_settings
  ALTER TABLE managed_settings ADD COLUMN context_warning_percent INTEGER NOT NULL DEFAULT 10;
  ALTER TABLE managed_settings ADD COLUMN context_reset_percent INTEGER NOT NULL DEFAULT 20;
  ALTER TABLE managed_settings ADD COLUMN context_model_overrides TEXT NOT NULL DEFAULT '{}';
  ```
- **Validation**: Ensure migration executes smoothly against existing SQLite database instances in `store.Open()`.

### Task 2: Store Interface and SQLite Methods
- **File**: `router-proxy/internal/store/managed_context_thresholds.go`
- **Data Models**:
  ```go
  type ModelThresholdOverride struct {
      WarningPercent int `json:"warning_percent"`
      ResetPercent   int `json:"reset_percent"`
  }

  type ContextThresholdConfig struct {
      Global         ModelThresholdOverride            `json:"global"`
      ModelOverrides map[string]ModelThresholdOverride `json:"model_overrides,omitempty"`
  }
  ```
- **Default Constants**:
  - `DefaultContextWarningPercent = 10`
  - `DefaultContextResetPercent = 20`
- **Functions**:
  - `GetContextThresholdConfig(ctx context.Context) (ContextThresholdConfig, error)`:
    - Reads from `managed_settings`.
    - Handles defaults if columns are NULL or table is empty.
    - Unmarshals `context_model_overrides` JSON into map.
  - `SetContextThresholdConfig(ctx context.Context, cfg ContextThresholdConfig) error`:
    - Validates configuration boundaries ($1 \le \text{warning} < \text{reset} \le 100$).
    - Serializes overrides JSON.
    - Executes `UPDATE managed_settings SET context_warning_percent=?, context_reset_percent=?, context_model_overrides=?, updated_at=? WHERE singleton_id=1`.
- **Unit Test**: `router-proxy/internal/store/managed_context_thresholds_test.go` verifying read, write, update, and persistence across cold re-opens.

### Task 3: Compaction Mentions Cleanup
- **File 1**: `router-proxy/internal/managed/embedded_compaction_tool.go` (and consider renaming to `embedded_context_tool.go`):
  - Description: `"Read this session's context usage, threshold warnings, and context reset status."`
  - Action error: `"unsupported mecha_context action: only action=status is supported. Context resets are performed via context_reset."`
  - Guidance string:
    ```go
    Guidance: "Save key findings, decisions, and immediate plans to scratchpad before reaching the reset threshold. At the reset threshold, context history is automatically reset to fresh headroom while preserving scratchpad memory and recent conversation. You may also invoke context_reset directly at any time.",
    ```
  - Threshold fields in `ManagedContextStatus`:
    - Populate `WarningThresholdPercent` and `ResetThresholdPercent` dynamically from resolved model thresholds rather than hardcoded 40/50 or 70/80.
- **File 2**: `router-proxy/internal/managed/embedded_session.go`:
  - Update policy denial on `mecha_context`:
    `"mecha_context only supports action=status; context resets are performed via context_reset"`
- **File 3**: `router-proxy/internal/managed/root_control_manager.go`:
  - Retain `COMPACTION_DEPRECATED` for external root-control backward compatibility if needed, or update message to: `"Context compaction is retired; context reset and scratchpad are used instead"`.

### Task 4: Dynamic Threshold Resolution in Managed Engine
- **File**: `router-proxy/internal/managed/embedded_context_reset.go`
- **Functions to add/modify**:
  - `ResolveContextThresholds(model Model, cfg store.ContextThresholdConfig) (warningPct, resetPct int)`:
    - Checks `cfg.ModelOverrides[model.ID]` and `cfg.ModelOverrides[model.Provider+"/"+model.ID]`.
    - If found, returns override `WarningPercent` and `ResetPercent`.
    - Else returns `cfg.Global.WarningPercent` and `cfg.Global.ResetPercent`.
    - If `cfg.Global` values are 0, fall back to defaults (10 and 20).
  - Update `FormatContextResetWarningReminder(currentTokens, contextWindow, resetPct int) string`:
    ```go
    func FormatContextResetWarningReminder(currentTokens, contextWindow, resetPct int) string {
        pct := float64(0)
        if contextWindow > 0 {
            pct = float64(currentTokens) / float64(contextWindow) * 100.0
        }
        return fmt.Sprintf("Context threshold reached: %d / %d tokens (%.1f%%). Approaching automatic reset boundary at %d%%. Please use the scratchpad tool to record your current plan, key findings, and immediate next steps before reset occurs, or call context_reset now.",
            currentTokens, contextWindow, pct, resetPct)
    }
    ```
  - In `checkContextResetAfterTurn`:
    - Load context threshold config: `m.store.GetContextThresholdConfig(ctx)`.
    - Resolve `warningPct, resetPct := ResolveContextThresholds(managedModel, cfg)`.
    - Threshold limits:
      - `resetThresholdTokens := model.ContextWindow * resetPct / 100`
      - `warningThresholdTokens := model.ContextWindow * warningPct / 100`
    - Condition 1: `currentTokens >= resetThresholdTokens`:
      - Trigger `m.ExecuteContextReset(ctx, binding, "threshold_exceeded")`.
    - Condition 2: `currentTokens >= warningThresholdTokens`:
      - If `!lane.warningEmitted` for `binding.Epoch`:
        - Set `lane.warningEmitted = true` and `lane.warningEpoch = binding.Epoch`.
        - Emit warning reminder via `emitContextWarningReminder(ctx, binding, currentTokens, model.ContextWindow, resetPct)`.

### Task 5: HTTP Transport Endpoints
- **File**: `router-proxy/internal/transport/context_threshold_http.go`
- **Endpoints**:
  - `GET /v1/settings/context-thresholds`:
    - Calls `r.store.GetContextThresholdConfig(req.Context())`.
    - Returns JSON response.
  - `PUT /v1/settings/context-thresholds`:
    - Reads payload with 64 KiB limit.
    - Validates:
      - `global.warning_percent` must be in $[1, 99]$.
      - `global.reset_percent` must be in $[1, 100]$.
      - `global.warning_percent < global.reset_percent`.
      - For each entry in `model_overrides`:
        - Key must not be empty.
        - $1 \le \text{warning\_percent} < \text{reset\_percent} \le 100$.
    - Calls `r.store.SetContextThresholdConfig(req.Context(), cfg)`.
    - Returns HTTP 200 with saved config.
- **Routing**:
  - Hook into `router-proxy/internal/transport/http.go` and `router-proxy/internal/transport/managed_http.go`.
- **Unit & Integration Test**: `router-proxy/internal/transport/context_threshold_http_test.go`:
  - Tests GET defaults.
  - Tests PUT valid configuration.
  - Tests PUT invalid configuration (warning >= reset, negative, >100) -> 400 Bad Request.

### Task 6: React Web UI Configuration Dialog
- **File**: `web/router-ui-react/src/components/ContextThresholdDialog.tsx`
  - Uses `DialogShell` (`open={open} onClose={onClose} title="Context Reset & Warning Thresholds"`).
  - Sections:
    1. **Global Thresholds**:
       - Number inputs / sliders for Warning Threshold % and Auto-Reset Threshold %.
       - Live visual helper displaying warning boundary (e.g. 10%) vs auto-reset boundary (e.g. 20%).
    2. **Per-Model Overrides**:
       - Table listing model overrides with Model Name, Warning %, Reset %, and Actions (Edit, Delete).
       - "Add Model Override" sub-form with dropdown populated from `client.models()` and numeric inputs.
       - Compact circular buttons (`.icon-btn-round`) for Add/Remove actions.
    3. **Action Bar**:
       - "Cancel" and "Save Thresholds" buttons.
       - Error banner for validation feedback.
- **Client Library**: `web/router-ui-react/src/transport/http-client.ts`:
  - Add `contextThresholds(): Promise<ContextThresholdConfig>`
  - Add `saveContextThresholds(cfg: ContextThresholdConfig): Promise<ContextThresholdConfig>`
- **UI Integration**:
  - `web/router-ui-react/src/components/Sidebar.tsx`: Add "Context Thresholds" option in the Managed Actions ellipsis menu.
  - `web/router-ui-react/src/components/ConversationHeader.tsx`: Context usage bar is rendered with an onClick handler (or an adjacent settings icon) to open `ContextThresholdDialog`.
  - `web/router-ui-react/src/App.tsx`:
    - Manage dialog state: `dialog === "context-thresholds"`.
    - Render `<ContextThresholdDialog open={dialog === "context-thresholds"} client={session.transport.http} onClose={() => setDialog(undefined)} />`.
- **CSS Styling**: `web/router-ui-react/src/app.css`:
  - Styles for threshold dialog, sliders/inputs, override table, and interactive context bar hover states adhering to single-user dark theme guidelines.

### Task 7: End-to-End Behavioral Functional Tests
- **File**: `router-proxy/internal/managed/embedded_context_reset_test.go`
  - `TestContextThresholds_WarningAndResetBehavioral`:
    - Configure global thresholds to warning 10%, reset 20%.
    - Spawn embedded agent with mock model (e.g. ContextWindow = 10,000 tokens).
    - Step 1: Agent generates 1,200 tokens (12% usage, > 10% warning, < 20% reset).
      - Verify mailbox receives `Context usage threshold warning` reminder.
      - Verify reminder text explicitly references scratchpad, context reset, and the 20% boundary.
      - Verify zero compaction mentions in the message.
    - Step 2: Next turn generates additional tokens (total 2,500 tokens, 25% usage, > 20% reset).
      - Verify automatic fallback context reset triggers: epoch advances from 1 to 2.
      - Verify wake turn prompt delivered (`ContextResetWakePrompt`).
      - Verify scratchpad notes and recent messages are preserved in epoch 2 context.
  - `TestContextThresholds_PerModelOverride`:
    - Configure global warning 10%, reset 20%.
    - Set per-model override for `mock-provider/override-model`: warning 30%, reset 40%.
    - Agent with `override-model` reaches 15% usage -> verify NO warning emitted.
    - Agent reaches 32% usage -> verify warning emitted.
    - Agent reaches 42% usage -> verify auto-reset triggers.
  - `TestCompactionHintsCleanRemoval`:
    - Query `mecha_context` with `action=status` -> verify output contains no compaction references and contains dynamic threshold percentages.
    - Query with invalid action -> verify error contains no compaction references.
    - Audit all prompts and error returns.
- **File**: `web/router-ui-react/tests/component/context-threshold-dialog.test.tsx`:
  - Test loading threshold configuration.
  - Test validation error when `warning >= reset`.
  - Test adding a per-model override and clicking Save.

---

## 4. Verification Checklist & Test Commands

### Verification Checklist
- [ ] Database migration 103 applies cleanly on startup without data loss.
- [ ] `GET /v1/settings/context-thresholds` returns default 10% / 20% thresholds and empty overrides.
- [ ] `PUT /v1/settings/context-thresholds` validates bounds and persists changes.
- [ ] `mecha_context` tool contains zero compaction strings in description, guidance, or error outputs.
- [ ] In-band warning reminder is sent at or above warning threshold (default 10%) exactly once per epoch.
- [ ] Auto-reset executes when tokens exceed reset threshold (default 20%).
- [ ] Per-model overrides correctly take precedence over global settings.
- [ ] React UI dialog allows adjusting global thresholds, adding/editing per-model overrides, and saving.
- [ ] TypeScript type checks and ESLint rules pass with zero errors (`npm run lint:mecha-messaging`, etc.).
- [ ] Go unit and integration tests pass cleanly (`go test ./...`).

### Test Commands
```bash
# 1. Run Go store migration & settings tests
go test -v ./router-proxy/internal/store -run "TestContextThreshold"

# 2. Run Go transport API endpoint tests
go test -v ./router-proxy/internal/transport -run "TestContextThreshold"

# 3. Run Go behavioral lifecycle & context reset tests
go test -v ./router-proxy/internal/managed -run "TestContextThreshold|TestContextReset"

# 4. Run Web UI component tests
cd web/router-ui-react && npm run test tests/component/context-threshold-dialog.test.tsx

# 5. Build router-proxy binary to verify compilation
cd /home/wolfgang/code/mecha-developer/router-proxy && go build -o /tmp/router-proxy-test ./cmd/mailbox-router
```

---

## 5. Rollback Considerations

1. **Database Backward Compatibility**:
   - Migration 103 only adds columns to `managed_settings` with safe defaults (`context_warning_percent=10`, `context_reset_percent=20`, `context_model_overrides='{}'`).
   - If rolled back to an earlier router binary, earlier versions ignore unknown columns in `managed_settings`.
2. **Fallback Safety**:
   - If store queries fail or return corrupt data, `ResolveContextThresholds` falls back to safe hardcoded defaults (10% warning / 20% reset).
   - If context window is 0 or negative, threshold evaluation is disabled to prevent spurious resets or division by zero.
