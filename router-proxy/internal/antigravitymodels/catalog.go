// Package antigravitymodels defines selectable Google Antigravity CLI models, not authentication state.
package antigravitymodels

const Provider = "antigravity"

type ModelDef struct {
	ID            string
	Name          string
	ContextWindow int
}

func Catalog() []ModelDef {
	return []ModelDef{
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro (Antigravity)", ContextWindow: 1_000_000},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash (Antigravity)", ContextWindow: 1_000_000},
		{ID: "gemini-3.8-pro", Name: "Gemini 3.8 Pro (Antigravity)", ContextWindow: 1_000_000},
		{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash (Antigravity)", ContextWindow: 1_000_000},
	}
}

func Valid(id string) bool {
	for _, m := range Catalog() {
		if m.ID == id {
			return true
		}
	}
	return false
}
