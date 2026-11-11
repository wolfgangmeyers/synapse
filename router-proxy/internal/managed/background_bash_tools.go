package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func isBackgroundBashTool(name string) bool {
	switch name {
	case "launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job":
		return true
	}
	return false
}

func backgroundBashOwnerKey(owner embeddedOwner) string {
	b := owner.binding
	data, _ := json.Marshal([]any{b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken, owner.identity})
	return string(data)
}

func (m *Manager) backgroundBashTools(owner embeddedOwner, cwd string) []agent.AgentTool {
	return m.backgroundBashToolsForOwner(owner, cwd, owner.binding.AgentID, func(ctx context.Context) error {
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
		if err != nil {
			return err
		}
		if role != "root" && role != "worker" {
			return errors.New("background bash requires embedded root or worker")
		}
		return nil
	})
}

func (m *Manager) backgroundBashToolsForOwner(owner embeddedOwner, cwd, laneID string, check func(context.Context) error) []agent.AgentTool {
	m.ensureBackgroundBash()
	closed := false
	launchSchema := ai.Object(ai.Prop("command", ai.String("Shell command.")), ai.Opt("timeout", ai.Integer("Timeout in seconds; default 600.")))
	launchSchema.AdditionalAllowed = &closed
	listSchema := ai.Object()
	listSchema.AdditionalAllowed = &closed
	idSchema := ai.Object(ai.Prop("job_id", ai.String("Owned job ID.")))
	idSchema.AdditionalAllowed = &closed
	tools := []agent.AgentTool{}
	for _, def := range []struct {
		name, description string
		schema            *ai.Schema
	}{
		{"launch_background_bash", "Start a background shell command.", launchSchema},
		{"list_background_bash_jobs", "List owned background job metadata; use get to read output.", listSchema},
		{"get_background_bash_job", "Get an owned background job snapshot.", idSchema},
		{"kill_background_bash_job", "Stop an owned background job.", idSchema},
	} {
		def := def
		tools = append(tools, agent.AgentTool{Name: def.name, Description: def.description, Parameters: def.schema, Execute: func(ctx context.Context, _ string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if err := check(ctx); err != nil {
				return agent.AgentToolResult{}, err
			}
			var err error
			if !def.schema.Check(args) {
				return agent.AgentToolResult{}, errors.New("invalid background bash arguments")
			}
			m.mu.Lock()
			jobs := m.backgroundBash
			m.mu.Unlock()
			if jobs == nil {
				return agent.AgentToolResult{}, errors.New("background bash unavailable")
			}
			key := backgroundBashOwnerKey(owner)
			var value any
			switch def.name {
			case "launch_background_bash":
				command, ok := args["command"].(string)
				if !ok || strings.TrimSpace(command) == "" || len(command) > 65536 {
					return agent.AgentToolResult{}, errors.New("invalid bash command")
				}
				timeout := int64(0)
				if v, ok := args["timeout"]; ok {
					switch n := v.(type) {
					case float64:
						if math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > float64(math.MaxInt64/int64(time.Second)) || math.Trunc(n) != n {
							return agent.AgentToolResult{}, errors.New("timeout must be a positive integer in seconds")
						}
						timeout = int64(n)
					case int:
						timeout = int64(n)
					case int64:
						timeout = n
					default:
						return agent.AgentToolResult{}, errors.New("timeout must be a positive integer in seconds")
					}
					if timeout < 1 || timeout > math.MaxInt64/int64(time.Second) {
						return agent.AgentToolResult{}, errors.New("timeout must be a positive integer in seconds")
					}
				}
				value, err = owner.backgroundBash.launchContext(ctx, func() (BackgroundBashJob, error) {
					return jobs.Launch(key, BackgroundBashLaunch{Command: command, Dir: cwd, Env: os.Environ(), TimeoutSeconds: timeout, claudeOrphanRoot: owner.claudeOrphanRoot})
				})
			case "list_background_bash_jobs":
				if err = m.laneFor(laneID).consumeBackgroundBashReadCredit(time.Now()); err == nil {
					snapshots := jobs.List(key)
					out := make([]any, 0, len(snapshots))
					for _, job := range snapshots {
						out = append(out, backgroundBashWireMetadata(job))
					}
					value = out
				}
			case "get_background_bash_job", "kill_background_bash_job":
				id, ok := args["job_id"].(string)
				if !ok || strings.TrimSpace(id) == "" {
					return agent.AgentToolResult{}, errors.New("job_id is required")
				}
				var job BackgroundBashJob
				if def.name == "get_background_bash_job" {
					// Inspect the owned snapshot first: terminal gets are free, while
					// unresolved gets must acquire credit before returning the snapshot.
					job, err = jobs.Get(key, id)
					if err == nil && (job.Status == "running" || job.Status == "uncertain") {
						err = m.laneFor(laneID).consumeBackgroundBashReadCredit(time.Now())
					}
				} else {
					job, err = jobs.Kill(key, id)
				}
				if err == nil {
					value = backgroundBashWireJob(job)
				}
			}
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			if job, ok := value.(BackgroundBashJob); ok {
				value = backgroundBashWireJob(job)
			}
			body, err := json.Marshal(value)
			if err != nil {
				return agent.AgentToolResult{}, fmt.Errorf("encode background job: %w", err)
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(body)}}}, nil
		}})
	}
	return tools
}

func backgroundBashWireMetadata(job BackgroundBashJob) map[string]any {
	snapshot := backgroundBashWireJob(job)
	delete(snapshot, "output")
	return snapshot
}

func backgroundBashWireJob(job BackgroundBashJob) map[string]any {
	return map[string]any{"job_id": job.ID, "status": job.Status, "output": job.Output, "truncated": job.Truncated, "exit_code": job.ExitCode, "started_at": job.StartedAt, "deadline": job.Deadline, "finished_at": job.FinishedAt, "error": job.Error}
}

func (m *Manager) ensureBackgroundBash() {
	m.mu.Lock()
	if m.backgroundBash == nil && !m.closed {
		m.backgroundBash = NewBackgroundBashManager(m.backgroundBashCompleted)
		m.backgroundBash.SetChangeCallbackContext(m.backgroundBashChanged)
	}
	m.mu.Unlock()
}

func (m *Manager) backgroundBashCurrentOwner(key string) (embeddedOwner, bool) {
	return m.backgroundBashOwner(key, false)
}

func (m *Manager) backgroundBashOwner(key string, duringClose bool) (embeddedOwner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed && !duringClose {
		return embeddedOwner{}, false
	}
	for _, owner := range m.embeddedOwners {
		if backgroundBashOwnerKey(owner) == key {
			return owner, true
		}
	}
	return embeddedOwner{}, false
}

func (m *Manager) backgroundBashChanged(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if _, err := m.store.ReserveProjectionVersion(ctx); err != nil {
		return err
	}
	return m.publishProjectionContext(ctx)
}

func (m *Manager) backgroundBashCompleted(job BackgroundBashJob) {
	m.enqueueBackgroundBashCompletion(job, nil)
}

// options is used by the SQLite fault regression to inject a post-commit failure.
func (m *Manager) enqueueBackgroundBashCompletion(job BackgroundBashJob, admission func() (func(bool), error)) {
	owner, ok := m.backgroundBashOwner(job.Owner, true)
	if !ok {
		return
	}
	// Shutdown cancels runCtx before joining job callbacks. The store still
	// fences the owner atomically; a bounded independent context lets the
	// ordinary admission attempt run while shutdown joins it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := fmt.Sprintf("Background bash job %s finished: %s", job.ID, job.Status)
	if job.ExitCode != nil {
		body += fmt.Sprintf(" (exit code %d)", *job.ExitCode)
	}
	subject := "[System Notice] Background Bash Completed"
	source := protocol.MessageSource{Kind: "system"}
	finished := job.FinishedAt
	if finished.IsZero() {
		finished = m.clock.Now()
	}
	noticeID := "background-bash-completed-" + job.ID
	if job.Status == "uncertain" {
		noticeID += "-uncertain"
	}
	envelope := protocol.Envelope{ID: noticeID, From: "system", To: owner.binding.AgentID, Subject: &subject, Body: body, CreatedAt: formatManagedTime(finished), Source: &source}
	options := store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &body, Source: &source}, OnFreshAdmission: admission}
	result, err := m.store.EnqueueEmbeddedOwnerNotification(ctx, owner.binding, owner.identity, envelope, options)
	if err != nil {
		log.Printf("[embedded:%s] background bash completion notice %s failed: %v", owner.binding.AgentID, job.ID, err)
		return
	}
	m.mu.Lock()
	publish := m.embeddedMailPublisher
	m.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if publish != nil {
		publish(result)
	} else if _, err := m.store.ResolveMessageTarget(ctx, result.Message.ID); err != nil {
		log.Printf("[embedded:%s] background bash completion notice target %s failed: %v", owner.binding.AgentID, job.ID, err)
	}
	m.OfferDirect(owner.binding.AgentID)
}

// Some narrowly constructed manager fixtures have no run context.
func (m *Manager) backgroundBashContext() context.Context {
	if m.runCtx != nil {
		return m.runCtx
	}
	return context.Background()
}
