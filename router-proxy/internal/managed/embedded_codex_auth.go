package managed

import (
	"context"
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// embeddedCodexAuth resolves credentials per provider request, never per session.
// Update holds the cross-process store lock through the expiry check and issuer.
// rotateEmbeddedCodexUnauthorized serializes a forced refresh with other
// processes. A peer that already replaced the bearer used by this request
// wins: the next request reads its new credential instead of refreshing again.
func (m *Manager) rotateEmbeddedCodexUnauthorized(ctx context.Context, used string) error {
	if used == "" || m.codexCredentialStore == nil || m.codexRefresh == nil {
		return errors.New("Codex refresh unavailable")
	}
	if err := m.codexCredentialStore.Update(ctx, func(locked store.CodexCredential) (store.CodexCredential, error) {
		if locked.AccessToken != used {
			return locked, nil
		}
		next, issueErr := m.codexRefresh(ctx, locked)
		if issueErr != nil || next.AccessToken == "" || next.AccountID == "" || !time.Now().Add(30*time.Second).Before(next.ExpiresAt) {
			return store.CodexCredential{}, errors.New("Codex refresh failed")
		}
		return next, nil
	}); err != nil {
		return errors.New("Codex refresh unavailable")
	}
	return nil
}

func (m *Manager) embeddedCodexAuth(ctx context.Context) (ai.ModelAuth, error) {
	if m.codexCredentialStore == nil {
		return ai.ModelAuth{}, errors.New("Codex credential store unavailable")
	}
	current, err := m.codexCredentialStore.Load(ctx)
	if err != nil {
		return ai.ModelAuth{}, errors.New("Codex credential unavailable")
	}
	if !time.Now().Add(30 * time.Second).Before(current.ExpiresAt) {
		if m.codexRefresh == nil {
			return ai.ModelAuth{}, errors.New("Codex refresh issuer unavailable")
		}
		err = m.codexCredentialStore.Update(ctx, func(locked store.CodexCredential) (store.CodexCredential, error) {
			if time.Now().Add(30 * time.Second).Before(locked.ExpiresAt) {
				return locked, nil
			}
			next, issueErr := m.codexRefresh(ctx, locked)
			if issueErr != nil {
				return store.CodexCredential{}, errors.New("Codex refresh failed")
			}
			if !time.Now().Add(30*time.Second).Before(next.ExpiresAt) || next.AccountID == "" {
				return store.CodexCredential{}, errors.New("Codex refresh returned invalid credential")
			}
			return next, nil
		})
		if err != nil {
			return ai.ModelAuth{}, errors.New("Codex refresh unavailable")
		}
		current, err = m.codexCredentialStore.Load(ctx)
		if err != nil {
			return ai.ModelAuth{}, errors.New("Codex credential unavailable")
		}
	}
	if current.AccountID == "" || !time.Now().Before(current.ExpiresAt) {
		return ai.ModelAuth{}, errors.New("Codex credential unavailable")
	}
	return ai.ModelAuth{APIKey: current.AccessToken, Headers: ai.ProviderHeaders{"ChatGPT-Account-ID": ai.HeaderValue(current.AccountID)}}, nil
}
