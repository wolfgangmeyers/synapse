package managed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/coding"
)

// Fixed v1 policy. Output is capped at 4096; the remaining reserve covers tool
// continuations and token-estimation uncertainty. No per-agent threshold.
const embeddedContextHeadroom = 16384
const embeddedOrdinaryMaxTokens = 4096

func embeddedProjectedTokens(messages []agent.AgentMessage) int {
	return coding.EstimateContextTokens(ai.NormalizeContext(ai.Context{Messages: messages}).Messages)
}

// Replay is SDK-owned. This temporary projector loads the latest checked
// checkpoint, but Enabled:false installs ONLY permanent checkpoint replay.
// Automatic SDK summarization and its CommitCheckpoint fallback stay inactive.
func embeddedCheckedContext(ctx context.Context, p *embeddedPersistence, model *ai.Model, messages []agent.AgentMessage) ([]agent.AgentMessage, error) {
	projector := coding.NewSession(coding.SessionOptions{SessionID: p.binding.SessionID, Model: model, Tools: []agent.AgentTool{}, OrderedPersistence: p})
	reader := &embeddedPersistence{db: p.db, binding: p.binding, digest: "genesis"}
	if err := reader.restore(ctx, projector); err != nil {
		return nil, err
	}
	if reader.seq != p.seq || reader.digest != p.digest || p.seq != int64(len(messages)) {
		return nil, errors.New("compaction ordered context head mismatch")
	}
	projector.EnableCompaction(coding.CompactionSettings{Enabled: false})
	return projector.Agent.TransformContext(ctx, messages), nil
}

// Retain the current user turn, or the latest assistant call plus its checked
// tool results. A call/result pair is never split by automatic compaction.
func embeddedAutomaticCut(messages []agent.AgentMessage) int64 {
	toolContinuation := len(messages) > 0 && messages[len(messages)-1].MessageRole() == ai.RoleToolResult
	for i := len(messages) - 1; i >= 0; i-- {
		if toolContinuation && messages[i].MessageRole() == ai.RoleAssistant {
			return int64(i + 1)
		}
		if messages[i].MessageRole() == ai.RoleUser {
			return int64(i + 1)
		}
	}
	return 0
}

// The summarizer uses the replayable view (including a previous summary), not
// a second history store. Map its kept suffix back to immutable SQLite sequence
// numbers and commit that exact current head before publishing any replacement.
func (m *Manager) compactEmbeddedAtProvider(ctx context.Context, p *embeddedPersistence, owner embeddedOwner, model *ai.Model, stream agent.StreamFn, messages, view []agent.AgentMessage, id string, firstKept int64) ([]agent.AgentMessage, error) {
	if firstKept < 2 || firstKept > int64(len(messages)) {
		return nil, errors.New("automatic compaction has no safely summarizable prefix")
	}
	target, err := json.Marshal(messages[firstKept-1])
	if err != nil {
		return nil, err
	}
	keptIndex := -1
	for i := len(view) - 1; i >= 0; i-- {
		body, err := json.Marshal(view[i])
		if err != nil {
			return nil, err
		}
		if bytes.Equal(body, target) {
			keptIndex = i
			break
		}
	}
	if keptIndex < 1 {
		return nil, errors.New("compaction kept sequence unavailable in replayable context")
	}
	auxiliary := func(requestCtx context.Context, requestModel *ai.Model, h ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		// SDK summary limits derive from model metadata, independently of the
		// ordinary session cap. Preserve that identity and cap a copy of options.
		if opts == nil || opts.MaxTokens == nil || *opts.MaxTokens < 1 {
			return ai.ErrorStream(requestModel, errors.New("compaction summary request cannot fit selected model"))
		}
		capped := *opts
		maxTokens := min(*opts.MaxTokens, embeddedOrdinaryMaxTokens)
		capped.MaxTokens = &maxTokens
		opts = &capped
		if requestModel == nil || requestModel.ContextWindow <= 0 || embeddedProjectedTokens(h.Messages) > requestModel.ContextWindow-maxTokens {
			return ai.ErrorStream(requestModel, errors.New("compaction summary request cannot fit selected model"))
		}
		return stream(context.WithValue(requestCtx, embeddedAuxiliaryRequestKey{}, true), requestModel, h, opts)
	}
	summarizer := coding.NewSession(coding.SessionOptions{SessionID: p.binding.SessionID, Model: model, Tools: []agent.AgentTool{}, OrderedPersistence: p, StreamFn: auxiliary})
	seq, digest := p.seq, p.digest
	return summarizer.CompactOrderedAtProvider(ctx, view, int64(keptIndex+1), func(checkpoint coding.OrderedCheckpoint) error {
		checkpoint.ThroughSequence = seq
		checkpoint.FirstKeptSequence = firstKept
		// Independently enforce the host's fixed policy BEFORE the durable commit;
		// SDK default settings do not govern admission or a truthful fit claim.
		projection := coding.OrderedSnapshot{SessionID: p.binding.SessionID, Checkpoint: &checkpoint}
		for i, message := range messages {
			projection.Messages = append(projection.Messages, coding.OrderedMessage{Sequence: int64(i + 1), ParentSequence: int64(i), Message: message})
		}
		candidate := coding.NewSession(coding.SessionOptions{SessionID: p.binding.SessionID, Model: model, Tools: []agent.AgentTool{}})
		if err := candidate.RestoreOrdered(ctx, projection); err != nil {
			return err
		}
		candidate.EnableCompaction(coding.CompactionSettings{Enabled: false})
		projected := candidate.Agent.TransformContext(ctx, messages)
		if embeddedProjectedTokens(projected) >= embeddedProjectedTokens(view) {
			return errors.New("compacted context did not reduce projected token estimate")
		}
		if embeddedProjectedTokens(projected) > model.ContextWindow-embeddedContextHeadroom {
			return errors.New("compacted context has insufficient fixed headroom")
		}
		if err := m.store.RenewEmbeddedOwner(ctx, p.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return err
		}
		return m.store.CommitEmbeddedOwnCompactionCheckpoint(ctx, p.binding, id, seq, digest, checkpoint)
	})
}

func unwrapEmbeddedMailText(text string) string {
	if !strings.Contains(text, `"Version":"embedded-mail/v1"`) && !strings.Contains(text, `"Version": "embedded-mail/v1"`) {
		return text
	}
	var mailEnv struct {
		Sender string `json:"Sender"`
		Body   string `json:"Body"`
	}
	if err := json.Unmarshal([]byte(text), &mailEnv); err == nil && mailEnv.Sender != "" {
		return fmt.Sprintf("[Direct message from %s]: %s", mailEnv.Sender, mailEnv.Body)
	}
	return text
}

func (m *Manager) fitEmbeddedContext(ctx context.Context, p *embeddedPersistence, owner embeddedOwner, model *ai.Model, stream agent.StreamFn, messages []agent.AgentMessage) ([]agent.AgentMessage, error) {
	if model == nil || model.ContextWindow <= embeddedContextHeadroom {
		return nil, errors.New("selected model window unknown or insufficient for fixed headroom")
	}
	view, err := embeddedCheckedContext(ctx, p, model, messages)
	if err != nil {
		return nil, err
	}
	id := p.pendingCompaction
	if id != "" {
		p.pendingCompaction = "" // Any failure is UNKNOWN, never replay permission.
		if err := m.store.ClaimEmbeddedOwnCompaction(ctx, p.binding, id, p.seq, p.digest); err != nil {
			return nil, err
		}
		view, err = m.compactEmbeddedAtProvider(ctx, p, owner, model, stream, messages, view, id, p.seq-1)
	} else if embeddedProjectedTokens(view) > model.ContextWindow-embeddedContextHeadroom {
		cut := embeddedAutomaticCut(messages)
		if cut < 2 {
			return nil, errors.New("automatic compaction has no safely summarizable prefix")
		}
		id = fmt.Sprintf("auto-compact:%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s:%d:%s", p.binding.AgentID, p.binding.SessionID, p.binding.Epoch, p.binding.Generation, p.seq, p.digest))))
		if err := m.store.ClaimEmbeddedAutomaticCompaction(ctx, p.binding, id, p.seq, p.digest); err != nil {
			return nil, err
		}
		view, err = m.compactEmbeddedAtProvider(ctx, p, owner, model, stream, messages, view, id, cut)
	}
	if err != nil {
		return nil, err
	}
	if embeddedProjectedTokens(view) > model.ContextWindow-embeddedContextHeadroom {
		return nil, errors.New("compacted context has insufficient fixed headroom")
	}
	for i, msg := range view {
		if um, ok := msg.(ai.UserMessage); ok {
			var newContent ai.ContentList
			changed := false
			for _, part := range um.Content {
				if tc, ok := part.(ai.TextContent); ok {
					unwrapped := unwrapEmbeddedMailText(tc.Text)
					if unwrapped != tc.Text {
						newContent = append(newContent, ai.TextContent{Text: unwrapped})
						changed = true
						continue
					}
				}
				newContent = append(newContent, part)
			}
			if changed {
				copyMsg := um
				copyMsg.Content = newContent
				view[i] = copyMsg
			}
		}
	}
	return view, nil
}
