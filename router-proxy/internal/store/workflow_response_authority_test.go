package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestWorkflowResponseAuthoritySerializesRevocation(t *testing.T) {
	ctx := context.Background()
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "response"})
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &state.ID, nil))
	userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "nudge"}}})
	require.NoError(t, err)
	user, err := s.AppendEmbeddedEntry(ctx, b, "user", userBody)
	require.NoError(t, err)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	entered, release := make(chan struct{}), make(chan struct{})
	accepted := make(chan error, 1)
	go func() {
		accepted <- s.WithCurrentEmbeddedWorkflowResponse(ctx, b, token, user.Sequence, user.Digest, wf.ID, func() { close(entered); <-release })
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { _, err := s.AcquireEmbeddedOwner(ctx, b, "successor", time.Minute); revoked <- err }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation overtook response callback: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-accepted)
	require.NoError(t, <-revoked)
	called := false
	require.Error(t, s.WithCurrentEmbeddedWorkflowResponse(ctx, b, token, user.Sequence, user.Digest, wf.ID, func() { called = true }))
	require.False(t, called)
}

func TestWorkflowResponseAuthorityCommitFailureDoesNotAccept(t *testing.T) {
	ctx := context.Background()
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "commit"})
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &state.ID, nil))
	user, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"nudge"}`))
	require.NoError(t, err)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	s.workflowResponseCommitFault = func() error { return errors.New("commit injected") }
	called := false
	require.ErrorContains(t, s.WithCurrentEmbeddedWorkflowResponse(ctx, b, token, user.Sequence, user.Digest, wf.ID, func() { called = true }), "commit injected")
	require.False(t, called)
}

func TestWorkflowResponseAuthorityRollbackFailureReturned(t *testing.T) {
	ctx := context.Background()
	s, b, _ := evaluationFixture(t)
	defer s.Close()
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "rollback"})
	require.NoError(t, err)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &state.ID, nil))
	user, err := s.AppendEmbeddedEntry(ctx, b, "user", []byte(`{"role":"user","content":"work"}`))
	require.NoError(t, err)
	token, err := s.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	require.NoError(t, s.SetEmbeddedSDKExecutionUser(ctx, b, token, user.Sequence, user.Digest))
	s.workflowResponseRollbackFault = func() error { return errors.New("rollback injected") }
	called := false
	err = s.WithCurrentEmbeddedWorkflowResponse(ctx, b, token, user.Sequence, user.Digest, wf.ID, func() { called = true })
	require.True(t, called)
	require.ErrorContains(t, err, "rollback injected")
	// The injected failure occurs instead of ROLLBACK, leaving a real SQLite
	// transaction active until the store closes its connection.
	require.ErrorContains(t, s.WithCurrentEmbeddedWorkflowResponse(ctx, b, token, user.Sequence, user.Digest, wf.ID, func() {}), "store is closed")
	other, err := sql.Open("sqlite", s.path)
	require.NoError(t, err)
	defer other.Close()
	_, err = other.ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err, "failed rollback must not leak the SQLite writer lock")
	_, err = other.ExecContext(ctx, "ROLLBACK")
	require.NoError(t, err)
}
