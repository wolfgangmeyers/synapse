package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// This registration is router-owned. The SDK call carries arguments, never a
// caller identity, role, parent, or authority to choose a root lane.
func embeddedWorkerLaunchSchema() *ai.Schema {
	model := ai.Object(ai.Prop("provider", ai.String()), ai.Prop("id", ai.String()))
	closed := false
	model.AdditionalAllowed = &closed
	schema := ai.Object(ai.Prop("action", ai.EnumOf("list", "launch", "cleanup", "send")), ai.Prop("name", ai.String()), ai.Prop("cwd", ai.String()), ai.Opt("model", model), ai.Prop("instruction", ai.String()))
	schema.AdditionalAllowed = &closed
	return schema
}

func embeddedWorkerRequestID(binding store.EmbeddedBinding, callID string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("embedded-worker-launch\x00%s\x00%s\x00%d\x00%s\x00%s", binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, callID)))
	digest[6] = (digest[6] & 15) | 64
	digest[8] = (digest[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

func (m *Manager) embeddedWorkerLaunchTool(owner embeddedOwner, freshAdmission ...func(string) bool) agent.AgentTool {
	return agent.AgentTool{Name: "mecha_worker", Description: "Launch one independent embedded worker with action launch. Other worker actions are unavailable in this creation-only capability. Returns an accepted operation without waiting for readiness.", Parameters: embeddedWorkerLaunchSchema(), Execute: func(ctx context.Context, callID string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		// The canonical declaration is not permission to perform other actions.
		if action, ok := args["action"].(string); !ok || action != "launch" {
			return agent.AgentToolResult{}, errors.New("embedded worker capability only supports action launch")
		}
		binding := owner.binding
		// Test-only fault seam: the SDK has committed its original intent,
		// but no worker reservation has started yet. Nil in production.
		if m.embeddedAfterIntent != nil {
			m.embeddedAfterIntent(ctx)
		}
		if err := m.authorizeEmbeddedWorkerLaunch(ctx, owner); err != nil {
			return agent.AgentToolResult{}, err
		}
		name, ok := args["name"].(string)
		if !ok {
			return agent.AgentToolResult{}, errors.New("invalid worker name")
		}
		cwd, ok := args["cwd"].(string)
		if !ok {
			return agent.AgentToolResult{}, errors.New("invalid worker cwd")
		}
		instruction, ok := args["instruction"].(string)
		if !ok {
			return agent.AgentToolResult{}, errors.New("invalid worker instruction")
		}
		var model *Model
		if raw, exists := args["model"]; exists {
			value, ok := raw.(map[string]any)
			if !ok {
				return agent.AgentToolResult{}, errors.New("invalid worker model")
			}
			provider, p := value["provider"].(string)
			id, i := value["id"].(string)
			if !p || !i {
				return agent.AgentToolResult{}, errors.New("invalid worker model")
			}
			model = &Model{Provider: provider, ID: id}
		}
		body, err := json.Marshal(args)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		requestID := embeddedWorkerRequestID(binding, callID)
		// Reconcile the original SDK intent separately from the launch operation.
		// The only permission to reserve without an operation is a fresh admission
		// in this SDK turn, consumed once at Execute entry.
		fresh := false
		if len(freshAdmission) == 1 {
			fresh = freshAdmission[0](callID)
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, binding, callID)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("worker launch intent unknown: %w", err)
		}
		var original struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(effect.Intent, &original); err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("worker launch intent invalid: %w", err)
		}
		originalArgs, err := json.Marshal(original.Arguments)
		if err != nil || original.ID != callID || original.Name != "mecha_worker" || !bytes.Equal(originalArgs, body) {
			return agent.AgentToolResult{}, errors.New("worker launch call ID conflicts with durable intent")
		}
		prior, err := m.store.ManagedOperation(ctx, requestID)
		if errors.Is(err, sql.ErrNoRows) {
			if !fresh || effect.State != "unknown" {
				return agent.AgentToolResult{}, errors.New("worker launch outcome unknown: no proven worker reservation")
			}
		} else if err != nil {
			return agent.AgentToolResult{}, err
		} else {
			if prior.Method != "POST" || prior.CanonicalPath != "/v1/managed-agents" || prior.RequestID != requestID || prior.CanonicalBodyHash != hashString(string(body)) {
				return agent.AgentToolResult{}, errors.New("worker launch reservation conflicts with durable intent")
			}
			if effect.State == "unknown" {
				// A launch operation alone doesn't prove that the SDK saved its result.
				// Only the checked, correlated toolResult permits ordinary replay.
				worker, workerErr := m.store.ManagedAgent(ctx, name)
				if workerErr != nil && !errors.Is(workerErr, sql.ErrNoRows) {
					return agent.AgentToolResult{}, workerErr
				}
				if workerErr == nil && (worker.LaunchOperationID != requestID || worker.Runtime != "embedded") {
					return agent.AgentToolResult{}, errors.New("worker launch reservation conflicts with durable worker")
				}
				snapshot, snapshotErr := m.store.ReopenEmbedded(ctx, binding)
				if snapshotErr != nil {
					return agent.AgentToolResult{}, snapshotErr
				}
				committed := false
				for _, entry := range snapshot.Entries {
					if entry.Kind != "toolResult" {
						continue
					}
					message, decodeErr := ai.UnmarshalMessage(entry.Body)
					if decodeErr != nil {
						return agent.AgentToolResult{}, decodeErr
					}
					result, ok := message.(ai.ToolResultMessage)
					if !ok || result.ToolCallID != callID || result.ToolName != "mecha_worker" || result.IsError || len(result.Content) != 1 {
						continue
					}
					text, ok := result.Content[0].(ai.TextContent)
					if !ok {
						continue
					}
					var accepted struct {
						Status      string `json:"status"`
						WorkerID    string `json:"worker_id"`
						OperationID string `json:"operation_id"`
					}
					if json.Unmarshal([]byte(text.Text), &accepted) == nil && workerErr == nil && accepted.Status == "accepted" && accepted.WorkerID == worker.AgentID && accepted.OperationID == requestID {
						committed = true
						break
					}
				}
				if !committed {
					return agent.AgentToolResult{}, errors.New("worker launch outcome unknown: SDK tool result was not committed")
				}
			}
		}
		// Bind the canonical arguments independently of the call-derived ID. The
		// ordinary launch reservation owns idempotency and the new worker's lane.
		launched, err := m.launchEmbeddedWorkerFromOwner(ctx, LaunchRequest{Name: name, Cwd: cwd, Model: model, InitialInstruction: instruction, Runtime: "embedded", RequestID: requestID, CanonicalBodyHash: hashString(string(body))}, owner)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if !launched.Replayed {
			aliasTrace("worker-dispatch", binding.AgentID, binding.SessionID, requestID, callID)
		}
		if m.embeddedAfterWorkerLaunch != nil {
			m.embeddedAfterWorkerLaunch(ctx)
		}
		payload, err := json.Marshal(map[string]any{"status": "accepted", "worker_id": launched.Agent.AgentID, "operation_id": requestID, "replayed": launched.Replayed})
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}, nil
	}}
}

func (m *Manager) authorizeEmbeddedWorkerLaunch(ctx context.Context, owner embeddedOwner) error {
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return err
	}
	role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
	if err != nil {
		return err
	}
	if role != "root" && role != "worker" {
		return errors.New("embedded worker launch requires root or worker capability")
	}
	return nil
}
