//go:build claude_mcp_queue_test_only

package managed

import "time"

func claudeMCPQueueDuration() time.Duration { return 2 * time.Second }
