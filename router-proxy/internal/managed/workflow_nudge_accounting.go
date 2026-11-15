package managed

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sky-valley/pi/ai"
)

var errWorkflowNudgeCap = errors.New("workflow unanswered nudge cap reached")

// goalContinuationLimit returns the configured maximum consecutive goal continuations
// or defaults to defaultMaxConsecutiveGoalContinuations (20). It consolidates the cap
// used across automatic goal continuations, workflow nudge accounting, and cap reconsiderations.
func (m *Manager) goalContinuationLimit() int64 {
	if m.maxConsecutiveGoalContinuations > 0 {
		return int64(m.maxConsecutiveGoalContinuations)
	}
	return defaultMaxConsecutiveGoalContinuations
}

type workflowDispatchDoer struct {
	before       func(context.Context) error
	refund       func()
	possiblySent func()
	inner        ai.HTTPDoer
}

func (d workflowDispatchDoer) Do(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if d.before != nil {
		if err := d.before(req.Context()); err != nil {
			return nil, err
		}
	}
	if err := req.Context().Err(); err != nil {
		if d.refund != nil {
			d.refund()
		}
		return nil, err
	}
	var gotConn atomic.Bool
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { gotConn.Store(true) }}
	resp, err := d.inner.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	// Dial/DNS errors before any connection are definite non-delivery. Other
	// failures remain uncertain even if an HTTP tracer was not called.
	certain := err != nil && resp == nil && !gotConn.Load() && isCertainPreSendFailure(err)
	if certain && d.refund != nil {
		d.refund()
	} else if d.possiblySent != nil {
		d.possiblySent()
	}
	return resp, err
}

func isCertainPreSendFailure(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	// Only a dial operation proves the failure happened before a connection.
	// Bare cancellation, DNS or socket errors from an arbitrary doer are not
	// proof that it did not send the request.
	return false
}

type workflowNudgeContextKey struct{}

func workflowDispatchRequest(ctx context.Context, inner ai.HTTPDoer) ai.HTTPDoer {
	observation, _ := ctx.Value(workflowNudgeContextKey{}).(*workflowNudgeObservation)
	if observation == nil {
		return inner
	}
	return workflowDispatchDoer{before: observation.dispatch, refund: observation.refund, possiblySent: observation.markDeliveryPossible, inner: inner}
}

type workflowNudgeObservation struct {
	mu               sync.Mutex
	lane             *managedAgentLane
	p                *embeddedPersistence
	limit            int64
	generation       uint64
	decided          bool
	workflow         bool
	responded        bool
	deliveryPossible bool
	wake             func()
	turnSeq          int64
	turnDigest       string
	workflowID       string
}

// dispatch runs immediately before provider submission. It is idempotent across
// multiple provider requests in the same SDK turn.
func (o *workflowNudgeObservation) dispatch(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.p.continuationKey != nil {
		identity, err := o.p.db.EmbeddedContinuationPromptIdentity(ctx, o.p.binding, *o.p.continuationKey)
		if err != nil {
			return err
		}
		if o.decided {
			return nil
		}
		o.workflow, o.workflowID = identity.Workflow, identity.WorkflowID
		if o.workflow {
			if o.p.turnSeq > 0 {
				o.turnSeq, o.turnDigest = o.p.turnSeq, o.p.turnDigest
			}
			o.generation = o.lane.beginWorkflowNudge()
			if !o.lane.dispatchWorkflowNudge(o.generation, o.limit) {
				return errWorkflowNudgeCap
			}
		}
		o.decided = true
		return nil
	}
	if o.decided {
		return nil
	}
	agent, err := o.p.db.ManagedAgent(ctx, o.p.binding.AgentID)
	if err != nil {
		return err
	}
	if agent == nil {
		return errors.New("workflow nudge agent missing")
	}
	if agent.PiSessionID != o.p.binding.SessionID || agent.SessionEpoch != o.p.binding.Epoch || agent.RuntimeGeneration != o.p.binding.Generation {
		return errors.New("stale workflow nudge binding")
	}
	o.workflow = agent.ActiveWorkflowID != "" || agent.ActiveWorkflowStateID != ""
	o.workflowID = agent.ActiveWorkflowID
	if o.workflow {
		if o.p.turnSeq > 0 {
			o.turnSeq, o.turnDigest = o.p.turnSeq, o.p.turnDigest
		}
		o.generation = o.lane.beginWorkflowNudge()
		if !o.lane.dispatchWorkflowNudge(o.generation, o.limit) {
			return errWorkflowNudgeCap
		}
	}
	o.decided = true
	return nil
}

func (o *workflowNudgeObservation) captureTurn() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.decided && o.workflow && o.turnSeq == 0 {
		o.turnSeq, o.turnDigest = o.p.turnSeq, o.p.turnDigest
	}
}

func (o *workflowNudgeObservation) observe(ctx context.Context, eligible bool) {
	if !eligible {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.decided || !o.workflow || o.responded {
		return
	}
	// This store operation checks owner, token and the exact currently bound user.
	if o.p.executionToken <= 0 || o.turnSeq <= 0 || o.turnDigest == "" || o.p.turnSeq != o.turnSeq || o.p.turnDigest != o.turnDigest {
		return
	}
	// The store transaction linearizes validation and reset with cross-manager
	// owner revocation and workflow mutation. The callback only takes the
	// bounded accounting mutex and never reenters the store.
	err := o.p.db.WithCurrentEmbeddedWorkflowResponse(ctx, o.p.binding, o.p.executionToken, o.turnSeq, o.turnDigest, o.workflowID, func() {
		o.responded, o.wake = o.lane.respondWorkflowNudgeAndWake(o.generation)
	})
	if err != nil {
		log.Printf("[embedded:%s] workflow response authority: %v", o.p.binding.AgentID, err)
	}
	if o.wake != nil {
		o.wake()
		o.wake = nil
	}
}

func (o *workflowNudgeObservation) observePi(ctx context.Context, event ai.AssistantMessageEvent) {
	eligible := false
	o.mu.Lock()
	switch event.Type {
	case ai.EventTextDelta:
		eligible = strings.TrimSpace(event.Delta) != ""
	case ai.EventTextEnd:
		eligible = strings.TrimSpace(event.Content) != ""
	case ai.EventToolCallEnd:
		eligible = event.ToolCall != nil && strings.TrimSpace(event.ToolCall.ID) != "" && strings.TrimSpace(event.ToolCall.Name) != ""
	}
	o.mu.Unlock()
	o.observe(ctx, eligible)
}

func (o *workflowNudgeObservation) observeClaude(ctx context.Context, msg *ai.AssistantMessage) {
	if msg == nil {
		return
	}
	for _, part := range msg.Content {
		switch item := part.(type) {
		case ai.TextContent:
			if strings.TrimSpace(item.Text) != "" {
				o.observe(ctx, true)
				return
			}
		case ai.ToolCall:
			if strings.TrimSpace(item.ID) != "" && strings.TrimSpace(item.Name) != "" {
				o.observe(ctx, true)
				return
			}
		}
	}
}

// Workflow response callbacks run inside an active turn, whose caller holds
// lane.serial. Keep the accounting lock independent and never take serial here.
func (lane *managedAgentLane) beginWorkflowNudge() uint64 {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	lane.workflowNudgeGeneration++
	lane.workflowNudgeDispatched = false
	lane.workflowNudgeResponded = false
	return lane.workflowNudgeGeneration
}

func (lane *managedAgentLane) dispatchWorkflowNudge(generation uint64, limit int64) bool {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	if generation != lane.workflowNudgeGeneration || lane.workflowNudgeDispatched || lane.workflowUnanswered.Load() >= limit {
		return false
	}
	lane.workflowNudgeDispatched = true
	lane.workflowUnanswered.Add(1)
	return true
}

func (lane *managedAgentLane) respondWorkflowNudge(generation uint64) bool {
	accepted, wake := lane.respondWorkflowNudgeAndWake(generation)
	if wake != nil {
		wake()
	}
	return accepted
}

func (lane *managedAgentLane) respondWorkflowNudgeAndWake(generation uint64) (bool, func()) {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	if generation != lane.workflowNudgeGeneration || !lane.workflowNudgeDispatched || lane.workflowNudgeResponded {
		return false, nil
	}
	lane.workflowNudgeResponded = true
	lane.workflowUnanswered.Store(0)
	lane.consecutiveContinuations.Store(0)
	wake := lane.workflowCapWake
	lane.workflowCapWake = nil
	return true, wake
}

func (lane *managedAgentLane) reconsiderWorkflowCap(limit int64, wake func()) bool {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	if lane.consecutiveContinuations.Load() < limit && lane.workflowUnanswered.Load() < limit {
		return false
	}
	lane.workflowCapWake = wake
	return true
}

func (o *workflowNudgeObservation) markDeliveryPossible() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deliveryPossible = true
}

func (o *workflowNudgeObservation) refund() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.workflow && o.decided && !o.responded && !o.deliveryPossible {
		o.lane.refundWorkflowNudge(o.generation)
		o.decided = false
	}
}

func (lane *managedAgentLane) refundWorkflowNudge(generation uint64) {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	if generation == lane.workflowNudgeGeneration && lane.workflowNudgeDispatched && !lane.workflowNudgeResponded {
		lane.workflowNudgeDispatched = false
		lane.workflowUnanswered.Add(-1)
	}
}

func (lane *managedAgentLane) resetWorkflowNudgeAccounting() {
	lane.workflowAccountingMu.Lock()
	defer lane.workflowAccountingMu.Unlock()
	lane.workflowNudgeGeneration++
	lane.workflowNudgeDispatched = false
	lane.workflowNudgeResponded = false
	lane.workflowUnanswered.Store(0)
	lane.workflowCapWake = nil
}
