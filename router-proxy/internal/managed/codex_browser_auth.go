package managed

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// CodexBrowserAuth composes the existing operation coordinator with the Go
// credential owner. Other providers keep the ordinary Pi helper contract.
// Device/OAuth secrets stay inside Go; only bounded display events leave it.
type CodexBrowserAuth struct {
	Ordinary    ProviderAuthHelper
	Credentials *store.LocalCodexCredentialStore
	Client      *http.Client
	Manager     *Manager
}

func (h CodexBrowserAuth) Snapshot(ctx context.Context) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, error) {
	return h.snapshot(ctx, nil)
}

// CommitSnapshot keeps the credential lock through the SQLite status commit.
// Neither a late GET nor another provider's terminal helper snapshot can
// restore pre-logout Codex metadata or replace the current owned revision.
func (h CodexBrowserAuth) CommitSnapshot(ctx context.Context, durable *store.Store) (store.ManagedProviderSnapshot, error) {
	var snapshot store.ManagedProviderSnapshot
	_, _, err := h.snapshot(ctx, func(providers []store.ManagedProviderStatus, models []store.ManagedProviderModel) error {
		if _, err := durable.CommitManagedProviderSnapshot(ctx, providers, models); err != nil {
			return err
		}
		var err error
		snapshot, err = durable.ManagedProviderSnapshot(ctx)
		return err
	})
	return snapshot, err
}

func (h CodexBrowserAuth) snapshot(ctx context.Context, commit func([]store.ManagedProviderStatus, []store.ManagedProviderModel) error) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, error) {
	reader, ok := h.Ordinary.(ProviderAuthSnapshotHelper)
	if !ok || h.Credentials == nil {
		return nil, nil, errors.New("provider status is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Resolve/rotate in Go before helper discovery. The configured ordinary
	// helper masks Codex resolution, so it can never spend this refresh token.
	if h.Manager != nil {
		_, _ = h.Manager.embeddedCodexAuth(ctx)
	}
	var projected []store.ManagedProviderStatus
	var catalog []store.ManagedProviderModel
	err := h.Credentials.ReadForProjection(ctx, func(credential *store.CodexCredential) error {
		helperCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		providers, models, err := reader.Snapshot(helperCtx)
		if err != nil {
			return errors.New("provider status is unavailable")
		}
		usable := credential != nil && credential.AccountID != "" && time.Now().Add(30*time.Second).Before(credential.ExpiresAt)
		projected = make([]store.ManagedProviderStatus, 0, len(providers)+1)
		for _, p := range providers {
			if p.Provider != "openai-codex" {
				projected = append(projected, p)
			}
		}
		status := "signed_out"
		if usable {
			status = "authenticated"
		}
		projected = append(projected, store.ManagedProviderStatus{Provider: "openai-codex", Label: "ChatGPT", Authenticated: usable, Status: status})
		catalog = make([]store.ManagedProviderModel, 0, len(models))
		for _, model := range models {
			if model.Provider != "openai-codex" {
				catalog = append(catalog, model)
			}
		}
		if usable {
			for _, model := range ai.GetModels("openai-codex") {
				if model.Api == ai.APIOpenAICodexResponses {
					catalog = append(catalog, store.ManagedProviderModel{Provider: model.Provider, ID: model.ID, Name: model.Name, Available: true})
				}
			}
		}
		if commit != nil {
			return commit(projected, catalog)
		}
		return nil
	})
	if err != nil {
		return nil, nil, errors.New("provider status is unavailable")
	}
	return projected, catalog, nil
}

func (h CodexBrowserAuth) Start(ctx context.Context, operationID, provider, action string) (ProviderAuthRun, error) {
	if provider != "openai-codex" {
		return h.Ordinary.Start(ctx, operationID, provider, action)
	}
	if h.Credentials == nil || h.Client == nil || operationID == "" || (action != "login" && action != "logout") {
		return nil, errors.New("Codex sign-in is unavailable")
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &codexBrowserRun{events: make(chan ProviderAuthEvent, 2), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(run.done)
		defer close(run.events)
		var err error
		if action == "logout" {
			err = h.Credentials.Delete(runCtx)
		} else {
			// Login, expiry refresh and logout share the file's stable lock. No
			// stale refresh can overwrite a newly completed login or logout.
			err = h.Credentials.Update(runCtx, func(store.CodexCredential) (store.CodexCredential, error) {
				invalid, prompted := false, false
				credential, issueErr := codexdevice.Acquire(runCtx, h.Client, func(p codexdevice.DevicePrompt) {
					if prompted || !codexdevice.ValidUserCode(p.UserCode) {
						invalid = true
						return
					}
					prompted = true
					run.emit(runCtx, ProviderAuthEvent{Kind: "prompt", OperationID: operationID, Provider: provider, Prompt: &ProviderAuthPrompt{ID: "device-confirm", Kind: "manual_code", Message: "Enter " + p.UserCode + " on the Codex sign-in page. Authorization completes automatically.", URL: "https://auth.openai.com/codex/device"}})
				})
				if issueErr != nil || invalid || !prompted || credential.AccountID == "" || credential.ExpiresAt.IsZero() {
					return store.CodexCredential{}, errors.New("Codex sign-in failed")
				}
				if !time.Now().Add(30 * time.Second).Before(credential.ExpiresAt) {
					return store.CodexCredential{}, errors.New("Codex sign-in failed")
				}
				return credential, nil
			})
		}
		event := ProviderAuthEvent{Kind: "terminal", OperationID: operationID, Provider: provider, Status: "failed", Error: "Codex sign-in did not complete"}
		if err == nil {
			event.Status = "succeeded"
			event.Error = ""
		}
		// The coordinator reads and commits the current composite snapshot
		// under the owned lock; no cached terminal auth snapshot is trusted.
		run.emit(runCtx, event)
	}()
	return run, nil
}

type codexBrowserRun struct {
	events    chan ProviderAuthEvent
	done      chan struct{}
	cancel    context.CancelFunc
	mu        sync.Mutex
	responded bool
}

func (r *codexBrowserRun) Events() <-chan ProviderAuthEvent { return r.events }
func (r *codexBrowserRun) emit(ctx context.Context, event ProviderAuthEvent) {
	select {
	case r.events <- event:
	case <-ctx.Done():
	}
}
func (r *codexBrowserRun) Respond(ctx context.Context, id, value string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != "device-confirm" || r.responded || len(value) > 8192 {
		return errors.New("Codex prompt is unavailable")
	}
	select {
	case <-r.done:
		return errors.New("Codex prompt is unavailable")
	default:
	}
	r.responded = true // Values are neither used as issuer input nor retained.
	return nil
}
func (r *codexBrowserRun) Cancel()     { r.cancel() }
func (r *codexBrowserRun) Wait() error { <-r.done; return nil }
