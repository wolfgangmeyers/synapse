package managed

import (
	"github.com/sky-valley/pi/coding"
	"testing"
)

func TestIssue45ReadNullableSchema(t *testing.T) {
	tool, err := coding.CreateTool("read", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configureEmbeddedReadSchema(&tool)
	for _, args := range []map[string]any{{"path": "fixture"}, {"path": "fixture", "offset": nil, "limit": nil}, {"path": "fixture", "offset": float64(1), "limit": float64(2)}} {
		if !tool.Parameters.Check(args) {
			t.Errorf("read schema rejected valid pagination: %#v", args)
		}
	}
	for _, field := range []string{"offset", "limit"} {
		if tool.Parameters.Properties[field].Type != "integer" || !tool.Parameters.Properties[field].Nullable {
			t.Fatalf("%s schema must be integer or null: %#v", field, tool.Parameters.Properties[field])
		}
		if tool.Parameters.Check(map[string]any{"path": "fixture", field: 1.5}) {
			t.Errorf("accepted fractional %s", field)
		}
	}
	if tool.Parameters.Check(map[string]any{"path": "fixture", "offset": "bad"}) {
		t.Fatal("accepted invalid offset")
	}
}
