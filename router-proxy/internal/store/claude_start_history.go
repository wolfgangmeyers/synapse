package store

// Only these fixed strings may enter user-visible history; category is untrusted.
func claudeStartHistoryText(category string) string {
	switch category {
	case "auth":
		return "Claude startup failed (auth). Run claude auth status and sign in on the router host, then retry the message."
	case "spawn":
		return "Claude startup failed (spawn). Check or install the Claude CLI on the router host, then retry the message."
	case "probe-died":
		return "Claude startup failed (probe-died). Inspect the router logs and Claude CLI health, then retry the message."
	case "pool-fenced":
		return "Claude startup failed (pool-fenced). Inspect the router logs and resolve the process fence before retrying the message."
	case "grant-failed":
		return "Claude startup failed (grant-failed). Inspect the router logs and resolve the process grant failure, then retry the message."
	default:
		return "Claude startup failed (unknown). Inspect the router logs and Claude CLI on the router host, then retry the message."
	}
}
