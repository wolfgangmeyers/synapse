package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type contractManifest struct {
	Version string `json:"version"`
	Status  string `json:"status"`
	CORS    struct {
		Status           string `json:"status"`
		GoImplementation string `json:"go_implementation"`
	} `json:"cors"`
	HTTP      []json.RawMessage `json:"http"`
	WebSocket struct {
		Path         string   `json:"path"`
		ClientFrames []string `json:"client_frames"`
		ServerFrames []string `json:"server_frames"`
		Schema       string   `json:"schema"`
	} `json:"websocket"`
	Fixtures map[string]string `json:"fixtures"`
}

type frameCasesFixture struct {
	Version string           `json:"version"`
	Status  string           `json:"status"`
	Cases   []frameCaseEntry `json:"cases"`
}

type frameCaseEntry struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	CloseCode *int   `json:"close_code"`
}

type httpOutcomesFixture struct {
	Version       string            `json:"version"`
	Status        string            `json:"status"`
	Cases         []httpOutcomeCase `json:"cases"`
	RouteOutcomes []json.RawMessage `json:"route_outcomes"`
}

type httpOutcomeCase struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type generatedFieldsFixture struct {
	Version           string      `json:"version"`
	NormalizationRule string      `json:"normalization_rule"`
	Normalizable      []fieldRule `json:"normalizable"`
	Protected         []fieldRule `json:"protected"`
}

type fieldRule struct {
	Path     string   `json:"path"`
	Contexts []string `json:"contexts"`
}

func TestContractManifestScaffoldIsSelfContained(t *testing.T) {
	contractDir := filepath.Join("..", "router", "contract")
	manifest := readManifest(t, contractDir)

	require.Equal(t, "v1", manifest.Version)
	require.Equal(t, "cors_verified", manifest.Status)
	require.Equal(t, "verified", manifest.CORS.Status)
	require.Equal(t, "implemented", manifest.CORS.GoImplementation)
	require.NotEmpty(t, manifest.HTTP)
	require.Equal(t, "/v1/agents", manifest.WebSocket.Path)
	require.NotEmpty(t, manifest.WebSocket.ClientFrames)
	require.NotEmpty(t, manifest.WebSocket.ServerFrames)
	require.Equal(t, "protocol/v1/schema.json", manifest.WebSocket.Schema)
	require.Equal(t, map[string]string{
		"http_outcomes":          "http-outcomes.json",
		"frame_cases":            "frame-cases.json",
		"generated_fields":       "generated-fields.json",
		"transport_differential": "transport-differential.json",
	}, manifest.Fixtures)

	fixtures, err := loadManifestFixtures(contractDir, manifest)
	require.NoError(t, err)
	require.Len(t, fixtures, len(manifest.Fixtures))

	protocol := readContractJSON(t, filepath.Join("..", "protocol", "v1", "schema.json"))
	clientTypes := schemaFrameTypes(t, protocol, "clientFrame")
	serverTypes := schemaFrameTypes(t, protocol, "serverFrame")
	for _, frameType := range manifest.WebSocket.ClientFrames {
		require.Contains(t, clientTypes, frameType, "client frame is absent from protocol clientFrame union")
	}
	for _, frameType := range manifest.WebSocket.ServerFrames {
		require.Contains(t, serverTypes, frameType, "server frame is absent from protocol serverFrame union")
	}
	manifestClientTypes := frameTypeSet(manifest.WebSocket.ClientFrames)
	manifestServerTypes := frameTypeSet(manifest.WebSocket.ServerFrames)
	require.Equal(t, clientTypes, manifestClientTypes, "manifest client inventory must match protocol clientFrame union")
	require.Equal(t, serverTypes, manifestServerTypes, "manifest server inventory must match protocol serverFrame union")
}

func TestContractManifestManagedLifecycleRoutes(t *testing.T) {
	manifest := readManifest(t, filepath.Join("..", "router", "contract"))
	found := map[string]bool{}
	for _, raw := range manifest.HTTP {
		var route struct {
			Methods []string `json:"methods"`
			Path    string   `json:"path"`
			Managed bool     `json:"managed"`
		}
		require.NoError(t, json.Unmarshal(raw, &route))
		if route.Path == "/v1/managed-agents/restart-all" || route.Path == "/v1/managed-agents/{agent_id}/restart" || route.Path == "/v1/managed-agents/{id}/reset" {
			found[route.Path] = true
			require.Equal(t, []string{"POST"}, route.Methods)
			require.True(t, route.Managed)
		}
	}
	require.True(t, found["/v1/managed-agents/restart-all"])
	require.True(t, found["/v1/managed-agents/{agent_id}/restart"])
	require.True(t, found["/v1/managed-agents/{id}/reset"])
}

func TestManifestFixtureReferenceFailureIsReported(t *testing.T) {
	contractDir := filepath.Join("..", "router", "contract")
	manifest := readManifest(t, contractDir)
	brokenManifest := manifest
	brokenManifest.Fixtures = make(map[string]string, len(manifest.Fixtures))
	for name, reference := range manifest.Fixtures {
		brokenManifest.Fixtures[name] = reference
	}
	brokenManifest.Fixtures["frame_cases"] = "missing-frame-cases.json"

	_, err := loadManifestFixtures(contractDir, brokenManifest)
	require.Error(t, err)
	require.ErrorContains(t, err, "frame_cases")
	require.ErrorContains(t, err, "missing-frame-cases.json")
}

func TestContractFixturesValidateFrameAndGeneratedFieldRules(t *testing.T) {
	contractDir := filepath.Join("..", "router", "contract")
	manifest := readManifest(t, contractDir)
	fixtures, err := loadManifestFixtures(contractDir, manifest)
	require.NoError(t, err)

	var outcomes httpOutcomesFixture
	decodeFixture(t, fixtures, "http_outcomes", &outcomes)
	require.Equal(t, "v1", outcomes.Version)
	require.Equal(t, "cors_verified", outcomes.Status)
	require.NotEmpty(t, outcomes.Cases)
	require.NotEmpty(t, outcomes.RouteOutcomes)
	foundRestartAllOutcome := false
	for _, raw := range outcomes.RouteOutcomes {
		var outcome struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.Unmarshal(raw, &outcome))
		if outcome.Path == "/v1/managed-agents/restart-all" {
			foundRestartAllOutcome = true
		}
	}
	require.True(t, foundRestartAllOutcome)
	for _, outcomeCase := range outcomes.Cases {
		require.NotEmpty(t, outcomeCase.ID)
		require.Equal(t, "verified", outcomeCase.Status)
	}

	var frames frameCasesFixture
	decodeFixture(t, fixtures, "frame_cases", &frames)
	require.Equal(t, "v1", frames.Version)
	require.Equal(t, "scaffold", frames.Status)
	require.NotEmpty(t, frames.Cases)
	seenCases := make(map[string]struct{}, len(frames.Cases))
	for _, fixtureCase := range frames.Cases {
		require.NotEmpty(t, fixtureCase.ID)
		_, duplicate := seenCases[fixtureCase.ID]
		require.False(t, duplicate, "duplicate frame case %q", fixtureCase.ID)
		seenCases[fixtureCase.ID] = struct{}{}
		require.Equal(t, "pending", fixtureCase.Status)
	}
	for id, wantCode := range map[string]int{
		"frame-too-large":    1009,
		"lease-expired":      4001,
		"replacement":        4001,
		"duplicate-identity": 4009,
		"internal-error":     1011,
	} {
		fixtureCase := findFrameCase(t, frames.Cases, id)
		require.NotNil(t, fixtureCase.CloseCode)
		require.Equal(t, wantCode, *fixtureCase.CloseCode)
	}
	leaseNotActive := findFrameCase(t, frames.Cases, "lease-not-active")
	require.NotNil(t, leaseNotActive)
	require.NotNil(t, leaseNotActive.CloseCode)
	require.Equal(t, 4003, *leaseNotActive.CloseCode)

	var generated generatedFieldsFixture
	decodeFixture(t, fixtures, "generated_fields", &generated)
	require.Equal(t, "v1", generated.Version)
	require.Equal(t, "Normalize only when both path and context match a normalizable rule. Protected rules always win.", generated.NormalizationRule)
	require.NotEmpty(t, generated.Normalizable)
	require.NotEmpty(t, generated.Protected)
	for _, rule := range append(append([]fieldRule{}, generated.Normalizable...), generated.Protected...) {
		require.True(t, strings.HasPrefix(rule.Path, "$."), "field rule path must be JSON-path-like: %q", rule.Path)
		require.NotEmpty(t, rule.Contexts, "field rule %q has no context", rule.Path)
	}
	requireUniqueRuleContexts(t, generated.Normalizable)
	requireUniqueRuleContexts(t, generated.Protected)

	normalizable := rulesByPath(generated.Normalizable)
	protected := rulesByPath(generated.Protected)
	for _, path := range []string{"$.created_at", "$.frame_id", "$.request_id", "$.control_id"} {
		for _, context := range protected[path] {
			require.NotContains(t, normalizable[path], context, "protected context may not be normalized")
		}
	}
	require.NotContains(t, normalizable, "$.control_id")
	require.Contains(t, protected["$.created_at"], "request")
	require.Contains(t, protected["$.created_at"], "echoed_response")
	require.Contains(t, protected["$.frame_id"], "client_frame")
	require.Contains(t, protected["$.frame_id"], "echoed_response")
	require.Contains(t, protected["$.request_id"], "request")
	require.Contains(t, protected["$.request_id"], "echoed_response")
	require.Contains(t, protected["$.control_id"], "client_frame")
	require.Contains(t, protected["$.control_id"], "forwarded_frame")
	require.Contains(t, protected["$.control_id"], "echoed_response")
}

func readManifest(t *testing.T, contractDir string) contractManifest {
	t.Helper()
	var manifest contractManifest
	readContractFile(t, filepath.Join(contractDir, "manifest.json"), &manifest)
	return manifest
}

func decodeFixture(t *testing.T, fixtures map[string]json.RawMessage, name string, destination interface{}) {
	t.Helper()
	document, ok := fixtures[name]
	require.True(t, ok, "manifest fixture %q was not loaded", name)
	require.NoError(t, json.Unmarshal(document, destination))
}

func loadManifestFixtures(contractDir string, manifest contractManifest) (map[string]json.RawMessage, error) {
	if len(manifest.Fixtures) == 0 {
		return nil, fmt.Errorf("manifest declares no fixtures")
	}
	loaded := make(map[string]json.RawMessage, len(manifest.Fixtures))
	for name, reference := range manifest.Fixtures {
		path, err := resolveManifestFixturePath(contractDir, reference)
		if err != nil {
			return nil, fmt.Errorf("resolve %s fixture %q: %w", name, reference, err)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s fixture %q: %w", name, reference, err)
		}
		var document json.RawMessage
		if err := json.Unmarshal(contents, &document); err != nil {
			return nil, fmt.Errorf("parse %s fixture %q: %w", name, reference, err)
		}
		loaded[name] = document
	}
	return loaded, nil
}

func resolveManifestFixturePath(contractDir, reference string) (string, error) {
	if reference == "" || filepath.IsAbs(reference) {
		return "", fmt.Errorf("fixture reference must be a non-empty relative path")
	}
	path := filepath.Join(contractDir, reference)
	relative, err := filepath.Rel(contractDir, path)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fixture reference escapes contract directory")
	}
	return path, nil
}

func frameTypeSet(frameTypes []string) map[string]struct{} {
	result := make(map[string]struct{}, len(frameTypes))
	for _, frameType := range frameTypes {
		result[frameType] = struct{}{}
	}
	return result
}

func findFrameCase(t *testing.T, cases []frameCaseEntry, id string) *frameCaseEntry {
	t.Helper()
	for index := range cases {
		if cases[index].ID == id {
			return &cases[index]
		}
	}
	t.Fatalf("frame case %q is missing", id)
	return nil
}

func requireUniqueRuleContexts(t *testing.T, rules []fieldRule) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, rule := range rules {
		for _, context := range rule.Contexts {
			key := rule.Path + "\x00" + context
			_, duplicate := seen[key]
			require.False(t, duplicate, "duplicate field rule %q in context %q", rule.Path, context)
			seen[key] = struct{}{}
		}
	}
}

func rulesByPath(rules []fieldRule) map[string][]string {
	result := make(map[string][]string, len(rules))
	for _, rule := range rules {
		result[rule.Path] = append(result[rule.Path], rule.Contexts...)
	}
	return result
}

func schemaFrameTypes(t *testing.T, schema map[string]interface{}, unionName string) map[string]struct{} {
	t.Helper()
	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok)
	union, ok := defs[unionName].(map[string]interface{})
	require.True(t, ok)
	oneOf, ok := union["oneOf"].([]interface{})
	require.True(t, ok)

	result := make(map[string]struct{}, len(oneOf))
	for _, rawReference := range oneOf {
		reference, ok := rawReference.(map[string]interface{})
		require.True(t, ok)
		ref, ok := reference["$ref"].(string)
		require.True(t, ok)
		definitionName := strings.TrimPrefix(ref, "#/$defs/")
		definition, ok := defs[definitionName].(map[string]interface{})
		require.True(t, ok)
		properties, ok := definition["properties"].(map[string]interface{})
		require.True(t, ok)
		typeProperty, ok := properties["type"].(map[string]interface{})
		require.True(t, ok)
		frameType, ok := typeProperty["const"].(string)
		require.True(t, ok)
		result[frameType] = struct{}{}
	}
	return result
}

func readContractFile(t *testing.T, path string, destination interface{}) {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(contents, destination))
}

func readContractJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	var document map[string]interface{}
	readContractFile(t, path, &document)
	return document
}
