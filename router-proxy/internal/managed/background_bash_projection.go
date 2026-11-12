package managed

import (
	"context"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// BackgroundBashJobs returns only jobs belonging to this manager's current
// fenced binding. Browser projection does not spend the tool read budget.
func (m *Manager) BackgroundBashJobs(record *store.ManagedAgentRecord) []map[string]string {
	return m.BackgroundBashJobsContext(context.Background(), record)
}

func (m *Manager) BackgroundBashJobsContext(ctx context.Context, record *store.ManagedAgentRecord) []map[string]string {
	result, _ := m.backgroundBashJobsContext(ctx, record, nil)
	return result
}

func (m *Manager) BackgroundBashJobsSnapshot(ctx context.Context, record *store.ManagedAgentRecord) ([]map[string]string, error) {
	return m.backgroundBashJobsContext(ctx, record, nil)
}

func (m *Manager) backgroundBashJobsContext(ctx context.Context, record *store.ManagedAgentRecord, beforeRead func()) ([]map[string]string, error) {
	if m == nil || record == nil {
		return backgroundBashPublicJobs(nil), nil
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Failed or stopped agents have no current owner to project. The manager
	// may still retain their old owner for cleanup; validating it would turn
	// one failed agent into an error for the entire agent list.
	if record.Runtime != "embedded" || record.DesiredState != "running" || (record.LifecycleState != "starting" && record.LifecycleState != "running") {
		return backgroundBashPublicJobs(nil), nil
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[record.AgentID]
	jobs, closed := m.backgroundBash, m.closed
	m.mu.Unlock()
	if !ok || jobs == nil || closed || owner.binding.SessionID != record.PiSessionID || owner.binding.Epoch != record.SessionEpoch || owner.binding.Generation != record.RuntimeGeneration {
		return backgroundBashPublicJobs(nil), nil
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return nil, err
	}
	if beforeRead != nil {
		beforeRead()
	}
	result := jobs.projectionJobs(backgroundBashOwnerKey(owner))
	m.mu.Lock()
	current, ok := m.embeddedOwners[record.AgentID]
	valid := ok && !m.closed && current.binding == owner.binding && current.identity == owner.identity
	m.mu.Unlock()
	if !valid {
		return nil, fmt.Errorf("background bash owner changed during snapshot")
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return nil, err
	}
	return backgroundBashPublicJobs(result), nil
}

// backgroundBashPublicJobs deliberately excludes command, output and ownership
// metadata from browser-facing projections. The caller must supply only jobs
// owned by the currently fenced managed binding.
func backgroundBashPublicJobs(jobs []BackgroundBashJob) []map[string]string {
	public := make([]map[string]string, 0, len(jobs))
	for _, job := range jobs {
		public = append(public, map[string]string{"id": job.ID, "status": job.Status})
	}
	return public
}
