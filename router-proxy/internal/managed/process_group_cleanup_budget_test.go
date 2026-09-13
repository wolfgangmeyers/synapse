//go:build !race

package managed

import "time"

const behavioralCleanupTermStop = 500 * time.Millisecond
