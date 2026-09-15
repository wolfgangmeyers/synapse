package transport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

// PolicySender delivers a normal durable mailbox message. Tests may replace it
// to observe policy output without changing the sweep lifecycle.
type PolicySender func(context.Context, protocol.Envelope) error

// NativePolicy is a router policy run by the existing lifecycle sweep.
type NativePolicy struct {
	Name   string
	Period time.Duration
	Run    func(context.Context, *PolicyContext) error
}

// PolicyContext exposes the narrow live router state native policies need.
type PolicyContext struct{ router *Router }

func (c *PolicyContext) TmuxWindows(ctx context.Context, session string) ([]TmuxWindow, error) {
	return c.router.auditTmuxWindows(ctx, session)
}

func (c *PolicyContext) ConnectedAgents(ctx context.Context) map[string]any {
	return c.router.connectedAgents(ctx, false)
}

func (c *PolicyContext) Reminders(ctx context.Context) ([]domain.Reminder, error) {
	return c.router.store.Reminders(ctx)
}

func (c *PolicyContext) Send(ctx context.Context, message protocol.Envelope) error {
	return c.router.policySender(ctx, message)
}

func (c *PolicyContext) Now() string { return timestamp(c.router.now()) }

func (c *PolicyContext) NextID() (string, error) { return c.router.nextID() }

func nativePolicies(configured []NativePolicy) ([]NativePolicy, error) {
	policies := append([]NativePolicy(nil), configured...)
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if !failureRE.MatchString(policy.Name) || policy.Period < 0 || policy.Run == nil {
			return nil, errors.New("invalid native policy")
		}
		if _, exists := seen[policy.Name]; exists {
			return nil, fmt.Errorf("duplicate native policy %q", policy.Name)
		}
		seen[policy.Name] = struct{}{}
	}
	sort.SliceStable(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}

func (r *Router) runPolicies(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, policy := range r.policies {
		policyCtx, cancel := context.WithTimeout(ctx, r.policyTimeout)
		stopLifecycle := context.AfterFunc(r.policyContext, cancel)
		result, started := r.startPolicy(policyCtx, policy)
		if !started {
			stopLifecycle()
			cancel()
			continue
		}
		select {
		case err := <-result:
			if err != nil {
				r.emitEvent(RouterErrorWithPolicyEvent("native_policy_failed", policy.Name))
			}
		case <-policyCtx.Done():
			r.emitEvent(RouterErrorWithPolicyEvent("native_policy_failed", policy.Name))
		}
		stopLifecycle()
		cancel()
	}
}

// startPolicy allows one bounded policy invocation at a time. A policy with a
// period records its start time before invocation, so a one-second lifecycle
// sweep cannot trigger a thirty-minute audit repeatedly.
func (r *Router) startPolicy(ctx context.Context, policy NativePolicy) (<-chan error, bool) {
	r.policyMu.Lock()
	if r.policiesClosed || r.policyRunning[policy.Name] {
		r.policyMu.Unlock()
		return nil, false
	}
	now := r.now()
	if policy.Period > 0 {
		if previous, exists := r.policyLastRun[policy.Name]; exists && now.Sub(previous) < policy.Period {
			r.policyMu.Unlock()
			return nil, false
		}
		r.policyLastRun[policy.Name] = now
	}
	r.policyRunning[policy.Name] = true
	r.policyWG.Add(1)
	r.policyMu.Unlock()

	result := make(chan error, 1)
	go func() {
		defer r.policyWG.Done()
		err := policy.Run(ctx, &PolicyContext{router: r})
		r.policyMu.Lock()
		delete(r.policyRunning, policy.Name)
		r.policyMu.Unlock()
		result <- err
	}()
	return result, true
}

func (r *Router) runPoliciesAsync(ctx context.Context) {
	r.policyMu.Lock()
	if r.policiesClosed {
		r.policyMu.Unlock()
		return
	}
	r.policyRunWG.Add(1)
	r.policyMu.Unlock()
	go func() {
		defer r.policyRunWG.Done()
		r.runPolicies(ctx)
	}()
}

func (r *Router) stopPolicies(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, r.policyTimeout)
	defer cancel()
	r.policyMu.Lock()
	if !r.policiesClosed {
		r.policiesClosed = true
		r.policyCancel()
	}
	r.policyMu.Unlock()
	if err := waitForPolicies(waitCtx, &r.policyWG); err != nil {
		return err
	}
	return waitForPolicies(waitCtx, &r.policyRunWG)
}

func waitForPolicies(ctx context.Context, group *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) sendPolicyMessage(ctx context.Context, message protocol.Envelope) error {
	if message.From == routerAuditSender {
		live, err := r.store.HasLiveMessage(ctx, message.From, message.To)
		if err != nil {
			return err
		}
		if live {
			return nil
		}
	}
	result, err := r.store.Enqueue(ctx, message)
	if err != nil {
		return err
	}
	r.afterEnqueue(result)
	return nil
}

func (r *Router) auditTmuxWindows(ctx context.Context, wanted string) ([]TmuxWindow, error) {
	if r.tmux == nil {
		return nil, ErrTmuxUnavailable
	}
	sessions, err := r.tmux.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		if session == wanted {
			return r.tmux.Windows(ctx, wanted)
		}
	}
	return nil, ErrTmuxUnavailable
}

const (
	tmuxRouterAuditPolicyName = "tmux_router_audit"
	routerAuditSender         = "router-audit"
)

// NewTmuxRouterAuditPolicy reports untracked tmux windows and live agents that
// have neither an active goal nor a scheduled recurring reminder.
func NewTmuxRouterAuditPolicy(supervisorTarget, tmuxSession string) NativePolicy {
	return NativePolicy{Name: tmuxRouterAuditPolicyName, Period: DefaultTmuxRouterAuditPeriod, Run: func(ctx context.Context, policy *PolicyContext) error {
		windows, err := policy.TmuxWindows(ctx, tmuxSession)
		if err != nil {
			return fmt.Errorf("tmux/router audit: %w", err)
		}
		reminders, err := policy.Reminders(ctx)
		if err != nil {
			return fmt.Errorf("tmux/router audit: %w", err)
		}
		agents, ok := policy.ConnectedAgents(ctx)["agents"].([]map[string]any)
		if !ok {
			return errors.New("tmux/router audit: live agent inventory is invalid")
		}
		live := make(map[string]map[string]any, len(agents))
		for _, agent := range agents {
			id, ok := agent["agent_id"].(string)
			if !ok || id == "" {
				return errors.New("tmux/router audit: live agent inventory is invalid")
			}
			live[id] = agent
		}
		recurring := make(map[string]bool, len(reminders))
		for _, reminder := range reminders {
			if isLiveRecurringReminder(reminder, live) {
				recurring[reminder.Target] = true
			}
		}
		unmapped := 0
		for _, window := range windows {
			if _, exists := live[window.Name]; !exists {
				unmapped++
			}
		}
		goals := 0
		covered := 0
		for _, agent := range agents {
			activeGoal := activeGoal(agent["goal"])
			if activeGoal {
				goals++
			}
			if activeGoal || recurring[agent["agent_id"].(string)] {
				covered++
			}
		}
		uncovered := len(agents) - covered
		if unmapped == 0 && uncovered == 0 {
			return nil
		}
		body := fmt.Sprintf("tmux/router audit: %d tmux windows, %d live agents. %d windows lack a live agent. %d agents lack an active goal or recurring reminder. Active goals: %d/%d. Recurring reminders: %d/%d.", len(windows), len(agents), unmapped, uncovered, goals, len(agents), len(recurring), len(agents))
		id, err := policy.NextID()
		if err != nil {
			return fmt.Errorf("tmux/router audit: %w", err)
		}
		subject := "tmux/router audit"
		if err := policy.Send(ctx, protocol.Envelope{ID: id, From: routerAuditSender, To: supervisorTarget, Subject: &subject, Body: body, CreatedAt: policy.Now()}); err != nil {
			return fmt.Errorf("tmux/router audit: %w", err)
		}
		return nil
	}}
}

func activeGoal(value any) bool {
	goal, ok := value.(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := goal["enabled"].(bool)
	text, _ := goal["goal"].(string)
	return enabled && strings.TrimSpace(text) != ""
}

func isLiveRecurringReminder(reminder domain.Reminder, live map[string]map[string]any) bool {
	return reminder.IntervalMS != nil && reminder.State == "scheduled" && live[reminder.Target] != nil
}
