package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type workflowPhase0HTTPFixture struct {
	Version             string `json:"version"`
	Phase               string `json:"phase"`
	Status              string `json:"status"`
	Authority           string `json:"authority"`
	ResponseConventions struct {
		CacheControl          string `json:"cacheControl"`
		CacheControlAppliesTo string `json:"cacheControlAppliesTo"`
		JSONContentType       string `json:"jsonContentType"`
		JSONMembers           string `json:"jsonMembers"`
		ErrorCodes            string `json:"errorCodes"`
		CorrelationHeader     string `json:"correlationHeader"`
	} `json:"responseConventions"`
	Routing struct {
		AliasesAccepted                 bool              `json:"aliasesAccepted"`
		OptionalTrailingSlashAccepted   bool              `json:"optionalTrailingSlashAccepted"`
		UnlistedQueryParametersAccepted bool              `json:"unlistedQueryParametersAccepted"`
		CollectionQueryParameters       []string          `json:"collectionQueryParameters"`
		PathIdentifiers                 map[string]string `json:"pathIdentifiers"`
	} `json:"routing"`
	RequestID struct {
		Header                            string   `json:"header"`
		Format                            string   `json:"format"`
		RequiredForMutations              bool     `json:"requiredForMutations"`
		MutationMethods                   []string `json:"mutationMethods"`
		Scope                             string   `json:"scope"`
		SuccessBodyPlacement              string   `json:"successBodyPlacement"`
		ValidErrorBodyPlacements          []string `json:"validErrorBodyPlacements"`
		InvalidOrAbsentErrorBodyPlacement string   `json:"invalidOrAbsentErrorBodyPlacement"`
		ChangedPathOrCanonicalBody        struct {
			Status int    `json:"status"`
			Code   string `json:"code"`
		} `json:"changedPathOrCanonicalBody"`
	} `json:"requestId"`
	RequestBodies struct {
		MaximumBytes           int    `json:"maximumBytes"`
		ContentType            string `json:"contentType"`
		ClosedObjects          bool   `json:"closedObjects"`
		DuplicateKeysAccepted  bool   `json:"duplicateKeysAccepted"`
		TrailingValuesAccepted bool   `json:"trailingValuesAccepted"`
		LoneSurrogatesAccepted bool   `json:"loneSurrogatesAccepted"`
		DeleteBody             string `json:"deleteBody"`
	} `json:"requestBodies"`
	Routes                []workflowPhase0Route `json:"routes"`
	ClosedResponseMembers map[string][]string   `json:"closedResponseMembers"`
	ErrorEnvelope         struct {
		TopLevelMembersWithValidRequestID    []string               `json:"topLevelMembersWithValidRequestId"`
		TopLevelMembersWithoutValidRequestID []string               `json:"topLevelMembersWithoutValidRequestId"`
		ErrorMembers                         []string               `json:"errorMembers"`
		DetailsRequiredMembers               []string               `json:"detailsRequiredMembers"`
		DetailsState                         string                 `json:"detailsState"`
		ValidRequestIDAlsoInDetails          bool                   `json:"validRequestIdAlsoInDetails"`
		DetailsAreClosedPerOperation         bool                   `json:"detailsAreClosedPerOperation"`
		ExampleWithValidRequestID            map[string]interface{} `json:"exampleWithValidRequestId"`
	} `json:"errorEnvelope"`
	Errors []struct {
		Status int      `json:"status"`
		Codes  []string `json:"codes"`
	} `json:"errors"`
	Phase0JournalTypesInCompletedOrder []string `json:"phase0JournalTypesInCompletedOrder"`
}

type workflowPhase0Route struct {
	Method                string                 `json:"method"`
	Path                  string                 `json:"path"`
	SuccessStatus         int                    `json:"successStatus"`
	SuccessEnvelope       []string               `json:"successEnvelope"`
	RequestFields         []string               `json:"requestFields"`
	RequiredRequestFields []string               `json:"requiredRequestFields"`
	Defaults              map[string]string      `json:"defaults"`
	DeletedFields         []string               `json:"deletedFields"`
	Sort                  []string               `json:"sort"`
	Operation             string                 `json:"operation"`
	Phase0Inputs          map[string]interface{} `json:"phase0Inputs"`
}

func TestWorkflowPhase0HTTPContractFreezesRouteInventory(t *testing.T) {
	fixture := readWorkflowPhase0HTTPFixture(t)

	type routeExpectation struct {
		method   string
		path     string
		status   int
		envelope []string
	}
	want := []routeExpectation{
		{"GET", "/v1/workflow-definitions", 200, []string{"definitions"}},
		{"POST", "/v1/workflow-definitions", 201, []string{"definition", "revision"}},
		{"GET", "/v1/workflow-definitions/{definitionId}", 200, []string{"definition", "revision"}},
		{"PUT", "/v1/workflow-definitions/{definitionId}", 200, []string{"definition", "revision"}},
		{"DELETE", "/v1/workflow-definitions/{definitionId}", 200, []string{"deleted"}},
		{"GET", "/v1/workflow-definitions/{definitionId}/revisions", 200, []string{"revisions"}},
		{"GET", "/v1/workflow-definitions/{definitionId}/revisions/{revision}", 200, []string{"revision"}},
		{"GET", "/v1/workflow-eligible-agents", 200, []string{"agents"}},
		{"POST", "/v1/workflow-runs", 201, []string{"run"}},
		{"GET", "/v1/workflow-runs/{runId}", 200, []string{"run"}},
		{"GET", "/v1/workflow-runs/{runId}/journal", 200, []string{"runId", "events"}},
	}
	require.Len(t, fixture.Routes, len(want))
	seen := make(map[string]struct{}, len(want))
	for index, expected := range want {
		route := fixture.Routes[index]
		require.Equal(t, expected.method, route.Method, "route %d method", index)
		require.Equal(t, expected.path, route.Path, "route %d path", index)
		require.Equal(t, expected.status, route.SuccessStatus, "route %s success status", route.Path)
		require.Equal(t, expected.envelope, route.SuccessEnvelope, "route %s success envelope", route.Path)
		require.NotEmpty(t, route.Operation)
		key := route.Method + " " + route.Path
		_, duplicate := seen[key]
		require.False(t, duplicate, "duplicate route %s", key)
		seen[key] = struct{}{}
		require.False(t, strings.HasSuffix(route.Path, "/"), "canonical route has trailing slash")
		require.NotContains(t, route.Path, "?")
	}

	require.False(t, fixture.Routing.AliasesAccepted)
	require.False(t, fixture.Routing.OptionalTrailingSlashAccepted)
	require.False(t, fixture.Routing.UnlistedQueryParametersAccepted)
	require.Empty(t, fixture.Routing.CollectionQueryParameters)
	require.Equal(t, map[string]string{
		"definitionId": "canonical lowercase UUID",
		"revision":     "canonical positive base-10 integer without leading zeroes",
		"runId":        "canonical lowercase UUID",
	}, fixture.Routing.PathIdentifiers)
	require.Equal(t, []string{"name", "definitionId"}, fixture.Routes[0].Sort)
	require.Equal(t, []string{"revision desc"}, fixture.Routes[5].Sort)
	require.Equal(t, []string{"agentId"}, fixture.Routes[7].Sort)
	require.Equal(t, []string{"sequence"}, fixture.Routes[10].Sort)
	for index, route := range fixture.Routes {
		if index != 0 && index != 5 && index != 7 && index != 10 {
			require.Empty(t, route.Sort, "route %s has an uncontracted sort", route.Path)
		}
	}
}

func TestWorkflowPhase0HTTPContractFreezesRequestsAndResponseCasing(t *testing.T) {
	fixture := readWorkflowPhase0HTTPFixture(t)

	require.Equal(t, "no-store", fixture.ResponseConventions.CacheControl)
	require.Equal(t, "every response including errors", fixture.ResponseConventions.CacheControlAppliesTo)
	require.Equal(t, "application/json; charset=utf-8", fixture.ResponseConventions.JSONContentType)
	require.Equal(t, "lowerCamelCase", fixture.ResponseConventions.JSONMembers)
	require.Equal(t, "lowercase_snake_case", fixture.ResponseConventions.ErrorCodes)
	require.Equal(t, "X-Correlation-ID", fixture.ResponseConventions.CorrelationHeader)

	require.Equal(t, 64*1024, fixture.RequestBodies.MaximumBytes)
	require.Equal(t, "application/json", fixture.RequestBodies.ContentType)
	require.True(t, fixture.RequestBodies.ClosedObjects)
	require.False(t, fixture.RequestBodies.DuplicateKeysAccepted)
	require.False(t, fixture.RequestBodies.TrailingValuesAccepted)
	require.False(t, fixture.RequestBodies.LoneSurrogatesAccepted)
	require.Equal(t, "forbidden", fixture.RequestBodies.DeleteBody)

	wantRequestFields := map[string][]string{
		"POST /v1/workflow-definitions":               {"name", "description", "status", "snapshot"},
		"PUT /v1/workflow-definitions/{definitionId}": {"expectedVersion", "expectedRevision", "name", "description", "status", "snapshot"},
		"POST /v1/workflow-runs":                      {"definitionId", "expectedRevision", "agentId", "inputs"},
	}
	wantRequiredFields := map[string][]string{
		"POST /v1/workflow-definitions":               {"name", "snapshot"},
		"PUT /v1/workflow-definitions/{definitionId}": {"expectedVersion", "expectedRevision", "name", "description", "status", "snapshot"},
		"POST /v1/workflow-runs":                      {"definitionId", "expectedRevision", "agentId", "inputs"},
	}
	for _, route := range fixture.Routes {
		key := route.Method + " " + route.Path
		if expected, ok := wantRequestFields[key]; ok {
			require.Equal(t, expected, route.RequestFields, key)
			require.Equal(t, wantRequiredFields[key], route.RequiredRequestFields, key)
		} else {
			require.Empty(t, route.RequestFields, key)
			require.Empty(t, route.RequiredRequestFields, key)
		}
	}
	require.NotNil(t, fixture.Routes[8].Phase0Inputs, "launch inputs contract must be present")
	require.Empty(t, fixture.Routes[8].Phase0Inputs, "launch inputs must be the exact empty object in Phase 0")
	require.Equal(t, map[string]string{"description": "", "status": "disabled"}, fixture.Routes[1].Defaults)
	require.Equal(t, []string{"definitionId"}, fixture.Routes[4].DeletedFields)

	wantResponseMembers := map[string][]string{
		"definition":       {"definitionId", "name", "description", "status", "version", "currentRevision", "validation", "createdAt", "updatedAt"},
		"revision":         {"definitionId", "revision", "snapshot", "validation", "createdAt"},
		"revisionSummary":  {"definitionId", "revision", "validation", "createdAt"},
		"validation":       {"valid", "issues"},
		"validationIssue":  {"path", "code", "message"},
		"snapshot":         {"format", "inputs", "entryStateId", "states"},
		"state":            {"stateId", "name", "entryText", "compactOnExit", "exits"},
		"completeExit":     {"targetKind", "description"},
		"stateExit":        {"targetKind", "targetStateId", "description"},
		"eligibleAgent":    {"agentId", "name", "ready", "acceptingPrompts", "hasActiveWorkflow", "manifest"},
		"manifestIdentity": {"revision", "sha256"},
		"run":              {"runId", "definitionId", "definitionName", "revision", "agentId", "status", "phase", "version", "currentState", "currentEntryId", "availableExits", "notification", "startedAt", "completedAt", "finalExit"},
		"currentState":     {"stateId", "name"},
		"notification":     {"state", "wakeState"},
		"journalEvent":     {"sequence", "type", "at", "stateId", "entryId", "exit", "note"},
		"deleted":          {"definitionId"},
	}
	require.Equal(t, wantResponseMembers, fixture.ClosedResponseMembers)
	lowerCamel := regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	for shape, members := range fixture.ClosedResponseMembers {
		require.True(t, lowerCamel.MatchString(shape), "response shape name %q is not lowerCamelCase", shape)
		for _, member := range members {
			require.True(t, lowerCamel.MatchString(member), "response member %q in %s is not lowerCamelCase", member, shape)
		}
	}
}

func TestWorkflowPhase0HTTPContractFreezesRequestIDAndErrorEnvelope(t *testing.T) {
	fixture := readWorkflowPhase0HTTPFixture(t)

	require.Equal(t, "X-Request-ID", fixture.RequestID.Header)
	require.Equal(t, "canonical lowercase UUID", fixture.RequestID.Format)
	require.True(t, fixture.RequestID.RequiredForMutations)
	require.Equal(t, []string{"POST", "PUT", "DELETE"}, fixture.RequestID.MutationMethods)
	require.Equal(t, "HTTP method plus X-Request-ID", fixture.RequestID.Scope)
	require.Equal(t, "absent", fixture.RequestID.SuccessBodyPlacement)
	require.Equal(t, []string{"requestId", "error.details.requestId"}, fixture.RequestID.ValidErrorBodyPlacements)
	require.Equal(t, "absent", fixture.RequestID.InvalidOrAbsentErrorBodyPlacement)
	require.Equal(t, 409, fixture.RequestID.ChangedPathOrCanonicalBody.Status)
	require.Equal(t, "idempotency_conflict", fixture.RequestID.ChangedPathOrCanonicalBody.Code)

	require.Equal(t, []string{"error", "requestId"}, fixture.ErrorEnvelope.TopLevelMembersWithValidRequestID)
	require.Equal(t, []string{"error"}, fixture.ErrorEnvelope.TopLevelMembersWithoutValidRequestID)
	require.Equal(t, []string{"code", "message", "details"}, fixture.ErrorEnvelope.ErrorMembers)
	require.Equal(t, []string{"operation", "state"}, fixture.ErrorEnvelope.DetailsRequiredMembers)
	require.Equal(t, "rejected", fixture.ErrorEnvelope.DetailsState)
	require.True(t, fixture.ErrorEnvelope.ValidRequestIDAlsoInDetails)
	require.True(t, fixture.ErrorEnvelope.DetailsAreClosedPerOperation)

	example := fixture.ErrorEnvelope.ExampleWithValidRequestID
	require.Equal(t, "b7ba25de-50ee-4b66-b780-4b31dc5e3725", example["requestId"])
	errorObject, ok := example["error"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "workflow_not_found", errorObject["code"])
	require.Equal(t, "Workflow definition was not found.", errorObject["message"])
	details, ok := errorObject["details"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "workflow_definition_read", details["operation"])
	require.Equal(t, "rejected", details["state"])
	require.Equal(t, example["requestId"], details["requestId"])
}

func TestWorkflowPhase0HTTPContractFreezesErrorStatusAndCodeInventory(t *testing.T) {
	fixture := readWorkflowPhase0HTTPFixture(t)
	want := map[int][]string{
		400: {"invalid_request_id", "invalid_json", "unknown_field", "invalid_identifier", "invalid_request"},
		404: {"workflow_not_found", "revision_not_found", "run_not_found"},
		409: {"idempotency_conflict", "definition_revision_conflict", "definition_has_active_run", "agent_has_active_run"},
		413: {"payload_too_large"},
		422: {"workflow_invalid", "definition_name_conflict", "invalid_launch_inputs", "agent_not_eligible"},
		500: {"internal_error"},
		503: {"managed_disabled", "agent_not_ready", "workflow_delivery_unavailable"},
	}
	require.Len(t, fixture.Errors, len(want))
	lowerSnake := regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	for _, group := range fixture.Errors {
		require.Equal(t, want[group.Status], group.Codes, "status %d", group.Status)
		for _, code := range group.Codes {
			require.True(t, lowerSnake.MatchString(code), "error code %q is not lowercase_snake_case", code)
		}
		delete(want, group.Status)
	}
	require.Empty(t, want)
	require.Equal(t, []string{"run_launched", "state_entered", "transition_requested", "transition_completed", "run_completed"}, fixture.Phase0JournalTypesInCompletedOrder)
}

func TestWorkflowPhase0HTTPContractPendingImplementationIsExplicit(t *testing.T) {
	fixture := readWorkflowPhase0HTTPFixture(t)
	require.Equal(t, "v1", fixture.Version)
	require.Equal(t, "managed-workflows-phase-0", fixture.Phase)
	require.Equal(t, "pending_implementation", fixture.Status)
	require.Equal(t, "MANAGED_WORKFLOWS_PHASE_0.md", fixture.Authority)
}

func readWorkflowPhase0HTTPFixture(t *testing.T) workflowPhase0HTTPFixture {
	t.Helper()
	manifest := readManifest(t, filepath.Join("..", "router", "contract"))
	reference, ok := manifest.Fixtures["workflow_phase0_http"]
	require.True(t, ok, "manifest must reference the Phase 0 workflow HTTP fixture")
	var fixture workflowPhase0HTTPFixture
	readContractFile(t, filepath.Join("..", "router", "contract", reference), &fixture)
	return fixture
}
