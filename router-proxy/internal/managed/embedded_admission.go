package managed

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/sky-valley/pi/ai"
)

// The shared Pi catalog remains broad. Embedded authority comes only from the
// pinned Codex SDK model and this router's private credential, even for defaults.
func (m *Manager) admitEmbeddedModel(ctx context.Context, candidate Model) (Model, error) {
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
		return errors.New("embedded Codex credential store unavailable")
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
