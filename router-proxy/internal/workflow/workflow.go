// Package workflow defines and validates versioned managed-workflow definitions.
// It is intentionally independent of transport and persistence concerns.
// Required names are tested with strings.TrimSpace (Unicode whitespace), while
// accepted values are preserved verbatim and uniqueness remains case-sensitive.
package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	SnapshotFormat = "mecha.workflow-definition/v1"

	MaxNameBytes        = 256
	MaxDescriptionBytes = 4096
	MaxEntryTextBytes   = 32768
)

type DefinitionStatus string

const (
	StatusEnabled  DefinitionStatus = "enabled"
	StatusDisabled DefinitionStatus = "disabled"
	StatusArchived DefinitionStatus = "archived"
)

type ExitTargetKind string

const (
	ExitTargetState    ExitTargetKind = "state"
	ExitTargetComplete ExitTargetKind = "complete"
)

// Definition is the author-editable portion of a definition. IDs, revisions,
// versions, and timestamps belong to persistence projections rather than this
// validation model.
type Definition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Status      DefinitionStatus `json:"status"`
	Snapshot    Snapshot         `json:"snapshot"`
}

// Input is deliberately empty in Phase 0. Its presence makes a snapshot
// non-runnable; the closed empty type prevents accidentally accepting a future
// input declaration contract before that contract exists.
type Input struct{}

type Snapshot struct {
	Format       string  `json:"format"`
	Inputs       []Input `json:"inputs"`
	EntryStateID string  `json:"entryStateId"`
	States       []State `json:"states"`
}

type State struct {
	StateID       string `json:"stateId"`
	Name          string `json:"name"`
	EntryText     string `json:"entryText"`
	CompactOnExit bool   `json:"compactOnExit"`
	Exits         []Exit `json:"exits"`
}

type Exit struct {
	TargetKind    ExitTargetKind `json:"targetKind"`
	TargetStateID *string        `json:"targetStateId,omitempty"`
	Description   string         `json:"description,omitempty"`
}

type ValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Validation struct {
	Valid  bool              `json:"valid"`
	Issues []ValidationIssue `json:"issues"`
}

// DecodeDefinitionJSON and DecodeSnapshotJSON enforce the closed JSON shapes,
// duplicate-key/trailing-value rules, UTF-8, and lone-surrogate rule before
// semantic validation. Missing or incomplete values remain validator issues so
// incomplete drafts can still be represented by the domain model.
func DecodeDefinitionJSON(data []byte) (Definition, error) {
	var value Definition
	if err := decodeClosedJSON(data, &value); err != nil {
		return Definition{}, err
	}
	return value, nil
}

func DecodeSnapshotJSON(data []byte) (Snapshot, error) {
	var value Snapshot
	if err := decodeClosedJSON(data, &value); err != nil {
		return Snapshot{}, err
	}
	return value, nil
}

func decodeClosedJSON(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("workflow JSON is not valid UTF-8")
	}
	if err := rejectLoneSurrogates(data); err != nil {
		return err
	}
	if err := rejectDuplicateKeysAndTrailingValues(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid workflow JSON: %w", err)
	}
	return nil
}

func rejectDuplicateKeysAndTrailingValues(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	if err := walk(); err != nil {
		return fmt.Errorf("invalid workflow JSON: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid workflow JSON: trailing value")
		}
		return fmt.Errorf("invalid workflow JSON: %w", err)
	}
	return nil
}

func rejectLoneSurrogates(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		for i++; i < len(data); i++ {
			switch data[i] {
			case '"':
				goto stringDone
			case '\\':
				if i+1 >= len(data) {
					return fmt.Errorf("invalid workflow JSON: incomplete escape")
				}
				i++
				if data[i] != 'u' {
					continue
				}
				first, ok := decodeHex4(data, i+1)
				if !ok {
					return fmt.Errorf("invalid workflow JSON: malformed unicode escape")
				}
				i += 4
				if 0xD800 <= first && first <= 0xDBFF {
					if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
						return fmt.Errorf("invalid workflow JSON: lone surrogate")
					}
					second, ok := decodeHex4(data, i+3)
					if !ok || second < 0xDC00 || second > 0xDFFF || utf16.DecodeRune(rune(first), rune(second)) == utf8.RuneError {
						return fmt.Errorf("invalid workflow JSON: lone surrogate")
					}
					i += 6
				} else if 0xDC00 <= first && first <= 0xDFFF {
					return fmt.Errorf("invalid workflow JSON: lone surrogate")
				}
			}
		}
	stringDone:
	}
	return nil
}

func decodeHex4(data []byte, start int) (uint16, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, c := range data[start : start+4] {
		value <<= 4
		switch {
		case '0' <= c && c <= '9':
			value += uint16(c - '0')
		case 'a' <= c && c <= 'f':
			value += uint16(c-'a') + 10
		case 'A' <= c && c <= 'F':
			value += uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func (definition Definition) Validate() Validation { return ValidateDefinition(definition) }

func ValidateDefinition(definition Definition) Validation {
	issues := make([]ValidationIssue, 0)
	checkRequiredText(&issues, "name", definition.Name, MaxNameBytes, "definition_name")
	checkText(&issues, "description", definition.Description, MaxDescriptionBytes, "description")
	if definition.Status != StatusEnabled && definition.Status != StatusDisabled && definition.Status != StatusArchived {
		add(&issues, "status", "invalid_status", "Status must be enabled, disabled, or archived.")
	}
	issues = append(issues, validateSnapshot(definition.Snapshot)...)
	return finish(issues)
}

func (snapshot Snapshot) Validate() Validation { return ValidateSnapshot(snapshot) }

func ValidateSnapshot(snapshot Snapshot) Validation { return finish(validateSnapshot(snapshot)) }

func validateSnapshot(snapshot Snapshot) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	if snapshot.Format != SnapshotFormat {
		add(&issues, "snapshot.format", "invalid_format", "Format must be mecha.workflow-definition/v1.")
	}
	if snapshot.Inputs == nil {
		add(&issues, "snapshot.inputs", "inputs_required", "Inputs must be an array.")
	} else if len(snapshot.Inputs) != 0 {
		add(&issues, "snapshot.inputs", "phase0_unsupported_graph", "Phase 0 requires inputs to be empty.")
	}
	if len(snapshot.States) == 0 {
		add(&issues, "snapshot.states", "state_required", "At least one state is required.")
	}
	if snapshot.EntryStateID == "" {
		add(&issues, "snapshot.entryStateId", "entry_state_required", "EntryStateId is required.")
	} else if !isCanonicalUUID(snapshot.EntryStateID) {
		add(&issues, "snapshot.entryStateId", "invalid_state_id", "EntryStateId must be a canonical lowercase UUID.")
	}

	ids := make(map[string][]int)
	names := make(map[string][]int)
	for i, state := range snapshot.States {
		base := fmt.Sprintf("snapshot.states[%d]", i)
		if state.StateID == "" {
			add(&issues, base+".stateId", "state_id_required", "StateId is required.")
		} else {
			if !isCanonicalUUID(state.StateID) {
				add(&issues, base+".stateId", "invalid_state_id", "StateId must be a canonical lowercase UUID.")
			}
			ids[state.StateID] = append(ids[state.StateID], i)
		}
		checkRequiredText(&issues, base+".name", state.Name, MaxNameBytes, "state_name")
		if strings.TrimSpace(state.Name) == "complete" {
			add(&issues, base+".name", "reserved_state_name", "State name complete is reserved.")
		}
		if strings.TrimSpace(state.Name) != "" {
			names[state.Name] = append(names[state.Name], i)
		}
		checkText(&issues, base+".entryText", state.EntryText, MaxEntryTextBytes, "entry_text")
		if state.CompactOnExit {
			add(&issues, base+".compactOnExit", "phase0_unsupported_graph", "Phase 0 requires compactOnExit to be false.")
		}
		if len(state.Exits) == 0 {
			add(&issues, base+".exits", "exit_required", "At least one exit is required.")
		}
		seenTargets := make(map[string]bool)
		for j, exit := range state.Exits {
			exitBase := fmt.Sprintf("%s.exits[%d]", base, j)
			checkText(&issues, exitBase+".description", exit.Description, MaxDescriptionBytes, "exit_description")
			switch exit.TargetKind {
			case ExitTargetComplete:
				if exit.TargetStateID != nil {
					add(&issues, exitBase+".targetStateId", "target_state_id_forbidden", "A complete exit must not contain targetStateId.")
				}
				if seenTargets["complete"] {
					add(&issues, exitBase+".targetKind", "duplicate_exit_target", "A state may have only one exit to a target.")
				}
				seenTargets["complete"] = true
			case ExitTargetState:
				if exit.TargetStateID == nil || *exit.TargetStateID == "" {
					add(&issues, exitBase+".targetStateId", "target_state_id_required", "A state exit requires targetStateId.")
				} else {
					target := *exit.TargetStateID
					if !isCanonicalUUID(target) {
						add(&issues, exitBase+".targetStateId", "invalid_state_id", "TargetStateId must be a canonical lowercase UUID.")
					}
					if seenTargets["state:"+target] {
						add(&issues, exitBase+".targetStateId", "duplicate_exit_target", "A state may have only one exit to a target.")
					}
					seenTargets["state:"+target] = true
				}
			default:
				add(&issues, exitBase+".targetKind", "invalid_target_kind", "TargetKind must be state or complete.")
				if exit.TargetStateID != nil {
					add(&issues, exitBase+".targetStateId", "target_state_id_forbidden", "An exit with an invalid targetKind must not contain targetStateId.")
				}
			}
		}
	}
	for id, indexes := range ids {
		if id != "" && len(indexes) > 1 {
			for _, index := range indexes {
				add(&issues, fmt.Sprintf("snapshot.states[%d].stateId", index), "duplicate_state_id", "State IDs must be unique.")
			}
		}
	}
	for _, indexes := range names {
		if len(indexes) > 1 {
			for _, index := range indexes {
				add(&issues, fmt.Sprintf("snapshot.states[%d].name", index), "duplicate_state_name", "State names must be unique.")
			}
		}
	}

	entryIndexes := ids[snapshot.EntryStateID]
	if snapshot.EntryStateID != "" {
		switch len(entryIndexes) {
		case 0:
			add(&issues, "snapshot.entryStateId", "entry_state_not_found", "EntryStateId must reference a state in this revision.")
		case 1:
		default:
			add(&issues, "snapshot.entryStateId", "multiple_entry_states", "EntryStateId must identify exactly one state.")
		}
	}
	for i, state := range snapshot.States {
		for j, exit := range state.Exits {
			if exit.TargetKind == ExitTargetState && exit.TargetStateID != nil && len(ids[*exit.TargetStateID]) == 0 {
				add(&issues, fmt.Sprintf("snapshot.states[%d].exits[%d].targetStateId", i, j), "target_state_not_found", "TargetStateId must reference a state in this revision.")
			}
		}
	}

	if len(entryIndexes) == 1 {
		validateGraph(&issues, snapshot, ids, entryIndexes[0])
	}
	if !hasNonPhase0Issue(issues) && !phase0GraphShape(snapshot, entryIndexes) {
		add(&issues, "snapshot.states", "phase0_unsupported_graph", "Phase 0 supports exactly one entry state with exactly one complete exit.")
	}
	return issues
}

func hasNonPhase0Issue(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Code != "phase0_unsupported_graph" {
			return true
		}
	}
	return false
}

func validateGraph(issues *[]ValidationIssue, snapshot Snapshot, ids map[string][]int, entry int) {
	reachable := make([]bool, len(snapshot.States))
	stack := []int{entry}
	reachable[entry] = true
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, exit := range snapshot.States[i].Exits {
			if exit.TargetKind != ExitTargetState || exit.TargetStateID == nil || len(ids[*exit.TargetStateID]) != 1 {
				continue
			}
			next := ids[*exit.TargetStateID][0]
			if !reachable[next] {
				reachable[next] = true
				stack = append(stack, next)
			}
		}
	}
	for i, yes := range reachable {
		if !yes {
			add(issues, fmt.Sprintf("snapshot.states[%d]", i), "unreachable_state", "State must be reachable from the entry state.")
		}
	}

	canComplete := make([]bool, len(snapshot.States))
	for i, state := range snapshot.States {
		for _, exit := range state.Exits {
			if exit.TargetKind == ExitTargetComplete && exit.TargetStateID == nil {
				canComplete[i] = true
				break
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for i, state := range snapshot.States {
			if canComplete[i] {
				continue
			}
			for _, exit := range state.Exits {
				if exit.TargetKind == ExitTargetState && exit.TargetStateID != nil && len(ids[*exit.TargetStateID]) == 1 && canComplete[ids[*exit.TargetStateID][0]] {
					canComplete[i] = true
					changed = true
					break
				}
			}
		}
	}
	if !canComplete[entry] {
		add(issues, "snapshot.entryStateId", "completion_unreachable", "A completion exit must be reachable from the entry state.")
	}
	for i := range snapshot.States {
		if reachable[i] && !canComplete[i] {
			add(issues, fmt.Sprintf("snapshot.states[%d]", i), "no_completion_path", "Every reachable state must have a path to completion.")
		}
	}
}

// IsPhase0Runnable is deliberately stricter than general graph validity. It is
// true only for the exact one-state completion slice and only when all format,
// content, and graph validation succeeds.
func (snapshot Snapshot) Runnable() bool { return IsPhase0Runnable(snapshot) }

func IsPhase0Runnable(snapshot Snapshot) bool {
	validation := ValidateSnapshot(snapshot)
	if !validation.Valid {
		return false
	}
	entryIndexes := map[string][]int{}
	for i, state := range snapshot.States {
		entryIndexes[state.StateID] = append(entryIndexes[state.StateID], i)
	}
	return phase0Shape(snapshot, entryIndexes[snapshot.EntryStateID])
}

func phase0Shape(snapshot Snapshot, entryIndexes []int) bool {
	return snapshot.Format == SnapshotFormat && len(snapshot.Inputs) == 0 && phase0GraphShape(snapshot, entryIndexes) && !snapshot.States[0].CompactOnExit
}

func phase0GraphShape(snapshot Snapshot, entryIndexes []int) bool {
	if len(snapshot.States) != 1 || len(entryIndexes) != 1 || entryIndexes[0] != 0 {
		return false
	}
	state := snapshot.States[0]
	return state.StateID == snapshot.EntryStateID && strings.TrimSpace(state.Name) != "" && strings.TrimSpace(state.Name) != "complete" && len(state.Exits) == 1 && state.Exits[0].TargetKind == ExitTargetComplete && state.Exits[0].TargetStateID == nil
}

func checkRequiredText(issues *[]ValidationIssue, path, value string, max int, label string) {
	checkText(issues, path, value, max, label)
	if strings.TrimSpace(value) == "" {
		add(issues, path, label+"_required", "Value must not be empty after trimming whitespace.")
	}
}

func checkText(issues *[]ValidationIssue, path, value string, max int, label string) {
	if !utf8.ValidString(value) {
		add(issues, path, "invalid_utf8", "Value must be valid UTF-8.")
	}
	if strings.IndexByte(value, 0) >= 0 {
		add(issues, path, "nul_forbidden", "Value must not contain NUL.")
	}
	if len(value) > max {
		add(issues, path, label+"_too_long", fmt.Sprintf("Value must be at most %d UTF-8 bytes.", max))
	}
}

func isCanonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !('0' <= value[i] && value[i] <= '9') && !('a' <= value[i] && value[i] <= 'f') {
			return false
		}
	}
	return true
}

func add(issues *[]ValidationIssue, path, code, message string) {
	*issues = append(*issues, ValidationIssue{Path: path, Code: code, Message: message})
}

func finish(issues []ValidationIssue) Validation {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Message < issues[j].Message
	})
	if issues == nil {
		issues = []ValidationIssue{}
	}
	return Validation{Valid: len(issues) == 0, Issues: issues}
}
