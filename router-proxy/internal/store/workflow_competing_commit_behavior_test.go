package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// IR-012: both callers capture the same persisted binding before either is
// allowed to commit. Exactly one may replace it, regardless of scheduling.
func TestWorkflowCompetingTransitionsSameSnapshot(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "competition"})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID})
	require.NoError(t, err)
	c, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "C", WorkflowID: &wf.ID})
	require.NoError(t, err)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []WorkflowTransitionConfig{{TargetStateID: b.ID}, {TargetStateID: c.ID}}})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &a.ID, nil))
	type result struct {
		target string
		err    error
	}
	ready := make(chan error, 2)
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, target := range []string{b.ID, c.ID} {
		go func(target string) {
			snapshot, e := s.WorkflowBindingVersion(ctx, agent.AgentID)
			ready <- e
			<-start
			if e == nil {
				_, e = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, snapshot.Generation, a.ID, target)
			}
			results <- result{target, e}
		}(target)
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, <-ready)
	}
	close(start)
	var winner string
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err == nil {
				require.Empty(t, winner)
				winner = r.target
			} else {
				require.ErrorContains(t, r.err, "workflow binding changed")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("competing transition did not finish")
		}
	}
	require.NotEmpty(t, winner)
	current, err := s.GetManagedAgentActiveWorkflowState(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, winner, current.ID)
	snapshot, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, a.ID, snapshot.PredecessorID)
}

// IR-012: a transition already holding a snapshot is delayed while the real
// assignment API replaces the binding, including same-value and ABA changes.
func TestWorkflowStaleTransitionAfterReassignment(t *testing.T) {
	for _, mode := range []string{"same-value", "aba", "deactivate"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, agent := readinessSourceFixture(t)
			wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "reassignment"})
			require.NoError(t, err)
			b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID})
			require.NoError(t, err)
			a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []WorkflowTransitionConfig{{TargetStateID: b.ID}}})
			require.NoError(t, err)
			require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &a.ID, nil))
			observed := make(chan error, 1)
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				snap, e := s.WorkflowBindingVersion(ctx, agent.AgentID)
				observed <- e
				<-release
				if e == nil {
					_, e = s.AuthorizeWorkflowTransition(ctx, agent.AgentID, snap.Generation, a.ID, b.ID)
				}
				done <- e
			}()
			require.NoError(t, <-observed)
			if mode == "aba" {
				require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &b.ID, nil))
			}
			if mode == "deactivate" {
				err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, nil, nil)
			} else {
				err = s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &a.ID, nil)
			}
			require.NoError(t, err)
			before, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
			require.NoError(t, err)
			close(release)
			select {
			case err := <-done:
				require.ErrorContains(t, err, "workflow binding changed")
			case <-time.After(5 * time.Second):
				t.Fatal("stale transition did not finish")
			}
			after, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			current, err := s.GetManagedAgentActiveWorkflowState(ctx, agent.AgentID)
			require.NoError(t, err)
			if mode == "deactivate" {
				require.Nil(t, current)
			} else {
				require.NotNil(t, current)
				require.Equal(t, a.ID, current.ID)
			}
		})
	}
}
