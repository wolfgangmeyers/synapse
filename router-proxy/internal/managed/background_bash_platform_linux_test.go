//go:build linux

package managed

import "testing"

func TestBackgroundBashLinuxPlatformSupported(t *testing.T) {
	if err := backgroundBashPlatformSupport(); err != nil {
		t.Fatal(err)
	}
}
