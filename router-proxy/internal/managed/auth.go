package managed

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var ErrProviderAuthBusy = errors.New("provider auth operation is already pending")

const (
	ProviderAuthLifetime = 10 * time.Minute
	maxProviderPromptID  = 128
)

// ProviderAuthPrompt is deliberately limited to display metadata. The value a
// user enters is consumed by the helper and never becomes router state.
type ProviderAuthPrompt struct {
	ID          string
	Kind        string
	Message     string
	Placeholder string
	URL         string
}

type ProviderAuthEvent struct {
	Kind        string
	OperationID string
	Provider    string
	Prompt      *ProviderAuthPrompt
	Status      string
	Providers   []store.ManagedProviderStatus
	Models      []store.ManagedProviderModel
	Error       string
}

// ProviderAuthRun is a one-shot private JSONL helper process. Implementations
// may expose only parsed metadata; raw stdout/stderr and prompt values stay
// inside the process boundary.
type ProviderAuthRun interface {
	Events() <-chan ProviderAuthEvent
	Respond(context.Context, string, string) error
	Cancel()
	Wait() error
}

type ProviderAuthHelper interface {
	Start(context.Context, string, string, string) (ProviderAuthRun, error)
}

// ProviderAuthSnapshotHelper reads the ordinary Pi credential store through a
// fixed local helper. Its result is a bounded, non-secret status/catalog view.
type ProviderAuthSnapshotHelper interface {
	Snapshot(context.Context) ([]store.ManagedProviderStatus, []store.ManagedProviderModel, error)
}

type ProviderAuthCoordinator struct {
	store   *store.Store
	manager *Manager
	helper  ProviderAuthHelper

	mu       sync.Mutex
	current  *providerAuthOperation
	closed   bool
	publish  func(ProviderAuthEvent)
	lifetime time.Duration
	wg       sync.WaitGroup
}

type providerAuthOperation struct {
	id       string
	provider string
	action   string
	run      ProviderAuthRun
	ctx      context.Context
	cancel   context.CancelFunc
	prompt   *ProviderAuthPrompt
	promptID string
	settled  bool
}

func NewProviderAuthCoordinator(durable *store.Store, manager *Manager, helper ProviderAuthHelper) (*ProviderAuthCoordinator, error) {
	if durable == nil || helper == nil {
		return nil, errors.New("provider auth requires store and helper")
	}
	return &ProviderAuthCoordinator{store: durable, manager: manager, helper: helper, lifetime: ProviderAuthLifetime}, nil
}

func validProviderAuthProvider(provider string) bool {
	return provider == "openai-codex" || provider == "google" || provider == "cursor"
}

func (a *ProviderAuthCoordinator) SetPublisher(publish func(ProviderAuthEvent)) {
	a.mu.Lock()
	a.publish = publish
	a.mu.Unlock()
}

func (a *ProviderAuthCoordinator) publishEvent(event ProviderAuthEvent) {
	a.mu.Lock()
	publish := a.publish
	a.mu.Unlock()
	if publish != nil {
		publish(event)
	}
}

func (a *ProviderAuthCoordinator) Snapshot(ctx context.Context) (store.ManagedProviderSnapshot, error) {
	reader, ok := a.helper.(ProviderAuthSnapshotHelper)
	if !ok {
		return a.store.ManagedProviderSnapshot(ctx)
	}
	providers, models, err := reader.Snapshot(ctx)
	if err != nil {
		return store.ManagedProviderSnapshot{}, errors.New("provider status is unavailable")
	}
	if _, err := a.store.CommitManagedProviderSnapshot(ctx, providers, models); err != nil {
		return store.ManagedProviderSnapshot{}, errors.New("provider status is unavailable")
	}
	snapshot, err := a.store.ManagedProviderSnapshot(ctx)
	if err != nil {
		return store.ManagedProviderSnapshot{}, errors.New("provider status is unavailable")
	}
	return snapshot, nil
}

// CurrentPrompt returns a copy of the one live prompt, if any, so a direct
// UI-live reconnect can continue the same bounded operation without storing
// prompt values or replaying helper output.
func (a *ProviderAuthCoordinator) CurrentPrompt() *ProviderAuthEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.current == nil || a.current.settled || a.current.prompt == nil || a.current.promptID == "" {
		return nil
	}
	prompt := *a.current.prompt
	return &ProviderAuthEvent{Kind: "prompt", OperationID: a.current.id, Provider: a.current.provider, Prompt: &prompt}
}

// Begin accepts one router-wide operation. The caller has already reserved its
// idempotency response, so a rejected concurrent request performs no helper
// work and cannot observe another operation's prompt.
func (a *ProviderAuthCoordinator) Begin(ctx context.Context, operationID, provider, action string) error {
	if operationID == "" || !validProviderAuthProvider(provider) || (action != "login" && action != "logout") {
		return errors.New("provider auth operation is invalid")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("provider auth is closed")
	}
	if a.current != nil {
		a.mu.Unlock()
		return ErrProviderAuthBusy
	}
	operationCtx, cancel := context.WithTimeout(context.Background(), a.lifetime)
	run, err := a.helper.Start(operationCtx, operationID, provider, action)
	if err != nil {
		cancel()
		a.mu.Unlock()
		return err
	}
	operation := &providerAuthOperation{id: operationID, provider: provider, action: action, run: run, ctx: operationCtx, cancel: cancel}
	a.current = operation
	a.wg.Add(1)
	a.mu.Unlock()
	a.publishEvent(ProviderAuthEvent{Kind: "state", OperationID: operationID, Provider: provider, Status: "pending"})
	go a.watch(operation)
	return nil
}

func (a *ProviderAuthCoordinator) watch(operation *providerAuthOperation) {
	defer a.wg.Done()
	terminal := false
	for {
		select {
		case <-operation.ctx.Done():
			operation.run.Cancel()
			if errors.Is(operation.ctx.Err(), context.DeadlineExceeded) {
				a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "expired", Error: "Provider sign-in expired"})
			} else {
				a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "cancelled", Error: "Provider sign-in was cancelled"})
			}
			return
		case event, ok := <-operation.run.Events():
			if !ok {
				if !terminal {
					a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "failed", Error: "Provider sign-in did not complete"})
				}
				return
			}
			if event.OperationID != operation.id || event.Provider != operation.provider {
				operation.run.Cancel()
				a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "failed", Error: "Provider sign-in returned an invalid response"})
				return
			}
			switch event.Kind {
			case "prompt":
				if !validProviderAuthPrompt(event.Prompt) {
					operation.run.Cancel()
					a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "failed", Error: "Provider sign-in returned an invalid prompt"})
					return
				}
				a.mu.Lock()
				if a.current == operation && !operation.settled {
					prompt := *event.Prompt
					operation.prompt = &prompt
					operation.promptID = event.Prompt.ID
				}
				a.mu.Unlock()
				a.publishEvent(event)
			case "state":
				a.publishEvent(event)
			case "terminal":
				terminal = true
				a.finish(operation, event)
				return
			default:
				operation.run.Cancel()
				a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "failed", Error: "Provider sign-in returned an invalid response"})
				return
			}
		}
	}
}

func (a *ProviderAuthCoordinator) finish(operation *providerAuthOperation, event ProviderAuthEvent) {
	// A terminal line is not permission for a helper to remain alive. Closing
	// the one-shot process before waiting also makes malformed terminal output
	// and router shutdown use the same bounded cleanup path.
	operation.cancel()
	operation.run.Cancel()
	_ = operation.run.Wait()
	success := event.Kind == "terminal" && event.Status == "succeeded"
	if success {
		if _, err := a.store.CommitManagedProviderSnapshot(context.Background(), event.Providers, event.Models); err != nil {
			event = ProviderAuthEvent{Kind: "state", OperationID: operation.id, Provider: operation.provider, Status: "failed", Error: "Provider sign-in could not be saved"}
			success = false
		}
	}
	a.mu.Lock()
	if a.current != operation || operation.settled {
		a.mu.Unlock()
		return
	}
	operation.settled = true
	a.current = nil
	a.mu.Unlock()
	// The route keeps its accepted response pending until the helper reaches a
	// terminal state. This preserves worker-wide replay after router restart
	// without turning a duplicate request into another provider interaction.
	_ = a.store.MarkManagedOperationTerminal(context.Background(), operation.id)
	if success && a.manager != nil {
		// Manager-owned restart reservation preserves its existing turn-settlement
		// and session fences. Failure to schedule one agent does not alter the
		// committed provider catalog or trigger an arbitrary model fallback.
		a.manager.RestartAfterAuthChange(context.Background(), operation.id)
	}
	if event.Kind == "terminal" {
		event.Kind = "state"
	}
	if event.Status == "" {
		event.Status = "failed"
	}
	a.publishEvent(event)
}

// Respond consumes the current prompt before forwarding the value. A second
// request cannot replay it even if the helper has not yet read stdin.
func validProviderAuthPrompt(prompt *ProviderAuthPrompt) bool {
	if prompt == nil || len(prompt.ID) == 0 || len(prompt.ID) > maxProviderPromptID || len(prompt.Message) == 0 || len(prompt.Message) > 512 || len(prompt.Placeholder) > 256 || len(prompt.URL) > 2048 {
		return false
	}
	if prompt.URL != "" {
		parsed, err := url.Parse(prompt.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return false
		}
	}
	switch prompt.Kind {
	case "text", "secret", "select", "manual_code":
		return true
	default:
		return false
	}
}

func (a *ProviderAuthCoordinator) Respond(ctx context.Context, operationID, promptID, value string) error {
	if operationID == "" || promptID == "" || len([]byte(value)) > 8*1024 {
		return errors.New("provider auth response is invalid")
	}
	a.mu.Lock()
	operation := a.current
	if operation == nil || operation.id != operationID || operation.promptID != promptID || operation.settled {
		a.mu.Unlock()
		return errors.New("provider auth prompt is unavailable")
	}
	operation.promptID = ""
	operation.prompt = nil
	run := operation.run
	a.mu.Unlock()
	if err := run.Respond(ctx, promptID, value); err != nil {
		a.finish(operation, ProviderAuthEvent{Kind: "state", OperationID: operationID, Provider: operation.provider, Status: "failed", Error: "Provider sign-in could not continue"})
		return err
	}
	return nil
}

func (a *ProviderAuthCoordinator) Close() {
	a.mu.Lock()
	a.closed = true
	operation := a.current
	a.mu.Unlock()
	if operation != nil {
		operation.cancel()
		operation.run.Cancel()
	}
	a.wg.Wait()
}
