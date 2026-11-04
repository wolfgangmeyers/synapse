package managed

import (
	"context"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeHeadroomReopenIgnoresZeroUsage(t *testing.T) {
	r, p, pool, cfg := claudeRuntimeFixture(t, `{}`)
	ctx := context.Background()
	valid := r.emptyAssistant()
	valid.Usage.Input = 123
	valid.Usage.CacheRead = 20
	require.NoError(t, p.AppendCheckedMessage(ctx, *valid))
	notice := r.emptyAssistant()
	notice.StopReason = ai.StopToolUse
	require.NoError(t, p.AppendCheckedMessage(ctx, *notice))
	next, err := newClaudeCodeRuntime(ctx, claudeCodeRuntimeOptions{Pool: pool, Config: cfg, Persistence: p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	require.Equal(t, 143, next.Snapshot().ContextTokens)
}

func TestClaudeHeadroomRetryCountsInputOnce(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"rate_limit":{"resetsAt":1}}],[{"text":"done"}]]}`)
	before := r.beforeSubmit
	calls := 0
	r.beforeSubmit = func(ctx context.Context, size int) error {
		calls++
		if calls == 2 {
			r.mu.Lock()
			r.snapshot.ContextWindow = embeddedContextHeadroom + size
			r.mu.Unlock()
		}
		return before(ctx, size)
	}
	_, err := r.Run(context.Background(), "limited")
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}

func TestClaudeHeadroomRetryRejectionIsPostConsumption(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"rate_limit":{"resetsAt":1}}]]}`)
	after := r.afterEcho
	r.afterEcho = func(ctx context.Context) error {
		err := after(ctx)
		r.mu.Lock()
		r.snapshot.ContextWindow = embeddedContextHeadroom
		r.mu.Unlock()
		return err
	}
	_, err := r.Run(context.Background(), "limited")
	require.ErrorContains(t, err, "headroom")
	require.NotErrorIs(t, err, errClaudeInputBounds)
}

func TestClaudeHeadroomLiveZeroUsageDoesNotErasePriorUsage(t *testing.T) {
	r, _, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"tool":{"id":"t1","name":"Read","input":{},"result":"ok"}},{"emit":{"type":"assistant","message":{"id":"zero","role":"assistant","model":"sonnet","content":[{"type":"text","text":"error"}],"usage":{}}}}]]}`)
	_, err := r.Run(context.Background(), "test")
	require.NoError(t, err)
	require.Equal(t, 150, r.Snapshot().ContextTokens)
}
