package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

func TestEmbeddedEffectObserverEvidenceAndFailure(t *testing.T) {
	binding := store.EmbeddedBinding{AgentID: "agent", SessionID: "session", Epoch: 1, Generation: "generation", OwnerToken: 2}
	sentinel := errors.New("barrier failed")
	m := &Manager{embeddedEffectObserver: func(ctx context.Context, event EmbeddedEffectObservation) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("observer wait must be bounded")
		}
		if event.Phase != "domain" || event.Binding != binding || event.CanonicalEffectID != "canonical" {
			t.Fatal("wrong identity")
		}
		event.Binding.OwnerToken = 99 // modifying a value cannot mint authority
		return sentinel
	}}
	err := m.observeEmbeddedEffect(context.Background(), "domain", binding, "canonical")
	var observerError *embeddedEffectObserverError
	if !errors.Is(err, sentinel) || !errors.As(err, &observerError) {
		t.Fatal("failure was not propagated")
	}
	if binding.OwnerToken != 2 {
		t.Fatal("observer changed caller binding")
	}
	// A failed domain observer is never converted into a persisted SDK error.
	// Nil persistence deliberately proves fail-stop happens before any append.
	authority := embeddedTurnToolAuthority{observerFailure: func() error { return err }}
	if got := authority.AppendCheckedMessage(context.Background(), ai.ToolResultMessage{}); !errors.Is(got, sentinel) {
		t.Fatal("append advanced after failed barrier")
	}
}
