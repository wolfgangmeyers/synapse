package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedInlineAllowanceLane(t *testing.T) {
	lane := &managedAgentLane{}
	baseTime := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// 1. Initial grant from full pool (3 credits capacity)
	res, err := lane.grantInlineAllowance("bash", 2, baseTime)
	require.NoError(t, err)
	require.Equal(t, "bash", res.ToolName)
	require.Equal(t, 2, res.GrantedCount)
	require.Equal(t, 2, res.Remaining)
	require.Equal(t, 1, res.AvailableWholeCredits) // 3 - 2 = 1 left in pool

	// 2. Grant remaining 1 credit to "edit"
	res, err = lane.grantInlineAllowance("edit", 1, baseTime)
	require.NoError(t, err)
	require.Equal(t, "edit", res.ToolName)
	require.Equal(t, 1, res.GrantedCount)
	require.Equal(t, 1, res.Remaining)
	require.Equal(t, 0, res.AvailableWholeCredits) // 0 left in pool

	// 3. Requesting when pool is empty (0 available) must fail with exact abuse message
	_, err = lane.grantInlineAllowance("write", 1, baseTime)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Insufficient inline tool credits: requested 1 credit(s), but only 0 whole credit(s) are available")
	require.Contains(t, err.Error(), "The pool is exhausted.")
	require.Contains(t, err.Error(), "Credits replenish at 1 credit per minute up to a maximum of 3 credits.")
	require.Contains(t, err.Error(), "ATTENTION: you are probably getting this error because you are ABUSING the inline allowance escape hatch tool.")
	require.Contains(t, err.Error(), "Read that again: YOU SHOULD BE USING SUBAGENTS INSTEAD.")

	// 4. Advance time by 60 seconds (1 minute = 1 replenished credit)
	tPlus60s := baseTime.Add(60 * time.Second)
	res, err = lane.grantInlineAllowance("write", 1, tPlus60s)
	require.NoError(t, err)
	require.Equal(t, 1, res.GrantedCount)
	require.Equal(t, 1, res.Remaining)
	require.Equal(t, 0, res.AvailableWholeCredits)

	// 5. Advance time by 10 minutes (should cap at 3 credits, not grow indefinitely)
	tPlus10m := tPlus60s.Add(10 * time.Minute)
	lane.activeMu.Lock()
	lane.replenishInlinePool(tPlus10m)
	available := int(lane.inlinePoolRemainingMs / InlineToolReplenishmentIntervalMS)
	lane.activeMu.Unlock()
	require.Equal(t, InlineToolPoolCapacity, available)
	require.Equal(t, 3, available)

	// 6. Test consumeInlineAllowance:
	// bash has 2 explicit allowance credits, edit has 1, write has 1.
	// Pool is currently full at 3 credits (from step 5).
	require.Equal(t, 2, lane.getInlineAllowance("bash"))
	require.True(t, lane.consumeInlineAllowance("bash", tPlus10m))
	require.Equal(t, 1, lane.getInlineAllowance("bash"))
	require.True(t, lane.consumeInlineAllowance("bash", tPlus10m))
	require.Equal(t, 0, lane.getInlineAllowance("bash"))

	// bash has 0 explicit credits now, but pool has 3 credits. Direct pool consumption:
	require.True(t, lane.consumeInlineAllowance("bash", tPlus10m), "should consume directly from pool")
	lane.activeMu.Lock()
	require.Equal(t, 2, int(lane.inlinePoolRemainingMs/InlineToolReplenishmentIntervalMS))
	lane.activeMu.Unlock()

	require.True(t, lane.consumeInlineAllowance("bash", tPlus10m), "should consume second credit from pool")
	require.True(t, lane.consumeInlineAllowance("bash", tPlus10m), "should consume third credit from pool")

	// Pool is now empty, and bash has 0 credits.
	require.False(t, lane.consumeInlineAllowance("bash", tPlus10m), "must return false when both allowance credits and pool are exhausted")

	// edit still has 1 granted credit
	require.Equal(t, 1, lane.getInlineAllowance("edit"))
	require.True(t, lane.consumeInlineAllowance("edit", tPlus10m), "can consume from its explicit credit even when pool is 0")
	require.Equal(t, 0, lane.getInlineAllowance("edit"))
	require.False(t, lane.consumeInlineAllowance("edit", tPlus10m))
}

func TestNormalizeInlineToolName(t *testing.T) {
	norm, err := normalizeInlineToolName("bash")
	require.NoError(t, err)
	require.Equal(t, "bash", norm)

	norm, err = normalizeInlineToolName("edit")
	require.NoError(t, err)
	require.Equal(t, "edit", norm)

	norm, err = normalizeInlineToolName("write")
	require.NoError(t, err)
	require.Equal(t, "write", norm)

	norm, err = normalizeInlineToolName("functions.bash")
	require.NoError(t, err)
	require.Equal(t, "bash", norm)

	norm, err = normalizeInlineToolName("functions.edit")
	require.NoError(t, err)
	require.Equal(t, "edit", norm)

	norm, err = normalizeInlineToolName("functions.write")
	require.NoError(t, err)
	require.Equal(t, "write", norm)

	_, err = normalizeInlineToolName("read")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported inline tool name")

	_, err = normalizeInlineToolName("")
	require.Error(t, err)
}

func TestEmbeddedRequestInlineToolAllowanceTool(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	s, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	agentID := "allowance-test-agent"
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Allowance Test Agent",
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	op := store.ManagedOperation{
		OperationID:       "op-launch",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch",
		CanonicalBodyHash: "hash-launch",
		State:             "terminal",
		ResponseStatus:    200,
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, op))

	mgr := &Manager{store: s}
	owner := embeddedOwner{
		binding: store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1"},
	}

	tool := mgr.embeddedRequestInlineToolAllowanceTool(owner)
	require.Equal(t, InlineToolAllowanceToolName, tool.Name)
	require.Equal(t, InlineToolAllowanceToolDesc, tool.Description)
	require.Contains(t, tool.PromptGuidelines, "Use mecha_request_inline_tool_allowance only for genuinely necessary top-level bash/edit/write work.")

	// Test validation: missing params
	res, err := tool.Execute(ctx, "call-1", map[string]any{}, nil)
	require.Error(t, err)

	// Test validation: usageCount out of range
	res, err = tool.Execute(ctx, "call-2", map[string]any{"toolName": "bash", "usageCount": 4}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "usageCount must be an integer from 1 through 3")

	// Test successful grant
	res, err = tool.Execute(ctx, "call-3", map[string]any{"toolName": "bash", "usageCount": 2}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	require.Equal(t, "Granted 2 temporary inline use(s) for bash. This allowance is an exception to the rule; subagents remain mandatory by default.", res.Content[0].(ai.TextContent).Text)

	// Check lane credits
	lane := mgr.laneFor(agentID)
	require.Equal(t, 2, lane.getInlineAllowance("bash"))

	// Test native subagent exemption
	subagentID := "subagent-child-1"
	subagentRecord := store.ManagedAgentRecord{
		AgentID:          subagentID,
		Name:             "Subagent Child",
		ParentAgentID:    agentID,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, subagentRecord, store.ManagedOperation{
		OperationID:       "op-launch-sub",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-sub",
		CanonicalBodyHash: "hash-launch-sub",
		State:             "terminal",
		ResponseStatus:    200,
	}))

	subOwner := embeddedOwner{
		binding: store.EmbeddedBinding{AgentID: subagentID, SessionID: "session-sub"},
	}
	subTool := mgr.embeddedRequestInlineToolAllowanceTool(subOwner)
	subRes, subErr := subTool.Execute(ctx, "call-sub", map[string]any{"toolName": "bash", "usageCount": 1}, nil)
	require.NoError(t, subErr)
	require.Equal(t, "Native subagents are exempt from the inline tool guard; no parent allowance was changed.", subRes.Content[0].(ai.TextContent).Text)
}

func TestInlineToolExecutionGate(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	s, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	agentID := "gate-test-agent"
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Gate Test Agent",
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, store.ManagedOperation{
		OperationID:       "op-launch-gate",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-gate",
		CanonicalBodyHash: "hash-launch-gate",
		State:             "terminal",
		ResponseStatus:    200,
	}))

	mgr := &Manager{store: s}
	lane := mgr.laneFor(agentID)

	gateCheck := func(toolName string) *agent.BeforeToolCallResult {
		normalizedToolName, isInlineTool := normalizeInlineActionTool(toolName)
		if isInlineTool {
			if mgr.IsInlineToolsAllowed(ctx, agentID) {
				return nil
			}
			lookupTools := []agent.AgentTool{}
			if lane != nil {
				if dynTools := lane.getActiveTools(); len(dynTools) > 0 {
					lookupTools = dynTools
				} else if dynBase := lane.getBaseTools(); len(dynBase) > 0 {
					lookupTools = dynBase
				}
			}
			hasAllowanceTool := false
			for _, t := range lookupTools {
				if t.Name == InlineToolAllowanceToolName {
					hasAllowanceTool = true
					break
				}
			}
			if !hasAllowanceTool {
				return nil
			}

			if lane != nil && lane.consumeInlineAllowance(normalizedToolName, clock.Now()) {
				return nil
			}
			return &agent.BeforeToolCallResult{
				Block:  true,
				Reason: InlineToolBlockReason,
			}
		}
		return nil
	}

	// Set lane tools to include mecha_request_inline_tool_allowance
	lane.setTools([]agent.AgentTool{
		{Name: "bash"},
		{Name: "edit"},
		{Name: "write"},
		{Name: "read"},
		{Name: InlineToolAllowanceToolName},
	}, []agent.AgentTool{
		{Name: "bash"},
		{Name: "edit"},
		{Name: "write"},
		{Name: "read"},
		{Name: InlineToolAllowanceToolName},
	})

	// Exhaust the remaining pool so that allowance credit consumption can be tested in isolation
	lane.activeMu.Lock()
	lane.inlinePoolUpdatedAt = clock.Now()
	lane.inlinePoolRemainingMs = 0
	lane.activeMu.Unlock()

	// 1. allow_inline_tools == false and 0 credits: bash is BLOCKED
	res := gateCheck("bash")
	require.NotNil(t, res)
	require.True(t, res.Block)
	require.Equal(t, InlineToolBlockReason, res.Reason)

	// edit and write are also blocked
	resEdit := gateCheck("edit")
	require.NotNil(t, resEdit)
	require.True(t, resEdit.Block)
	resWrite := gateCheck("write")
	require.NotNil(t, resWrite)
	require.True(t, resWrite.Block)

	// Non-inline tools (e.g. read, custom) are NOT blocked
	resRead := gateCheck("read")
	require.Nil(t, resRead)

	// 2. Grant 2 credits for bash
	// Replenish pool first to allow granting
	lane.activeMu.Lock()
	lane.inlinePoolRemainingMs = InlineToolPoolCapacity * InlineToolReplenishmentIntervalMS
	lane.inlinePoolUpdatedAt = clock.Now()
	lane.activeMu.Unlock()

	_, err = lane.grantInlineAllowance("bash", 2, clock.Now())
	require.NoError(t, err)

	// To test that credits exhaust and eventually block when pool is also empty,
	// empty the pool remainder.
	lane.activeMu.Lock()
	lane.inlinePoolRemainingMs = 0
	lane.inlinePoolUpdatedAt = clock.Now()
	lane.activeMu.Unlock()

	// First bash call consumes 1 credit and succeeds
	res1 := gateCheck("bash")
	require.Nil(t, res1, "first call should be allowed")
	require.Equal(t, 1, lane.getInlineAllowance("bash"))

	// Second bash call consumes the last credit and succeeds
	res2 := gateCheck("bash")
	require.Nil(t, res2, "second call should be allowed")
	require.Equal(t, 0, lane.getInlineAllowance("bash"))

	// Third bash call is blocked because credits and pool are exhausted
	res3 := gateCheck("bash")
	require.NotNil(t, res3)
	require.True(t, res3.Block)

	// 3. Dynamic Override Toggle: set allow_inline_tools = true
	err = s.SetManagedAgentAllowInlineTools(ctx, agentID, true)
	require.NoError(t, err)

	// Now bash, edit, write execute freely without credits
	require.Nil(t, gateCheck("bash"), "bash should be allowed when allow_inline_tools == true")
	require.Nil(t, gateCheck("edit"), "edit should be allowed when allow_inline_tools == true")
	require.Nil(t, gateCheck("write"), "write should be allowed when allow_inline_tools == true")

	// 4. Toggle back to allow_inline_tools = false
	err = s.SetManagedAgentAllowInlineTools(ctx, agentID, false)
	require.NoError(t, err)

	// Now blocked again since credits are 0
	resBlockedAgain := gateCheck("bash")
	require.NotNil(t, resBlockedAgain)
	require.True(t, resBlockedAgain.Block)

	// 5. When mecha_request_inline_tool_allowance is NOT present in available tools,
	// bash is NOT blocked even if credits are 0 and allow_inline_tools == false.
	lane.setTools([]agent.AgentTool{
		{Name: "bash"},
		{Name: "read"},
	}, []agent.AgentTool{
		{Name: "bash"},
		{Name: "read"},
	})
	resNoAllowanceTool := gateCheck("bash")
	require.Nil(t, resNoAllowanceTool, "bash must NOT be blocked when mecha_request_inline_tool_allowance is not available to the agent")
}

func TestUnrestrictedInlineToolsReproduction(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test-unrestricted.db")
	s, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer s.Close()

	agentID := "context-helper-repro"
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Launch agent with allow_inline_tools = false in SQLite
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		AcceptingPrompts: true,
		SessionEpoch:     1,
		ManifestRevision: 1,
		ManifestHash:     strings.Repeat("a", 64),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, store.ManagedOperation{
		OperationID:       "op-launch-repro",
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-repro",
		CanonicalBodyHash: "hash-launch-repro",
		State:             "terminal",
		ResponseStatus:    200,
	}))

	// Set desired policy to have inline.enabled = true (exactly what happened to Context-helper when configured via UI dialog)
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()
	policyJSON := []byte(`{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{},"enabled":true},"recall":{"content":""},"settled":{"enabled":false}}`)
	h := sha256.Sum256(policyJSON)
	policyHash := hex.EncodeToString(h[:])
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_readiness_sources SET desired_policy_canonical=?, desired_policy_sha256=? WHERE agent_id=?`, policyJSON, policyHash, agentID)
	require.NoError(t, err)

	mgr := &Manager{store: s}
	defaultManagerInstance = mgr
	defer func() { defaultManagerInstance = nil }()

	// 1. Verify ManagedInlineToolsEnabled reports true from desired policy
	enabled, err := mgr.ManagedInlineToolsEnabled(ctx, agentID)
	require.NoError(t, err)
	require.True(t, enabled, "ManagedInlineToolsEnabled must return true when desired policy has inline.enabled=true")

	// 2. Verify IsInlineToolsAllowed reports true and heals allow_inline_tools in SQLite
	require.True(t, mgr.IsInlineToolsAllowed(ctx, agentID), "IsInlineToolsAllowed must return true")

	// Check DB row was healed
	reloaded, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.True(t, reloaded.AllowInlineTools, "allow_inline_tools must be healed to true in database")

	// 3. Verify PublicAgent projects allow_inline_tools = true
	publicProjection := PublicAgent(reloaded)
	require.Equal(t, true, publicProjection["allow_inline_tools"], "PublicAgent must project allow_inline_tools as true")

	// 4. Verify gateCheck allows bash, edit, write unrestricted without credits
	lane := mgr.laneFor(agentID)
	gateCheck := func(toolName string) *agent.BeforeToolCallResult {
		normalizedToolName, isInlineTool := normalizeInlineActionTool(toolName)
		if isInlineTool {
			if mgr.IsInlineToolsAllowed(ctx, agentID) {
				return nil
			}
			if lane != nil && lane.consumeInlineAllowance(normalizedToolName, time.Now()) {
				return nil
			}
			return &agent.BeforeToolCallResult{
				Block:  true,
				Reason: InlineToolBlockReason,
			}
		}
		return nil
	}

	for i := 0; i < 20; i++ {
		require.Nil(t, gateCheck("bash"), "bash call %d must be allowed unrestricted", i)
		require.Nil(t, gateCheck("edit"), "edit call %d must be allowed unrestricted", i)
		require.Nil(t, gateCheck("write"), "write call %d must be allowed unrestricted", i)
	}

	// 5. Verify mecha_request_inline_tool_allowance succeeds without error when unrestricted
	owner := embeddedOwner{binding: store.EmbeddedBinding{AgentID: agentID, SessionID: "sess-1", Epoch: 1, Generation: "gen-1"}}
	allowanceTool := mgr.embeddedRequestInlineToolAllowanceTool(owner)
	res, err := allowanceTool.Execute(ctx, "call_1", map[string]any{"toolName": "bash", "usageCount": 3}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "unrestricted")
}
