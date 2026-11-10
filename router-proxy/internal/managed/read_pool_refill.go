package managed

import "time"

// refillReadPool operates on caller-owned state; the caller must hold activeMu.
// capElapsed preserves the background-bash pool's existing elapsed-time clamp.
func refillReadPool(remaining *int64, updatedAt *time.Time, now time.Time, capElapsed bool) {
	if updatedAt.IsZero() {
		*remaining = SubagentReadPoolMaxMs
		*updatedAt = now
		return
	}
	elapsed := now.Sub(*updatedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if capElapsed && elapsed > SubagentReadPoolMaxMs {
		elapsed = SubagentReadPoolMaxMs
	}
	*remaining += elapsed
	if *remaining > SubagentReadPoolMaxMs {
		*remaining = SubagentReadPoolMaxMs
	}
	*updatedAt = now
}
