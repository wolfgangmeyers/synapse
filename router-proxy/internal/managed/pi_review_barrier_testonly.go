//go:build embedded_restart_test_only

package managed

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func piReviewBarrier(agent string, revision int64, verdict string) {
	dir := os.Getenv("MECHA_PI_REVIEW_BARRIER")
	if dir == "" || revision != 1 {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "parsed"), []byte(agent+"\n"+verdict), 0600); err != nil {
		panic(fmt.Sprintf("pi review barrier: write parsed: %v", err))
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	panic("pi review barrier: timed out waiting for release")
}

// Runs after the evaluator task has taken its cancellation or continuation path.
func piReviewDecisionComplete(agent string, revision int64) {
	dir := os.Getenv("MECHA_PI_REVIEW_BARRIER")
	if dir == "" || revision != 1 {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "decision-complete"), []byte(agent), 0600); err != nil {
		panic(fmt.Sprintf("pi review barrier: write decision-complete: %v", err))
	}
}
