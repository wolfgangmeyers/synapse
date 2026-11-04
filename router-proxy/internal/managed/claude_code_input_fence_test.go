package managed

import (
	"context"
	"errors"
	"io"
	"testing"
)

type shortClaudeInput struct{}

func (shortClaudeInput) Write(p []byte) (int, error) { return len(p) - 1, nil }
func (shortClaudeInput) Close() error                { return nil }

func TestClaudeInputShortWriteFailsAfterDurableStart(t *testing.T) {
	p := &ClaudeCodeProcess{stdin: shortClaudeInput{}, notify: make(chan struct{}, 1)}
	started := false
	err := p.writeWithStart(context.Background(), "input", func(context.Context) error {
		started = true
		return nil
	})
	if !started || !errors.Is(err, io.ErrShortWrite) || !errors.Is(p.Err(), io.ErrShortWrite) {
		t.Fatalf("started=%v write error=%v process error=%v", started, err, p.Err())
	}
}
