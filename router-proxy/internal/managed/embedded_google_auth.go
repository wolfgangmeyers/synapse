package managed

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sky-valley/pi/ai"
)

type googleAuthJSON struct {
	Antigravity struct {
		Type      string    `json:"type"`
		Access    string    `json:"access"`
		Refresh   string    `json:"refresh"`
		Expires   time.Time `json:"expires"`
		ProjectID string    `json:"projectId"`
		Email     string    `json:"email"`
	} `json:"antigravity"`
}

func (m *Manager) embeddedGoogleAuth(ctx context.Context) (string, error) {
	userHome, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(m.config.PiAgentDir, "auth.json"),
		filepath.Join(m.config.ManagedHome, "auth.json"),
		filepath.Join(userHome, ".pi", "agent", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".pi", "agent", "auth.json"),
	}
	var data []byte
	var err error
	for _, p := range paths {
		if p == "" {
			continue
		}
		data, err = os.ReadFile(p)
		if err == nil && len(data) > 0 {
			break
		}
	}
	if len(data) == 0 {
		return "", errors.New("google auth credentials not found in auth.json")
	}
	var authFile googleAuthJSON
	if err := json.Unmarshal(data, &authFile); err != nil {
		return "", err
	}
	if authFile.Antigravity.Access == "" {
		return "", errors.New("no google access token in auth.json")
	}
	return authFile.Antigravity.Access, nil
}

func (m *Manager) streamEmbeddedGoogle(requestCtx context.Context, requestModel *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	token, err := m.embeddedGoogleAuth(requestCtx)
	if err != nil {
		return ai.ErrorStream(requestModel, err)
	}
	if opts == nil {
		opts = &ai.SimpleStreamOptions{}
	} else {
		cp := *opts
		opts = &cp
	}
	if opts.Headers == nil {
		opts.Headers = make(ai.ProviderHeaders)
	}
	authBearer := "Bearer " + token
	opts.Headers["Authorization"] = &authBearer
	opts.APIKey = token
	return ai.StreamSimple(requestCtx, requestModel, ai.Context{Messages: transcript.Messages}, opts)
}
