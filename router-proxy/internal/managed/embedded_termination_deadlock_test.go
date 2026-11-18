package managed

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingRPCBinding struct {
	closed chan struct{}
}

func (b *blockingRPCBinding) Close() error {
	return nil
}

func (b *blockingRPCBinding) WaitForClose() {
	// Block forever until test completes
	<-b.closed
}

func (b *blockingRPCBinding) GetState(context.Context) (map[string]any, error) {
	return nil, nil
}

func (b *blockingRPCBinding) GetCommands(context.Context) ([]string, error) {
	return nil, nil
}

func (b *blockingRPCBinding) Bootstrap(context.Context, map[string]any) error {
	return nil
}

func (b *blockingRPCBinding) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}

func (b *blockingRPCBinding) GetActiveTools(context.Context) ([]string, error) {
	return nil, nil
}

func (b *blockingRPCBinding) Prompt(context.Context, string) error {
	return nil
}

func (b *blockingRPCBinding) Abort(context.Context) error {
	return nil
}

func (b *blockingRPCBinding) Request(ctx context.Context, method string, params any, result any) error {
	return nil
}

func (b *blockingRPCBinding) Notify(ctx context.Context, method string, params any) error {
	return nil
}

func (b *blockingRPCBinding) ApplyRuntimeOptions(ctx context.Context, opts any) error {
	return nil
}

// TestCloseRPCAndWaitTimeout verifies that closeRPCAndWait returns within bounded time
// even if rpc.WaitForClose blocks indefinitely.
func TestCloseRPCAndWaitTimeout(t *testing.T) {
	binding := &blockingRPCBinding{
		closed: make(chan struct{}),
	}
	defer close(binding.closed)

	start := time.Now()
	_ = closeRPCAndWait(binding)
	elapsed := time.Since(start)

	// closeRPCAndWait should return after ~5 seconds timeout, well under 10 seconds.
	require.GreaterOrEqual(t, elapsed, 4500*time.Millisecond)
	require.Less(t, elapsed, 7000*time.Millisecond)
}
