//go:build race

package managed

import "time"

// Race instrumentation can delay kernel process reaping. Keep this budget
// finite while allowing the real process-group path to finish.
const behavioralCleanupTermStop = 2 * time.Second
