package managed

import (
	"context"
	"errors"
	"fmt"
	"time"

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
		item := map[string]string{"id": job.ID, "status": job.Status}
		if !job.StartedAt.IsZero() {
			item["started_at"] = job.StartedAt.Format(time.RFC3339Nano)
		}
		public = append(public, item)
	}
	return public
}

// BackgroundBashJobDetail reads one retained job for the current fenced owner.
func (m *Manager) BackgroundBashJobDetail(ctx context.Context, record *store.ManagedAgentRecord, id string) (BackgroundBashJob, error) {
	if m == nil || record == nil {
		return BackgroundBashJob{}, ErrBackgroundBashNotFound
	}
	m.mu.Lock()
	owner, ok := m.embeddedOwners[record.AgentID]
	jobs, closed := m.backgroundBash, m.closed
	m.mu.Unlock()
	if !ok || jobs == nil || closed || owner.binding.SessionID != record.PiSessionID || owner.binding.Epoch != record.SessionEpoch || owner.binding.Generation != record.RuntimeGeneration {
		return BackgroundBashJob{}, ErrBackgroundBashNotFound
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		if errors.Is(err, store.ErrStaleEmbeddedFence) {
			return BackgroundBashJob{}, ErrBackgroundBashNotFound
		}
		return BackgroundBashJob{}, err
	}
	job, err := jobs.Get(backgroundBashOwnerKey(owner), id)
	if err != nil {
		return BackgroundBashJob{}, err
	}
	m.mu.Lock()
	current, ok := m.embeddedOwners[record.AgentID]
	valid := ok && !m.closed && current.binding == owner.binding && current.identity == owner.identity
	m.mu.Unlock()
	if !valid {
		return BackgroundBashJob{}, ErrBackgroundBashNotFound
	}
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		if errors.Is(err, store.ErrStaleEmbeddedFence) {
			return BackgroundBashJob{}, ErrBackgroundBashNotFound
		}
		return BackgroundBashJob{}, err
	}
	job.Owner = ""
	job.Command = ""
	return job, nil
}
