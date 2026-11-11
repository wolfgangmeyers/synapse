package managed

import (
	"testing"
	"time"
)

func TestRootBashPromotionDeadline(t *testing.T) {
	for _, tc := range []struct {
		role string
		args map[string]any
		want time.Duration
	}{
		{"root", map[string]any{"command": "true"}, 15 * time.Second},
		{"worker", map[string]any{"command": "true"}, 120 * time.Second},
		{"root", map[string]any{"command": "true", "timeout": 120}, 120 * time.Second},
		{"root", map[string]any{"command": "true", "timeout": 1}, time.Second},
	} {
		spec, err := parseEmbeddedBash(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if got := rootBashPromotionDeadline(spec, tc.args, tc.role).timeout; got != tc.want {
			t.Errorf("role %s args %v: got %v want %v", tc.role, tc.args, got, tc.want)
		}
	}
}
