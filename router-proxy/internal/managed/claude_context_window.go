package managed

import (
	"encoding/json"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// The serial lane owns this bounded, binding-scoped observation.
func (lane *managedAgentLane) claudeWindow(binding store.EmbeddedBinding, model string) int {
	if lane.claudeWindowBinding != binding || lane.claudeWindowModel != model {
		lane.claudeWindowBinding, lane.claudeWindowModel, lane.claudeWindowValue = binding, model, 0
	}
	if lane.claudeWindowValue > 0 {
		return lane.claudeWindowValue
	}
	return 1000000
}
func (lane *managedAgentLane) setClaudeWindow(binding store.EmbeddedBinding, model string, window int) {
	lane.claudeWindow(binding, model)
	if window > 0 {
		lane.claudeWindowValue = window
	}
}

func claudeReportedWindow(alias, resolved string, usage map[string]json.RawMessage) int {
	result := 0
	for key, raw := range usage {
		if key != alias && (resolved == "" || key != resolved) {
			continue
		}
		var n int
		if json.Unmarshal(raw, &n) != nil || n <= 0 {
			continue
		}
		if result != 0 && result != n {
			return 0
		}
		result = n
	}
	return result
}
func claudeHeadroomFits(window, used, bytes int) bool {
	return window > embeddedContextHeadroom && used >= 0 && bytes >= 0 && used <= window-embeddedContextHeadroom && bytes <= window-embeddedContextHeadroom-used
}
