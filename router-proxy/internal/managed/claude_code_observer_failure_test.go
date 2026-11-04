package managed

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// Regression: an intent observer failure must stop a second already-produced
// call, not merely reject the first call's eventual checked result.
func TestClaudeIntentObserverFailureStopsLaterCall(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("intent barrier failed")
	failure := &embeddedEffectObserverError{cause: sentinel}
	var latched error
	admissions, executions, fatals := 0, 0, 0
	authority := &embeddedTurnToolAuthority{
		observerFailure: func() error { return latched },
		beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error {
			admissions++
			if admissions == 1 {
				latched = failure
				return failure
			}
			return errors.New("later admission reached")
		},
		tools: []agent.AgentTool{{Name: "mecha_mailbox_send", Execute: func(context.Context, string, map[string]any, agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			executions++
			return agent.AgentToolResult{}, errors.New("domain reached")
		}}},
	}
	bridge := newClaudeCodeTurnBridge(authority)
	bridge.admitted = true
	bridge.fatal = func(err error) {
		fatals++
		if !errors.Is(err, sentinel) {
			t.Errorf("wrong fatal: %v", err)
		}
	}
	for _, id := range []string{"first", "second"} {
		if err := bridge.producer(ctx, ai.ToolCall{ID: id, Name: "mecha_mailbox_send", Arguments: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"first", "second"} {
		if _, err := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: id, Name: "mecha_mailbox_send", Arguments: json.RawMessage(`{}`)}); err == nil {
			t.Fatal("call succeeded after observer failure")
		}
	}
	if !errors.Is(bridge.err(), sentinel) || fatals != 1 || admissions != 1 || executions != 0 {
		t.Fatalf("failure=%v fatal=%d admissions=%d executions=%d", bridge.err(), fatals, admissions, executions)
	}
	if err := authority.BeforeToolExecute(ctx, agent.BeforeToolCallContext{}); !errors.Is(err, sentinel) {
		t.Fatalf("later authority admission: %v", err)
	}
	if err := bridge.producer(ctx, ai.ToolCall{ID: "late"}); err == nil {
		t.Fatal("producer accepted after fatal")
	}
}

func TestEmbeddedLatchedObserverRejectsAdmissionBeforeCallback(t *testing.T) {
	sentinel := &embeddedEffectObserverError{cause: errors.New("observer failed")}
	authority := &embeddedTurnToolAuthority{observerFailure: func() error { return sentinel }, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error {
		t.Fatal("admission callback reached")
		return nil
	}}
	if err := authority.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
