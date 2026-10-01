//go:build !embedded_private_codex_observer

package managed

import "net/http"

// The ordinary binary has no observer output, environment input, or body reads.
func PreparePrivateCodexObserver() error            { return nil }
func WritePrivateStartupStage(_, _, _ string) error { return nil }
func privateCodexObserverEnter() (int, error)       { return 0, nil }
func privateCodexObserverResult(_ int, resp *http.Response, _ error) (*http.Response, error) {
	return resp, nil
}
