package managed

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// The shared Pi catalog remains broad. Embedded authority comes only from the
// pinned Codex SDK model and this router's private credential, even for defaults.
func (m *Manager) admitEmbeddedModel(ctx context.Context, candidate Model) (Model, error) {
	if candidate.Provider == "antigravity" || candidate.Provider == "google" || candidate.Provider == "google-vertex" {
		return candidate, nil
	}
	sdk := ai.GetModel(candidate.Provider, candidate.ID)
	if validateEmbeddedCodexModel(sdk) != nil || m.codexCredentialStore == nil {
		return Model{}, &modelUnavailableError{candidate.Provider, candidate.ID}
	}
	credential, err := m.codexCredentialStore.Load(ctx)
	if err != nil || credential.AccessToken == "" || credential.RefreshToken == "" || credential.AccountID == "" || credential.ExpiresAt.IsZero() {
		return Model{}, &modelUnavailableError{candidate.Provider, candidate.ID}
	}
	return candidate, nil
}

// Admission excludes the entire credential parent (lock and temporary leaves
// included). Compare canonical components in both directions. RPC roots may
// still be broad: only embedded Cwds must be disjoint, including restored Cwds.
func (m *Manager) validateEmbeddedWorkingDirectory(cwd string) error {
	if m.codexCredentialStore == nil {
		return nil
	}
	project, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return errors.New("embedded working directory unavailable")
	}
	protected, err := filepath.EvalSymlinks(m.codexCredentialStore.Directory())
	if err != nil {
		return errors.New("embedded credential tree unavailable")
	}
	for _, pair := range [][2]string{{project, protected}, {protected, project}} {
		relative, err := filepath.Rel(pair[1], pair[0])
		if err != nil {
			return errors.New("embedded credential tree comparison unavailable")
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return errors.New("embedded working directory overlaps credential tree")
		}
	}
	return nil
}

// admitWorkflowStateTool verifies whether the specified tool is permitted by the agent's current active workflow state.
// If the agent has no active workflow state, or if AllowedTools is empty, all tools are permitted.
// If AllowedTools is non-empty, only tools in AllowedTools (with alias resolution) are permitted.
func admitWorkflowStateTool(ctx context.Context, s *store.Store, agentID, toolName string) error {
	if s == nil || agentID == "" {
		return nil
	}
	state, err := s.GetManagedAgentActiveWorkflowState(ctx, agentID)
	if err != nil || state == nil {
		if agent, aerr := s.ManagedAgent(ctx, agentID); aerr == nil && agent != nil && agent.ParentAgentID != "" {
			state, _ = s.GetManagedAgentActiveWorkflowState(ctx, agent.ParentAgentID)
		}
	}
	if state == nil || len(state.AllowedTools) == 0 {
		return nil
	}
	if !store.IsToolAllowedInWorkflowState(toolName, state.AllowedTools) {
		return fmt.Errorf("tool '%s' is not allowed in active workflow state '%s'", toolName, state.Name)
	}
	return nil
}
