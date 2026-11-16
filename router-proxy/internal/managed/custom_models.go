package managed

import (
	"context"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
)

func init() {
	providers.RegisterOpenAICompletions()
}

// CanonicalCustomProviderID ensures custom provider names start with "custom-".
func CanonicalCustomProviderID(providerID string) string {
	providerID = strings.TrimSpace(providerID)
	if strings.HasPrefix(providerID, "custom-") {
		return providerID
	}
	return "custom-" + providerID
}

// IsCustomProvider returns true if the provider ID has a "custom-" prefix.
func IsCustomProvider(providerID string) bool {
	return strings.HasPrefix(strings.TrimSpace(providerID), "custom-")
}

// SyncCustomModelsToAI loads all custom providers from the store and registers active models with ai.RegisterModel.
func SyncCustomModelsToAI(ctx context.Context, s *store.Store) error {
	if s == nil {
		return nil
	}
	providersList, err := s.ListCustomProviders(ctx)
	if err != nil {
		return err
	}

	key, err := crypto.DeriveKey("")
	if err != nil {
		key = nil
	}

	for _, p := range providersList {
		if !p.Provider.Enabled {
			continue
		}
		providerStr := CanonicalCustomProviderID(p.Provider.ID)

		headers := ai.ProviderHeaders{
			"Authorization": ai.HeaderValue("Bearer none"),
		}
		if p.Provider.APIKeyCiphertext != nil && *p.Provider.APIKeyCiphertext != "" && key != nil {
			decrypted, decErr := crypto.Decrypt(*p.Provider.APIKeyCiphertext, key)
			if decErr == nil && len(decrypted) > 0 {
				headers["Authorization"] = ai.HeaderValue("Bearer " + string(decrypted))
			}
		}

		for _, m := range p.Models {
			ctxWin := m.ContextWindow
			if ctxWin <= 0 {
				ctxWin = 131072
			}
			maxTok := m.MaxTokens
			if maxTok <= 0 {
				maxTok = 8192
			}
			dispName := m.DisplayName
			if dispName == "" {
				dispName = m.ID
			}

			aiModel := &ai.Model{
				Type:          ai.ModelTypeChat,
				ID:            m.ID,
				Provider:      providerStr,
				Name:          dispName,
				Api:           ai.APIOpenAICompletions,
				BaseURL:       p.Provider.BaseURL,
				Headers:       headers,
				ContextWindow: ctxWin,
				MaxTokens:     maxTok,
				Input:         []string{"text"},
			}
			ai.RegisterModel(aiModel)
		}
	}

	return nil
}
