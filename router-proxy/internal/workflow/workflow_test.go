package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	stateA = "11111111-1111-4111-8111-111111111111"
	stateB = "22222222-2222-4222-8222-222222222222"
	stateC = "33333333-3333-4333-8333-333333333333"
)

func validSnapshot() Snapshot {
	return Snapshot{
		Format:       SnapshotFormat,
		Inputs:       []Input{},
		EntryStateID: stateA,
		States: []State{{
			StateID:   stateA,
			Name:      "hello",
			EntryText: "Say hello, then complete this workflow.",
			Exits:     []Exit{{TargetKind: ExitTargetComplete, Description: "Finish."}},
		}},
	}
}

func issueCodes(validation Validation) []string {
	codes := make([]string, len(validation.Issues))
	for i, issue := range validation.Issues {
		codes[i] = issue.Code
	}
	return codes
}

func issueAt(validation Validation, path, code string) bool {
	for _, issue := range validation.Issues {
		if issue.Path == path && issue.Code == code {
			return true
		}
	}
	return false
}

func target(id string) *string { return &id }

func TestValidPhase0DefinitionAndRunnablePredicate(t *testing.T) {
	definition := Definition{
		Name: "hello-world", Description: "One-state demonstration",
		Status: StatusEnabled, Snapshot: validSnapshot(),
	}
	validation := ValidateDefinition(definition)
	require.True(t, validation.Valid)
	require.Empty(t, validation.Issues)
	require.True(t, IsPhase0Runnable(definition.Snapshot))

	for _, status := range []DefinitionStatus{StatusEnabled, StatusDisabled, StatusArchived} {
		definition.Status = status
		require.True(t, ValidateDefinition(definition).Valid)
	}
}

func TestDefinitionMetadataBoundsAndContent(t *testing.T) {
	definition := Definition{
		Name: " \t\n", Description: strings.Repeat("é", MaxDescriptionBytes/2+1),
		Status: "deleted", Snapshot: validSnapshot(),
	}
	validation := ValidateDefinition(definition)
	require.True(t, issueAt(validation, "name", "definition_name_required"))
	require.True(t, issueAt(validation, "description", "description_too_long"), "limits are UTF-8 bytes")
	require.True(t, issueAt(validation, "status", "invalid_status"))

	definition.Name = "bad\x00name"
	validation = ValidateDefinition(definition)
	require.True(t, issueAt(validation, "name", "nul_forbidden"))

	definition.Name = string([]byte{0xff})
	validation = ValidateDefinition(definition)
	require.True(t, issueAt(validation, "name", "invalid_utf8"))
}

func TestSnapshotBoundsUseUTF8BytesAndRejectNUL(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.States[0].Name = strings.Repeat("é", MaxNameBytes/2+1)
	snapshot.States[0].EntryText = strings.Repeat("x", MaxEntryTextBytes+1)
	snapshot.States[0].Exits[0].Description = "bad\x00description"
	validation := ValidateSnapshot(snapshot)
	require.True(t, issueAt(validation, "snapshot.states[0].name", "state_name_too_long"))
	require.True(t, issueAt(validation, "snapshot.states[0].entryText", "entry_text_too_long"))
	require.True(t, issueAt(validation, "snapshot.states[0].exits[0].description", "nul_forbidden"))
	require.False(t, IsPhase0Runnable(snapshot))
}

func TestStateIdentityNameAndEntryValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		path   string
		code   string
	}{
		{"no states", func(s *Snapshot) { s.States = nil }, "snapshot.states", "state_required"},
		{"entry absent", func(s *Snapshot) { s.EntryStateID = "" }, "snapshot.entryStateId", "entry_state_required"},
		{"entry unknown", func(s *Snapshot) { s.EntryStateID = stateB }, "snapshot.entryStateId", "entry_state_not_found"},
		{"uppercase entry uuid", func(s *Snapshot) { s.EntryStateID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }, "snapshot.entryStateId", "invalid_state_id"},
		{"empty state id", func(s *Snapshot) { s.States[0].StateID = "" }, "snapshot.states[0].stateId", "state_id_required"},
		{"empty state name", func(s *Snapshot) { s.States[0].Name = "\n " }, "snapshot.states[0].name", "state_name_required"},
		{"reserved name", func(s *Snapshot) { s.States[0].Name = " complete " }, "snapshot.states[0].name", "reserved_state_name"},
		{"no exits", func(s *Snapshot) { s.States[0].Exits = nil }, "snapshot.states[0].exits", "exit_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSnapshot()
			test.mutate(&snapshot)
			validation := ValidateSnapshot(snapshot)
			require.False(t, validation.Valid)
			require.True(t, issueAt(validation, test.path, test.code), "%+v", validation.Issues)
			require.False(t, IsPhase0Runnable(snapshot))
		})
	}
}

func TestDuplicateStateIDsNamesAndMultipleEntryAreReportedAtEveryState(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.States = append(snapshot.States, State{
		StateID: stateA, Name: "hello", Exits: []Exit{{TargetKind: ExitTargetComplete}},
	})
	validation := ValidateSnapshot(snapshot)
	for _, path := range []string{"snapshot.states[0].stateId", "snapshot.states[1].stateId"} {
		require.True(t, issueAt(validation, path, "duplicate_state_id"))
	}
	for _, path := range []string{"snapshot.states[0].name", "snapshot.states[1].name"} {
		require.True(t, issueAt(validation, path, "duplicate_state_name"))
	}
	require.True(t, issueAt(validation, "snapshot.entryStateId", "multiple_entry_states"))
}

func TestExitDiscriminatorTargetExistenceAndDuplicateTargets(t *testing.T) {
	t.Run("complete target id forbidden", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits[0].TargetStateID = target(stateB)
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetStateId", "target_state_id_forbidden"))
	})
	t.Run("state target id required", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits[0] = Exit{TargetKind: ExitTargetState}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetStateId", "target_state_id_required"))
	})
	t.Run("unknown target", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits[0] = Exit{TargetKind: ExitTargetState, TargetStateID: target(stateB)}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetStateId", "target_state_not_found"))
	})
	t.Run("invalid discriminator", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits[0] = Exit{TargetKind: "done", TargetStateID: target(stateA)}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetKind", "invalid_target_kind"))
		require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetStateId", "target_state_id_forbidden"))
	})
	t.Run("duplicate completion", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits = append(snapshot.States[0].Exits, Exit{TargetKind: ExitTargetComplete})
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[1].targetKind", "duplicate_exit_target"))
	})
	t.Run("duplicate state target", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States = append(snapshot.States, State{StateID: stateB, Name: "next", Exits: []Exit{{TargetKind: ExitTargetComplete}}})
		snapshot.States[0].Exits = []Exit{
			{TargetKind: ExitTargetState, TargetStateID: target(stateB)},
			{TargetKind: ExitTargetState, TargetStateID: target(stateB)},
		}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[0].exits[1].targetStateId", "duplicate_exit_target"))
	})
}

func TestGraphReachabilityAndCompletionPaths(t *testing.T) {
	t.Run("unreachable state", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States = append(snapshot.States, State{StateID: stateB, Name: "orphan", Exits: []Exit{{TargetKind: ExitTargetComplete}}})
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[1]", "unreachable_state"))
	})
	t.Run("entry cannot complete", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States[0].Exits = []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateA)}}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.entryStateId", "completion_unreachable"))
		require.True(t, issueAt(validation, "snapshot.states[0]", "no_completion_path"))
	})
	t.Run("reachable state cannot complete", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States = []State{
			{StateID: stateA, Name: "entry", Exits: []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateB)}, {TargetKind: ExitTargetComplete}}},
			{StateID: stateB, Name: "trap", Exits: []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateB)}}},
		}
		validation := ValidateSnapshot(snapshot)
		require.True(t, issueAt(validation, "snapshot.states[1]", "no_completion_path"))
	})
	t.Run("cycle with completion is graph-valid but phase0 unsupported", func(t *testing.T) {
		snapshot := validSnapshot()
		snapshot.States = []State{
			{StateID: stateA, Name: "a", Exits: []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateB)}}},
			{StateID: stateB, Name: "b", Exits: []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateA)}, {TargetKind: ExitTargetComplete}}},
		}
		validation := ValidateSnapshot(snapshot)
		require.Equal(t, []string{"phase0_unsupported_graph"}, issueCodes(validation), "%+v", validation.Issues)
	})
}

func TestEveryPhase0RunnableRestriction(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		path   string
	}{
		{"nonempty inputs", func(s *Snapshot) { s.Inputs = []Input{{}} }, "snapshot.inputs"},
		{"multiple states", func(s *Snapshot) {
			s.States[0].Exits = []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateB)}}
			s.States = append(s.States, State{StateID: stateB, Name: "other", Exits: []Exit{{TargetKind: ExitTargetComplete}}})
		}, "snapshot.states"},
		{"state-target exit", func(s *Snapshot) {
			s.States[0].Exits = []Exit{{TargetKind: ExitTargetState, TargetStateID: target(stateA)}, {TargetKind: ExitTargetComplete}}
		}, "snapshot.states"},
		{"multiple exits", func(s *Snapshot) {
			s.States[0].Exits = append(s.States[0].Exits, Exit{TargetKind: ExitTargetState, TargetStateID: target(stateA)})
		}, "snapshot.states"},
		{"compaction", func(s *Snapshot) { s.States[0].CompactOnExit = true }, "snapshot.states[0].compactOnExit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validSnapshot()
			test.mutate(&snapshot)
			validation := ValidateSnapshot(snapshot)
			require.True(t, issueAt(validation, test.path, "phase0_unsupported_graph"), "%+v", validation.Issues)
			require.False(t, IsPhase0Runnable(snapshot))
		})
	}
}

func TestValidationIssuesAreSortedByPathThenCode(t *testing.T) {
	snapshot := Snapshot{Format: "wrong", Inputs: nil, EntryStateID: "bad", States: []State{{Name: "", EntryText: "bad\x00", CompactOnExit: true}}}
	first := ValidateSnapshot(snapshot)
	second := ValidateSnapshot(snapshot)
	require.Equal(t, first, second)
	require.False(t, first.Valid)
	for i := 1; i < len(first.Issues); i++ {
		previous, current := first.Issues[i-1], first.Issues[i]
		require.True(t, previous.Path < current.Path || previous.Path == current.Path && previous.Code <= current.Code,
			"issues out of order: %+v then %+v", previous, current)
	}
}

func TestClosedJSONDecoder(t *testing.T) {
	validJSON := `{
		"format":"mecha.workflow-definition/v1",
		"inputs":[],
		"entryStateId":"11111111-1111-4111-8111-111111111111",
		"states":[{
			"stateId":"11111111-1111-4111-8111-111111111111",
			"name":"hello",
			"entryText":"hi",
			"compactOnExit":false,
			"exits":[{"targetKind":"complete","description":"done"}]
		}]
	}`
	snapshot, err := DecodeSnapshotJSON([]byte(validJSON))
	require.NoError(t, err)
	require.True(t, IsPhase0Runnable(snapshot))

	tests := []struct {
		name string
		json string
	}{
		{"unknown top field", strings.Replace(validJSON, `"inputs":[]`, `"unknown":1,"inputs":[]`, 1)},
		{"unknown nested field", strings.Replace(validJSON, `"entryText":"hi"`, `"entryText":"hi","unknown":true`, 1)},
		{"unknown exit field", strings.Replace(validJSON, `"description":"done"`, `"description":"done","name":"forged"`, 1)},
		{"duplicate field", strings.Replace(validJSON, `"format":"mecha.workflow-definition/v1"`, `"format":"mecha.workflow-definition/v1","format":"mecha.workflow-definition/v1"`, 1)},
		{"trailing value", validJSON + `{}`},
		{"lone high surrogate", strings.Replace(validJSON, `"name":"hello"`, `"name":"\ud800"`, 1)},
		{"lone low surrogate", strings.Replace(validJSON, `"name":"hello"`, `"name":"\udc00"`, 1)},
		{"invalid utf8", strings.Replace(validJSON, `"name":"hello"`, `"name":"`+string([]byte{0xff})+`"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSnapshotJSON([]byte(test.json))
			require.Error(t, err)
		})
	}

	paired := strings.Replace(validJSON, `"name":"hello"`, `"name":"\ud83d\ude00"`, 1)
	decoded, err := DecodeSnapshotJSON([]byte(paired))
	require.NoError(t, err)
	require.Equal(t, "😀", decoded.States[0].Name)
}

func TestDefinitionJSONDecoderIsClosedAtEveryLevel(t *testing.T) {
	definition := Definition{Name: "hello-world", Status: StatusEnabled, Snapshot: validSnapshot()}
	data, err := json.Marshal(definition)
	require.NoError(t, err)
	decoded, err := DecodeDefinitionJSON(data)
	require.NoError(t, err)
	require.Equal(t, definition, decoded)

	withUnknown := strings.Replace(string(data), `"status":"enabled"`, `"status":"enabled","version":1`, 1)
	_, err = DecodeDefinitionJSON([]byte(withUnknown))
	require.Error(t, err)
}

func TestExitTargetPresenceDistinguishesForbiddenEmptyValue(t *testing.T) {
	data := []byte(`{"format":"mecha.workflow-definition/v1","inputs":[],"entryStateId":"11111111-1111-4111-8111-111111111111","states":[{"stateId":"11111111-1111-4111-8111-111111111111","name":"hello","entryText":"","compactOnExit":false,"exits":[{"targetKind":"complete","targetStateId":""}]}]}`)
	snapshot, err := DecodeSnapshotJSON(data)
	require.NoError(t, err)
	validation := ValidateSnapshot(snapshot)
	require.True(t, issueAt(validation, "snapshot.states[0].exits[0].targetStateId", "target_state_id_forbidden"))
}
