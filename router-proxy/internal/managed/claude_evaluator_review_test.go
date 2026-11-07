package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

func TestGoalVerdictRequiresExactUnambiguousValue(t *testing.T) {
	for _, raw := range []string{"VERDICT: NOT COMPLETED", "VERDICT: INCOMPLETED", "Not completed", "VERDICT: COMPLETED OR NOT_DONE", "VERDICT: COMPLETED\nVERDICT: NOT_DONE"} {
		t.Run(raw, func(t *testing.T) { v, _ := parseClaudeGoalEvaluationResponse(raw); require.Empty(t, v) })
	}
	for _, tc := range []struct{ raw, want string }{{"VERDICT: COMPLETED\nREASON: done", "COMPLETED"}, {"VERDICT: NOT_DONE\nREASON: pending", "NOT_DONE"}, {"VERDICT: DONE", "COMPLETED"}} {
		v, _ := parseClaudeGoalEvaluationResponse(tc.raw)
		require.Equal(t, tc.want, v)
	}
}

func TestGoalContextCountsSystemPrompt(t *testing.T) {
	request := ai.Context{SystemPrompt: string(make([]byte, 40000)), Messages: []ai.Message{ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "x"}}}}}
	require.Greater(t, goalEvaluationContextTokens(request.SystemPrompt, request.Messages), coding.EstimateContextTokens(request.Messages))
}

func TestClaudePreflightReapsInheritedPipe(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30 &\necho '{\"loggedIn\":true}'\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := PreflightClaudeCode(ctx, ClaudeCodeProcessConfig{Binary: bin, Cwd: dir})
	require.NoError(t, err)
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestClaudeEvaluatorSafeDiagnostics(t *testing.T) {
	require.Contains(t, claudeEvaluatorDiagnostic(context.DeadlineExceeded), "timed out")
	require.Contains(t, claudeEvaluatorDiagnostic(errClaudeEvaluatorProcess), "CLI process failed")
	require.Contains(t, claudeEvaluatorDiagnostic(errClaudeEvaluatorOutput), "output exceeded")
	require.NotContains(t, claudeEvaluatorDiagnostic(errors.New("secret stderr")), "secret")
}

func TestClaudeEvaluatorMissingDefaultDiagnostic(t *testing.T) {
	require.Contains(t, claudeEvaluatorDiagnostic(errClaudeEvaluatorMissingDefault), "configure an authenticated router default")
}
