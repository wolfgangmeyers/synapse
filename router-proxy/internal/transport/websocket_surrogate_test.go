package transport

import "testing"

func TestContainsUnpairedSurrogateOnlyInspectsJSONStringEscapes(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		invalid bool
	}{
		{name: "literal escape in string", payload: `{"value":"\\uD800"}`},
		{name: "literal escape outside string", payload: `\\uD800`},
		{name: "paired escapes", payload: `{"value":"\uD800\uDC00"}`},
		{name: "lone high escape", payload: `{"value":"\uD800"}`, invalid: true},
		{name: "lone low escape", payload: `{"value":"\uDC00"}`, invalid: true},
		{name: "high escape followed by non-low", payload: `{"value":"\uD800x"}`, invalid: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := containsUnpairedSurrogate([]byte(testCase.payload)); got != testCase.invalid {
				t.Fatalf("containsUnpairedSurrogate(%q) = %v, want %v", testCase.payload, got, testCase.invalid)
			}
		})
	}
}
