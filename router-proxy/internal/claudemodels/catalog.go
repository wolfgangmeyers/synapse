// Package claudemodels defines selectable Claude CLI aliases, not authentication state.
package claudemodels

const Provider = "claude-code"

type Alias struct{ ID, Name string }

func Catalog() []Alias {
	return []Alias{{"fable", "Claude Fable"}, {"opus", "Claude Opus"}, {"sonnet", "Claude Sonnet"}, {"haiku", "Claude Haiku"}}
}

func Valid(id string) bool {
	for _, alias := range Catalog() {
		if alias.ID == id {
			return true
		}
	}
	return false
}
