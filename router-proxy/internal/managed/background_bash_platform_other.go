//go:build !linux

package managed

import (
	"fmt"
	"runtime"
)

func backgroundBashPlatformSupport() error {
	return fmt.Errorf("background bash is unsupported on %s: safe process cleanup is not implemented", runtime.GOOS)
}
