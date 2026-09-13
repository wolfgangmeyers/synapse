package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const managedRootRecoveryRetention = 24 * time.Hour

// ManagedLaunchRecipe contains the fixed, non-secret inputs needed to
// reproduce a managed root. Per-runtime values are supplied at start time.
type ManagedLaunchRecipe struct {
	PiExecutable         string   `json:"pi_executable"`
	SessionRoot          string   `json:"session_root"`
	Cwd                  string   `json:"cwd"`
	Provider             string   `json:"provider"`
	ModelID              string   `json:"model_id"`
	MechaRPCExtension    string   `json:"mecha_rpc_extension"`
	PiSubagentsExtension string   `json:"pi_subagents_extension"`
	CursorExtension      string   `json:"cursor_extension"`
	TestOnlyExtensions   []string `json:"test_only_extensions"`
	Environment          []string `json:"environment"`
	SHA256               string   `json:"sha256"`
	CreatedAt            string   `json:"created_at,omitempty"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
}

// ManagedRootRecoveryAction is the durable owner of one cold-start attempt.
// ActionID is derived from the complete old fence, so replaying a claim cannot
// allocate a second target generation.
type ManagedRootRecoveryAction struct {
	ActionID, AgentID, OldPiSessionID, OldProcessStartMarker, OldProcessIncarnation, OldRuntimeGeneration string
	OldSessionEpoch, OldProcessPID, OldManifestRevision                                                   int
	OldManifestHash, RecipeSHA256, TargetRuntimeGeneration                                                string
	TargetProcessPID                                                                                      int
	TargetProcessStartMarker, TargetProcessIncarnation, Stage, FailureCode, FailureMessage                string
	UnexpectedExitCount                                                                                   int
	CreatedAt, UpdatedAt, ExpiresAt                                                                       string
}

// IsManagedLaunchEnvironmentKey identifies the fixed environment names that
// are safe to retain in a launch recipe. Values are still bounded and hashed.
func IsManagedLaunchEnvironmentKey(key string) bool {
	switch key {
	case "HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "PI_CODING_AGENT_DIR", "MECHA_MANAGED_SESSION_ROOT", "MECHA_MANAGED_ACTIVE_ONLY":
		return true
	default:
		return strings.HasPrefix(key, "MECHA_TEST_")
	}
}

func ManagedLaunchRecipeDigest(recipe ManagedLaunchRecipe) (ManagedLaunchRecipe, error) {
	recipe.SHA256 = ""
	recipe.CreatedAt, recipe.UpdatedAt = "", ""
	recipe.TestOnlyExtensions = append([]string(nil), recipe.TestOnlyExtensions...)
	recipe.Environment = append([]string(nil), recipe.Environment...)
	if recipe.TestOnlyExtensions == nil {
		recipe.TestOnlyExtensions = []string{}
	}
	if recipe.Environment == nil {
		recipe.Environment = []string{}
	}
	encoded, err := json.Marshal(recipe)
	if err != nil {
		return ManagedLaunchRecipe{}, err
	}
	sum := sha256.Sum256(encoded)
	recipe.SHA256 = hex.EncodeToString(sum[:])
	return recipe, nil
}

func ValidateManagedLaunchRecipe(recipe ManagedLaunchRecipe) error {
	if recipe.PiExecutable == "" || recipe.SessionRoot == "" || recipe.Cwd == "" || recipe.Provider == "" || recipe.ModelID == "" || recipe.MechaRPCExtension == "" || recipe.PiSubagentsExtension == "" {
		return errors.New("managed launch recipe is incomplete")
	}
	for _, value := range []string{recipe.PiExecutable, recipe.SessionRoot, recipe.Cwd, recipe.Provider, recipe.ModelID, recipe.MechaRPCExtension, recipe.PiSubagentsExtension} {
		if !validRecipeValue(value, 4096) {
			return errors.New("managed launch recipe contains an invalid value")
		}
	}
	if recipe.CursorExtension != "" && !validRecipeValue(recipe.CursorExtension, 4096) {
		return errors.New("managed launch recipe contains an invalid value")
	}
	if len(recipe.Environment) > 128 || len(recipe.TestOnlyExtensions) > 32 || len(recipe.SHA256) != 64 || !isLowerHex(recipe.SHA256) {
		return errors.New("managed launch recipe is incomplete")
	}
	seenKeys := make(map[string]struct{}, len(recipe.Environment))
	for _, value := range recipe.Environment {
		key, _, ok := strings.Cut(value, "=")
		if !ok || !IsManagedLaunchEnvironmentKey(key) || !validRecipeValue(value, 4096) {
			return errors.New("managed launch recipe contains an invalid environment")
		}
		if _, exists := seenKeys[key]; exists {
			return errors.New("managed launch recipe contains duplicate environment")
		}
		seenKeys[key] = struct{}{}
	}
	for _, value := range recipe.TestOnlyExtensions {
		if !validRecipeValue(value, 4096) {
			return errors.New("managed launch recipe contains an invalid extension")
		}
	}
	canonical, err := ManagedLaunchRecipeDigest(recipe)
	if err != nil || canonical.SHA256 != recipe.SHA256 {
		return errors.New("managed launch recipe hash is invalid")
	}
	return nil
}

func validRecipeValue(value string, maxBytes int) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= maxBytes
}

func isLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (s *Store) putManagedLaunchRecipeTx(ctx context.Context, recipe ManagedLaunchRecipe, agentID, now string) error {
	if agentID == "" {
		return errors.New("managed launch recipe agent is required")
	}
	if err := ValidateManagedLaunchRecipe(recipe); err != nil {
		return err
	}
	if recipe.CreatedAt == "" {
		recipe.CreatedAt = now
	}
	if recipe.UpdatedAt == "" {
		recipe.UpdatedAt = now
	}
	testJSON, err := json.Marshal(recipe.TestOnlyExtensions)
	if err != nil {
		return err
	}
	envJSON, err := json.Marshal(recipe.Environment)
	if err != nil {
		return err
	}
	var existing ManagedLaunchRecipe
	var existingTestJSON, existingEnvJSON string
	err = s.db.QueryRowContext(ctx, `SELECT pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at FROM managed_launch_recipes WHERE agent_id=?`, agentID).Scan(&existing.PiExecutable, &existing.SessionRoot, &existing.Cwd, &existing.Provider, &existing.ModelID, &existing.MechaRPCExtension, &existing.PiSubagentsExtension, &existing.CursorExtension, &existingTestJSON, &existingEnvJSON, &existing.SHA256, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		if existingTestJSON == string(testJSON) && existingEnvJSON == string(envJSON) && existing.PiExecutable == recipe.PiExecutable && existing.SessionRoot == recipe.SessionRoot && existing.Cwd == recipe.Cwd && existing.Provider == recipe.Provider && existing.ModelID == recipe.ModelID && existing.MechaRPCExtension == recipe.MechaRPCExtension && existing.PiSubagentsExtension == recipe.PiSubagentsExtension && existing.CursorExtension == recipe.CursorExtension && existing.SHA256 == recipe.SHA256 {
			return nil
		}
		return errors.New("managed launch recipe is immutable")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_launch_recipes(agent_id,pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentID, recipe.PiExecutable, recipe.SessionRoot, recipe.Cwd, recipe.Provider, recipe.ModelID, recipe.MechaRPCExtension, recipe.PiSubagentsExtension, recipe.CursorExtension, string(testJSON), string(envJSON), recipe.SHA256, recipe.CreatedAt, recipe.UpdatedAt)
	return err
}

func (s *Store) ManagedLaunchRecipe(ctx context.Context, agentID string) (*ManagedLaunchRecipe, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return loadManagedLaunchRecipeQuery(ctx, s.db, agentID)
}

func loadManagedLaunchRecipeQuery(ctx context.Context, db *sql.DB, agentID string) (*ManagedLaunchRecipe, error) {
	var recipe ManagedLaunchRecipe
	var testJSON, envJSON string
	err := db.QueryRowContext(ctx, `SELECT pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at FROM managed_launch_recipes WHERE agent_id=?`, agentID).Scan(&recipe.PiExecutable, &recipe.SessionRoot, &recipe.Cwd, &recipe.Provider, &recipe.ModelID, &recipe.MechaRPCExtension, &recipe.PiSubagentsExtension, &recipe.CursorExtension, &testJSON, &envJSON, &recipe.SHA256, &recipe.CreatedAt, &recipe.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(testJSON), &recipe.TestOnlyExtensions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(envJSON), &recipe.Environment); err != nil {
		return nil, err
	}
	if err := ValidateManagedLaunchRecipe(recipe); err != nil {
		return nil, err
	}
	return &recipe, nil
}

func (s *Store) PutManagedLaunchRecipe(ctx context.Context, agentID string, recipe ManagedLaunchRecipe) error {
	return s.runImmediate(ctx, func() error { return s.putManagedLaunchRecipeTx(ctx, recipe, agentID, formatTimestamp(s.clock.Now())) })
}

func managedRootRecoveryActionID(agentID string, old ManagedRuntimeFence) string {
	values := []string{agentID, old.PiSessionID, fmt.Sprint(old.SessionEpoch), fmt.Sprint(old.ProcessPID), old.ProcessStartMarker, old.ProcessIncarnation, old.RuntimeGeneration, fmt.Sprint(old.ManifestRevision), old.ManifestHash}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return "mrr_" + hex.EncodeToString(sum[:])
}

func (s *Store) ClaimManagedRootRecovery(ctx context.Context, agentID string, old ManagedRuntimeFence, recipeSHA, targetGeneration string) (*ManagedRootRecoveryAction, bool, error) {
	if !validManagedRuntimeFence(old) || agentID == "" || old.AgentID != agentID || !isLowerHex(recipeSHA) || targetGeneration == "" || !utf8.ValidString(targetGeneration) || len([]byte(targetGeneration)) > 128 {
		return nil, false, errors.New("managed root recovery claim is invalid")
	}
	actionID := managedRootRecoveryActionID(agentID, old)
	var action *ManagedRootRecoveryAction
	created := false
	err := s.runImmediate(ctx, func() error {
		existing, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err == nil {
			if !sameManagedRootRecoveryFence(existing, old) || existing.RecipeSHA256 != recipeSHA {
				return errors.New("managed root recovery fence conflicts")
			}
			if existing.TargetRuntimeGeneration != targetGeneration {
				return errors.New("managed root recovery target is immutable")
			}
			action = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var activeID string
		err = s.db.QueryRowContext(ctx, `SELECT action_id FROM managed_root_recovery_actions WHERE agent_id=? AND stage NOT IN ('ready','failed') LIMIT 1`, agentID).Scan(&activeID)
		if err == nil {
			return errors.New("managed root recovery is already active")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_root_recovery_actions(action_id,agent_id,old_pi_session_id,old_session_epoch,old_process_pid,old_process_start_marker,old_process_incarnation,old_runtime_generation,old_manifest_revision,old_manifest_hash,recipe_sha256,target_runtime_generation,stage,unexpected_exit_count,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'claimed',0,?,?)`, actionID, agentID, old.PiSessionID, old.SessionEpoch, old.ProcessPID, old.ProcessStartMarker, old.ProcessIncarnation, old.RuntimeGeneration, old.ManifestRevision, old.ManifestHash, recipeSHA, targetGeneration, now, now)
		if err != nil {
			return err
		}
		created = true
		action, err = managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		return err
	})
	return action, created, err
}

func sameManagedRootRecoveryFence(action *ManagedRootRecoveryAction, fence ManagedRuntimeFence) bool {
	return action != nil && action.AgentID == fence.AgentID && action.OldPiSessionID == fence.PiSessionID && action.OldSessionEpoch == int(fence.SessionEpoch) && action.OldProcessPID == fence.ProcessPID && action.OldProcessStartMarker == fence.ProcessStartMarker && action.OldProcessIncarnation == fence.ProcessIncarnation && action.OldRuntimeGeneration == fence.RuntimeGeneration && action.OldManifestRevision == fence.ManifestRevision && action.OldManifestHash == fence.ManifestHash
}

func rootRecoveryTransitionAllowed(from, to string) bool {
	if from == "claimed" && to == "reaping" || from == "reaping" && to == "stopped" || from == "stopped" && to == "starting" || from == "starting" && to == "ready" {
		return true
	}
	return false
}

func (s *Store) AdvanceManagedRootRecovery(ctx context.Context, actionID, from, to string) error {
	if actionID == "" || !rootRecoveryTransitionAllowed(from, to) {
		return errors.New("managed root recovery transition is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{to, now, actionID, from}
		where := `action_id=? AND stage=? AND ` + managedRootRecoveryFenceWhere(action)
		args = append(args, managedRootRecoveryFenceArgs(action)...)
		var result sql.Result
		if to == "ready" {
			args = []any{to, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), actionID, from}
			args = append(args, managedRootRecoveryFenceArgs(action)...)
			where = `action_id=? AND stage=? AND ` + managedRootRecoveryFenceWhere(action)
			result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage=?,updated_at=?,expires_at=? WHERE `+where, args...)
		} else {
			result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage=?,updated_at=? WHERE `+where, args...)
		}
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("managed root recovery action is stale")
		}
		return nil
	})
}

func (s *Store) RecordManagedRootRecoveryUnexpectedExit(ctx context.Context, actionID string) error {
	if actionID == "" {
		return errors.New("managed root recovery action is required")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET unexpected_exit_count=MIN(unexpected_exit_count+1,5),updated_at=? WHERE action_id=? AND stage IN ('claimed','reaping') AND `+managedRootRecoveryFenceWhere(action), append([]any{formatTimestamp(s.clock.Now()), actionID}, managedRootRecoveryFenceArgs(action)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("managed root recovery action is stale")
		}
		return nil
	})
}

// ManagedProcessIdentity avoids making store depend on the managed package.
type ManagedProcessIdentity struct {
	PID                      int
	StartMarker, Incarnation string
}

// ManagedRootRecoveryTargetStaleError reports the expected ownership race at
// the one external target-start boundary. Callers may safely abandon their
// start attempt when this error is returned; database errors must propagate.
type ManagedRootRecoveryTargetStaleError struct{ ActionID string }

func (e *ManagedRootRecoveryTargetStaleError) Error() string {
	return "managed root recovery target reservation is stale"
}
func (e *ManagedRootRecoveryTargetStaleError) Code() string { return "stale" }

// BeginManagedRootRecoveryTarget reserves the one external start boundary.
// The starting stage with no target identity means a process may be created by
// the current owner; a later router must fail closed instead of starting twice.
func (s *Store) BeginManagedRootRecoveryTarget(ctx context.Context, actionID, targetGeneration string) error {
	if actionID == "" || targetGeneration == "" {
		return errors.New("managed root recovery target reservation is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.TargetRuntimeGeneration != targetGeneration {
			return errors.New("managed root recovery target is immutable")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='starting',updated_at=? WHERE action_id=? AND target_runtime_generation=? AND stage='stopped' AND target_process_pid IS NULL AND `+managedRootRecoveryFenceWhere(action), append([]any{formatTimestamp(s.clock.Now()), actionID, targetGeneration}, managedRootRecoveryFenceArgs(action)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRootRecoveryTargetStaleError{ActionID: actionID}
		}
		return nil
	})
}

func (s *Store) InstallManagedRootRecoveryTarget(ctx context.Context, actionID string, identity ManagedProcessIdentity, targetGeneration string) error {
	if actionID == "" || identity.PID <= 0 || identity.StartMarker == "" || identity.Incarnation == "" || targetGeneration == "" {
		return errors.New("managed root recovery target identity is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.TargetRuntimeGeneration != targetGeneration {
			return errors.New("managed root recovery target is immutable")
		}
		args := []any{identity.PID, identity.StartMarker, identity.Incarnation, formatTimestamp(s.clock.Now()), actionID, targetGeneration}
		args = append(args, managedRootRecoveryFenceArgs(action)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,stage='starting',updated_at=? WHERE action_id=? AND target_runtime_generation=? AND stage='starting' AND target_process_pid IS NULL AND `+managedRootRecoveryFenceWhere(action), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("managed root recovery target is stale")
		}
		return nil
	})
}

func (s *Store) InstallManagedRootRecoveryFence(ctx context.Context, old, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(old) || !validManagedRuntimeFence(target) || old.AgentID != target.AgentID || old.PiSessionID != target.PiSessionID || old.SessionEpoch != target.SessionEpoch || old.ManifestRevision != target.ManifestRevision || old.ManifestHash != target.ManifestHash || old.RuntimeGeneration == target.RuntimeGeneration || old.ProcessIncarnation == target.ProcessIncarnation {
		return errors.New("managed root recovery fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		args := []any{target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, formatTimestamp(s.clock.Now()), target.AgentID}
		args = append(args, managedFenceValues(old)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,lifecycle_state='starting',activity_state='starting',accepting_prompts=0,slot_held=1,updated_at=? WHERE agent_id=? AND desired_state='running' AND lifecycle_state='running' AND `+managedFenceWhere(""), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedReadinessSourceStaleError{AgentID: target.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='starting',updated_at=? WHERE agent_id=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=? AND stage='starting' AND target_runtime_generation=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=?`, formatTimestamp(s.clock.Now()), old.AgentID, old.PiSessionID, old.SessionEpoch, old.ProcessPID, old.ProcessStartMarker, old.ProcessIncarnation, old.RuntimeGeneration, old.ManifestRevision, old.ManifestHash, target.RuntimeGeneration, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation)
		if err != nil {
			return err
		}
		if changed, err = result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("managed root recovery action is stale")
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, target); err != nil {
			return err
		}
		return nil
	})
}

// SupersedeManagedRootRecovery closes a retained ready action when its target
// belongs to a router instance that no longer has the native RPC binding.
// Retaining the row as failed preserves diagnostics while freeing the active
// recovery owner for a fresh fence and generation.
func (s *Store) SupersedeManagedRootRecovery(ctx context.Context, actionID string) error {
	if actionID == "" {
		return errors.New("managed root recovery action is required")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Stage == "failed" {
			return nil
		}
		if action.Stage != "ready" {
			return errors.New("managed root recovery action is stale")
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='failed',failure_code='RECOVERY_SUPERSEDED',failure_message='ready target belonged to a prior router',updated_at=?,expires_at=? WHERE action_id=? AND stage='ready'`, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), actionID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("managed root recovery action is stale")
		}
		return nil
	})
}

func (s *Store) CompleteManagedRootRecovery(ctx context.Context, agentID string, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(target) || agentID == "" || target.AgentID != agentID {
		return errors.New("managed root recovery completion is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='ready',updated_at=?,expires_at=?,failure_code=NULL,failure_message=NULL WHERE agent_id=? AND target_runtime_generation=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=? AND stage='starting'`, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), agentID, target.RuntimeGeneration, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("managed root recovery target is stale")
		}
		return nil
	})
}

// CompleteManagedRootRecoveryReady is the recovery-only admission boundary.
// It publishes the new runtime, opens prompt admission, activates recipients,
// and retains the recovery claim in one transaction after all source receipts.
func (s *Store) CompleteManagedRootRecoveryReady(ctx context.Context, agentID string, target ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(target) || agentID == "" || target.AgentID != agentID {
		return errors.New("managed root recovery completion is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, managedRootRecoveryActionID(agentID, ManagedRuntimeFence{
			AgentID: agentID, PiSessionID: target.PiSessionID, SessionEpoch: target.SessionEpoch,
			ProcessPID: target.ProcessPID, ProcessStartMarker: target.ProcessStartMarker,
			ProcessIncarnation: target.ProcessIncarnation, RuntimeGeneration: target.RuntimeGeneration,
			ManifestRevision: target.ManifestRevision, ManifestHash: target.ManifestHash,
		}))
		if err != nil {
			// The action ID is based on the old fence, so locate the one active
			// target claim by its immutable target identity.
			action, err = managedRootRecoveryActionQuery(ctx, s.db, `WHERE agent_id=? AND target_runtime_generation=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=? AND stage='starting' LIMIT 1`, agentID, target.RuntimeGeneration, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation)
		}
		if err != nil || action.Stage != "starting" {
			return errors.New("managed root recovery target is stale")
		}
		if err := s.managedReadinessSourcesConfirmedForFenceTx(ctx, target); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,started_at=COALESCE(started_at,?),updated_at=? WHERE desired_state='running' AND lifecycle_state='starting' AND accepting_prompts=0 AND slot_held=1 AND `+managedFenceWhere(""), append([]any{now, now}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedReadinessSourceStaleError{AgentID: agentID}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE recipient_sources SET state='active',updated_at=? WHERE source_kind='managed' AND owner_identity=? AND state='reserved'`, now, agentID); err != nil {
			return err
		}
		if err := s.reconcileMessageTargetsTx(ctx); err != nil {
			return err
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='ready',updated_at=?,expires_at=?,failure_code=NULL,failure_message=NULL WHERE action_id=? AND stage='starting' AND target_runtime_generation=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=?`, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), action.ActionID, target.RuntimeGeneration, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("managed root recovery target is stale")
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) ManagedRootRecoveryAction(ctx context.Context, agentID string) (*ManagedRootRecoveryAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedRootRecoveryActionQuery(ctx, s.db, `WHERE agent_id=? ORDER BY updated_at DESC, action_id DESC LIMIT 1`, agentID)
}

func (s *Store) ManagedRootRecoveryActionByID(ctx context.Context, actionID string) (*ManagedRootRecoveryAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
}

func (s *Store) FailManagedRootRecovery(ctx context.Context, actionID, code, message string) error {
	if actionID == "" || code == "" || len([]byte(code)) > 128 || message == "" {
		return errors.New("managed root recovery failure is invalid")
	}
	message = RedactedManagedDiagnostic(message)
	return s.runImmediate(ctx, func() error {
		action, err := managedRootRecoveryActionForIDTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{code, message, now, formatTimestamp(s.clock.Now().Add(managedRootRecoveryRetention)), actionID}
		args = append(args, managedRootRecoveryFenceArgs(action)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_root_recovery_actions SET stage='failed',failure_code=?,failure_message=?,updated_at=?,expires_at=? WHERE action_id=? AND stage NOT IN ('ready','failed') AND `+managedRootRecoveryFenceWhere(action), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("managed root recovery action is stale")
		}
		return nil
	})
}

// PruneManagedRootRecovery removes only expired terminal rows. Active actions
// remain available for restart recovery and completed actions remain retained
// for diagnostics for the same bounded period as other managed operations.
// InterruptManagedControlRequests closes only nonterminal root-control work
// owned by an exact lost runtime. A replacement cannot safely resume it with a
// new fence because its handler may have crossed an external side-effect edge.
func (s *Store) InterruptManagedControlRequests(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed control interruption fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		args := append([]any{now}, managedFenceValues(fence)...)
		_, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='failed',terminal_at=?,error_code='PARENT_PROCESS_LOST',error_detail=NULL,retryable=0,next_append_at=NULL WHERE state NOT IN ('delivered','failed','cancelled') AND `+managedControlFenceWhere(), args...)
		return err
	})
}

// InterruptManagedGoalEvaluation abandons evaluator and snooze state that was
// claimed by an exact lost runtime. The wakeup row remains fence-bound, so a
// replacement cannot deliver it as new work.
func (s *Store) InterruptManagedGoalEvaluation(ctx context.Context, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return errors.New("managed goal interruption fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		args := []any{now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET evaluator_state='idle',evaluator_lease_until=NULL,evaluator_reason='evaluation interrupted',next_wakeup_at=NULL,snoozed_until=NULL,snooze_pi_session_id=NULL,snooze_session_epoch=NULL,snooze_process_pid=NULL,snooze_process_start_marker=NULL,snooze_process_incarnation=NULL,snooze_runtime_generation=NULL,updated_at=? WHERE agent_id=? AND ((evaluator_state='evaluating' AND evaluator_pi_session_id=? AND evaluator_session_epoch=? AND evaluator_process_pid=? AND evaluator_process_start_marker=? AND evaluator_process_incarnation=? AND evaluator_runtime_generation=?) OR (evaluator_state='snoozed' AND snooze_pi_session_id=? AND snooze_session_epoch=? AND snooze_process_pid=? AND snooze_process_start_marker=? AND snooze_process_incarnation=? AND snooze_runtime_generation=?))`, args...)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM managed_goal_activity WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=?`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration)
		return err
	})
}

func (s *Store) PruneManagedRootRecovery(ctx context.Context) (int, error) {
	var removed int
	err := s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `DELETE FROM managed_root_recovery_actions WHERE stage IN ('ready','failed') AND expires_at IS NOT NULL AND expires_at <= ?`, formatTimestamp(s.clock.Now()))
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		removed = int(count)
		return err
	})
	return removed, err
}

const managedRootRecoveryActionColumns = `action_id,agent_id,old_pi_session_id,old_session_epoch,old_process_pid,old_process_start_marker,old_process_incarnation,old_runtime_generation,old_manifest_revision,old_manifest_hash,recipe_sha256,target_runtime_generation,target_process_pid,target_process_start_marker,target_process_incarnation,stage,unexpected_exit_count,failure_code,failure_message,created_at,updated_at,expires_at`

func managedRootRecoveryActionForIDTx(ctx context.Context, db *sql.DB, actionID string) (*ManagedRootRecoveryAction, error) {
	return managedRootRecoveryActionQuery(ctx, db, `WHERE action_id=?`, actionID)
}

func managedRootRecoveryActionQuery(ctx context.Context, db *sql.DB, suffix string, args ...any) (*ManagedRootRecoveryAction, error) {
	var action ManagedRootRecoveryAction
	var targetPID, unexpected sql.NullInt64
	var targetMarker, targetIncarnation, failureCode, failureMessage, expires sql.NullString
	err := db.QueryRowContext(ctx, `SELECT `+managedRootRecoveryActionColumns+` FROM managed_root_recovery_actions `+suffix, args...).Scan(&action.ActionID, &action.AgentID, &action.OldPiSessionID, &action.OldSessionEpoch, &action.OldProcessPID, &action.OldProcessStartMarker, &action.OldProcessIncarnation, &action.OldRuntimeGeneration, &action.OldManifestRevision, &action.OldManifestHash, &action.RecipeSHA256, &action.TargetRuntimeGeneration, &targetPID, &targetMarker, &targetIncarnation, &action.Stage, &unexpected, &failureCode, &failureMessage, &action.CreatedAt, &action.UpdatedAt, &expires)
	if err != nil {
		return nil, err
	}
	action.TargetProcessPID = int(targetPID.Int64)
	action.TargetProcessStartMarker, action.TargetProcessIncarnation = targetMarker.String, targetIncarnation.String
	action.UnexpectedExitCount, action.FailureCode, action.FailureMessage, action.ExpiresAt = int(unexpected.Int64), failureCode.String, failureMessage.String, expires.String
	return &action, nil
}

func managedRootRecoveryFenceWhere(action *ManagedRootRecoveryAction) string {
	return `agent_id=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`
}

func managedRootRecoveryFenceArgs(action *ManagedRootRecoveryAction) []any {
	return []any{action.AgentID, action.OldPiSessionID, action.OldSessionEpoch, action.OldProcessPID, action.OldProcessStartMarker, action.OldProcessIncarnation, action.OldRuntimeGeneration, action.OldManifestRevision, action.OldManifestHash}
}
