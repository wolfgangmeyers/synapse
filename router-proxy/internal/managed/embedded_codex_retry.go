package managed

import (
	"context"

	"github.com/sky-valley/pi/ai"
)

// This is a single new provider request, not an SDK loop retry. A first
// terminal 401 is hidden only when it is the sole pre-output event and carries
// the provider's in-process cause. All other streams are forwarded unchanged.
func (m *Manager) streamEmbeddedCodexWith401Rotation(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, opts *ai.SimpleStreamOptions, used *string) *ai.AssistantMessageEventStream {
	out := ai.NewAssistantMessageEventStream()
	go func() {
		source := ai.StreamSimple(ctx, model, ai.Context{Messages: transcript.Messages}, opts)
		first, ok := source.Next()
		retry := ok && first.Type == ai.EventError && first.FailureKind == ai.FailureCodexPreOutputUnauthorized && ctx.Err() == nil
		if retry {
			if err := m.rotateEmbeddedCodexUnauthorized(ctx, *used); err != nil {
				source = ai.ErrorStream(model, err)
			} else {
				source = ai.StreamSimple(ctx, model, ai.Context{Messages: transcript.Messages}, opts)
			}
		} else if ok {
			out.Push(first)
		}
		if !ok {
			out.End()
			return
		}
		for event := range source.Events() {
			out.Push(event)
		}
		out.End()
	}()
	return out
}
