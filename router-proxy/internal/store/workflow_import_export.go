package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// WorkflowTransitionExport models transitions between states using local state keys.
type WorkflowTransitionExport struct {
	TargetStateKey string `json:"target_state_key"`
	Description    string `json:"description"`
}

// WorkflowStateExport models an exported workflow state.
type WorkflowStateExport struct {
	Key          string                     `json:"key"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	EntryText    string                     `json:"entry_text"`
	IsEntryState bool                       `json:"is_entry_state"`
	AllowExit    bool                       `json:"allow_exit"`
	AllowedTools []string                   `json:"allowed_tools"`
	Reminders    []WorkflowReminderConfig   `json:"reminders"`
	Monitors     []WorkflowMonitorConfig    `json:"monitors"`
	UIPosX       float64                    `json:"ui_pos_x"`
	UIPosY       float64                    `json:"ui_pos_y"`
	Transitions  []WorkflowTransitionExport `json:"transitions"`
}

// WorkflowInfoExport models workflow metadata.
type WorkflowInfoExport struct {
	Name             string                       `json:"name"`
	Description      string                       `json:"description"`
	SnoozeDurationMS int                          `json:"snooze_duration_ms"`
	Variables        []WorkflowVariableDefinition `json:"variables"`
}

// WorkflowExportBundle represents a fully portable workflow graph bundle.
type WorkflowExportBundle struct {
	SchemaVersion int                   `json:"schema_version"`
	ExportedAt    string                `json:"exported_at"`
	Workflow      WorkflowInfoExport    `json:"workflow"`
	EntryStateKey string                `json:"entry_state_key"`
	States        []WorkflowStateExport `json:"states"`
}

// WorkflowsExportPayload models the full export payload for git sync.
type WorkflowsExportPayload struct {
	Format     string                 `json:"format"`
	Version    int                    `json:"version"`
	ExportedAt string                 `json:"exported_at"`
	Workflows  []WorkflowExportBundle `json:"workflows"`
}

// ExportAllWorkflows exports all workflows as portable bundles.
func (s *Store) ExportAllWorkflows(ctx context.Context) (*WorkflowsExportPayload, error) {
	workflows, err := s.ListWorkflows(ctx)
	if err != nil {
		return nil, err
	}

	bundles := make([]WorkflowExportBundle, 0, len(workflows))
	for _, wf := range workflows {
		bundle, err := s.ExportWorkflow(ctx, wf.ID)
		if err != nil {
			return nil, err
		}
		if bundle != nil {
			bundles = append(bundles, *bundle)
		}
	}

	return &WorkflowsExportPayload{
		Format:     "mecha-workflows",
		Version:    1,
		ExportedAt: formatTimestamp(s.clock.Now()),
		Workflows:  bundles,
	}, nil
}

// ReplaceAllWorkflows atomically replaces all existing workflows with the given bundles.
func (s *Store) ReplaceAllWorkflows(ctx context.Context, bundles []WorkflowExportBundle) error {
	// 1. Validate all bundles upfront before entering the transaction
	names := make(map[string]bool, len(bundles))
	for i, bundle := range bundles {
		if err := s.ValidateExportBundle(bundle); err != nil {
			return err
		}
		name := strings.TrimSpace(bundle.Workflow.Name)
		if names[name] {
			return &WorkflowValidationError{
				Field:   fmt.Sprintf("workflows[%d].name", i),
				Message: fmt.Sprintf("duplicate workflow name %q in bundle list", name),
			}
		}
		names[name] = true
	}

	// 2. Perform replacement in an immediate transaction
	return s.runImmediate(ctx, func() error {
		// Clean up existing workflows and states
		// Deleting workflow_states explicitly first prevents any foreign key or constraint issues
		if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_states`); err != nil {
			return fmt.Errorf("delete workflow_states: %w", err)
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM workflows`); err != nil {
			return fmt.Errorf("delete workflows: %w", err)
		}

		now := formatTimestamp(s.clock.Now())

		for _, bundle := range bundles {
			newWfID, err := s.ids.Next()
			if err != nil {
				return err
			}

			// Generate new UUIDs for each state
			keyToNewID := make(map[string]string, len(bundle.States))
			for _, st := range bundle.States {
				newID, err := s.ids.Next()
				if err != nil {
					return err
				}
				keyToNewID[st.Key] = newID
			}

			// Determine new entry_state_id
			var newEntryStateID *string
			if bundle.EntryStateKey != "" {
				if id, ok := keyToNewID[bundle.EntryStateKey]; ok {
					newEntryStateID = &id
				}
			}
			if newEntryStateID == nil {
				for _, st := range bundle.States {
					if st.IsEntryState {
						id := keyToNewID[st.Key]
						newEntryStateID = &id
						break
					}
				}
			}

			varsList := bundle.Workflow.Variables
			if varsList == nil {
				varsList = []WorkflowVariableDefinition{}
			}
			variablesJSON, err := marshalWorkflowVariables(varsList)
			if err != nil {
				return err
			}

			workflowName := strings.TrimSpace(bundle.Workflow.Name)
			workflowDesc := strings.TrimSpace(bundle.Workflow.Description)
			snoozeDurationMS := bundle.Workflow.SnoozeDurationMS
			if snoozeDurationMS <= 0 {
				snoozeDurationMS = 600000
			}

			// Insert workflow first (with NULL entry_state_id to satisfy FK constraint)
			_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?, ?)`,
				newWfID, workflowName, workflowDesc, snoozeDurationMS, variablesJSON, now, now)
			if err != nil {
				return fmt.Errorf("insert workflow %s: %w", workflowName, err)
			}

			// Insert all states
			for _, st := range bundle.States {
				newStateID := keyToNewID[st.Key]

				remappedTransitions := make([]WorkflowTransitionConfig, 0, len(st.Transitions))
				for _, tr := range st.Transitions {
					mappedTargetID, ok := keyToNewID[tr.TargetStateKey]
					if ok {
						remappedTransitions = append(remappedTransitions, WorkflowTransitionConfig{
							TargetStateID: mappedTargetID,
							Description:   tr.Description,
						})
					}
				}

				remindersJSON, err := marshalWorkflowReminders(st.Reminders)
				if err != nil {
					return err
				}

				monitorsCopy := st.Monitors
				if monitorsCopy == nil {
					monitorsCopy = []WorkflowMonitorConfig{}
				}
				monitorsJSON, err := marshalWorkflowMonitors(monitorsCopy)
				if err != nil {
					return err
				}

				sanitizedTools, err := sanitizeWorkflowAllowedTools(st.AllowedTools)
				if err != nil {
					return err
				}
				allowedToolsJSON, err := marshalWorkflowAllowedTools(sanitizedTools)
				if err != nil {
					return err
				}

				transitionsJSON, err := marshalWorkflowTransitions(remappedTransitions)
				if err != nil {
					return err
				}

				allowExitInt := 1
				if !st.AllowExit {
					allowExitInt = 0
				}

				_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, workflow_id, name, description, entry_text, reminders_json, monitors_json, allowed_tools_json, transitions_json, allow_exit, ui_pos_x, ui_pos_y, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					newStateID, newWfID, st.Name, st.Description, st.EntryText, remindersJSON, monitorsJSON, allowedToolsJSON, transitionsJSON, allowExitInt, st.UIPosX, st.UIPosY, now, now)
				if err != nil {
					return fmt.Errorf("insert workflow state %s: %w", st.Name, err)
				}
			}

			// Update entry_state_id if set
			if newEntryStateID != nil {
				_, err = s.db.ExecContext(ctx, `UPDATE workflows SET entry_state_id = ? WHERE id = ?`, *newEntryStateID, newWfID)
				if err != nil {
					return fmt.Errorf("update entry_state_id for workflow %s: %w", workflowName, err)
				}
			}
		}

		return nil
	})
}

// ExportWorkflow generates a portable bundle containing the workflow, its variables,
// all child states, and relative transition links.
func (s *Store) ExportWorkflow(ctx context.Context, workflowID string) (*WorkflowExportBundle, error) {
	wf, err := s.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, &WorkflowNotFoundError{ID: workflowID}
	}

	states, err := s.ListWorkflowStatesByWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	// Map internal state ID to portable key.
	// We can use state ID or state name or indexed key. Using state.ID as key ensures uniqueness
	// while preserving clear mapping.
	idToKey := make(map[string]string, len(states))
	for _, st := range states {
		idToKey[st.ID] = st.ID
	}

	var entryKey string
	if wf.EntryStateID != nil {
		if k, ok := idToKey[*wf.EntryStateID]; ok {
			entryKey = k
		}
	}

	exportedStates := make([]WorkflowStateExport, 0, len(states))
	for _, st := range states {
		key := idToKey[st.ID]
		isEntry := (wf.EntryStateID != nil && *wf.EntryStateID == st.ID)

		exportedTransitions := make([]WorkflowTransitionExport, 0, len(st.Transitions))
		for _, tr := range st.Transitions {
			targetKey, ok := idToKey[tr.TargetStateID]
			if ok {
				exportedTransitions = append(exportedTransitions, WorkflowTransitionExport{
					TargetStateKey: targetKey,
					Description:    tr.Description,
				})
			}
		}

		reminders := st.Reminders
		if reminders == nil {
			reminders = []WorkflowReminderConfig{}
		}
		monitors := st.Monitors
		if monitors == nil {
			monitors = []WorkflowMonitorConfig{}
		}
		allowedTools := st.AllowedTools
		if allowedTools == nil {
			allowedTools = []string{}
		}

		exportedStates = append(exportedStates, WorkflowStateExport{
			Key:          key,
			Name:         st.Name,
			Description:  st.Description,
			EntryText:    st.EntryText,
			IsEntryState: isEntry,
			AllowExit:    st.AllowExit,
			AllowedTools: allowedTools,
			Reminders:    reminders,
			Monitors:     monitors,
			UIPosX:       st.UIPosX,
			UIPosY:       st.UIPosY,
			Transitions:  exportedTransitions,
		})
	}

	vars := wf.Variables
	if vars == nil {
		vars = []WorkflowVariableDefinition{}
	}

	snoozeDurationMS := wf.SnoozeDurationMS
	if snoozeDurationMS <= 0 {
		snoozeDurationMS = 600000
	}

	bundle := &WorkflowExportBundle{
		SchemaVersion: 1,
		ExportedAt:    formatTimestamp(s.clock.Now()),
		Workflow: WorkflowInfoExport{
			Name:             wf.Name,
			Description:      wf.Description,
			SnoozeDurationMS: snoozeDurationMS,
			Variables:        vars,
		},
		EntryStateKey: entryKey,
		States:        exportedStates,
	}

	return bundle, nil
}

const (
	maxWorkflowMonitorNameBytes         = 256
	maxWorkflowMonitorDescriptionBytes  = 4096
	maxWorkflowMonitorInstructionsBytes = 65536
	maxWorkflowMonitorNudgeBytes        = 16384
	maxWorkflowMonitorIntervalSeconds   = 2592000
	maxWorkflowMonitorCooldownSeconds   = 2592000
)

func validateWorkflowMonitorConfig(prefix string, mon WorkflowMonitorConfig) *WorkflowValidationError {
	name := strings.TrimSpace(mon.Name)
	if name == "" {
		return &WorkflowValidationError{Field: prefix + ".name", Message: "watcher name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowMonitorNameBytes {
		return &WorkflowValidationError{Field: prefix + ".name", Message: "watcher name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowValidationError{Field: prefix + ".name", Message: "watcher name contains invalid characters"}
	}

	if len([]byte(mon.Description)) > maxWorkflowMonitorDescriptionBytes {
		return &WorkflowValidationError{Field: prefix + ".description", Message: "watcher description exceeds maximum byte length"}
	}
	if !utf8.ValidString(mon.Description) || strings.ContainsRune(mon.Description, 0) {
		return &WorkflowValidationError{Field: prefix + ".description", Message: "watcher description contains invalid characters"}
	}

	inst := strings.TrimSpace(mon.QuestionInstructions)
	if inst == "" {
		return &WorkflowValidationError{Field: prefix + ".question_instructions", Message: "question instructions must not be empty"}
	}
	if len([]byte(inst)) > maxWorkflowMonitorInstructionsBytes {
		return &WorkflowValidationError{Field: prefix + ".question_instructions", Message: "question instructions exceed maximum byte length"}
	}
	if !utf8.ValidString(inst) || strings.ContainsRune(inst, 0) {
		return &WorkflowValidationError{Field: prefix + ".question_instructions", Message: "question instructions contain invalid characters"}
	}

	nudge := strings.TrimSpace(mon.NudgeTemplate)
	if nudge == "" {
		return &WorkflowValidationError{Field: prefix + ".nudge_template", Message: "nudge template must not be empty"}
	}
	if len([]byte(nudge)) > maxWorkflowMonitorNudgeBytes {
		return &WorkflowValidationError{Field: prefix + ".nudge_template", Message: "nudge template exceeds maximum byte length"}
	}
	if !utf8.ValidString(nudge) || strings.ContainsRune(nudge, 0) {
		return &WorkflowValidationError{Field: prefix + ".nudge_template", Message: "nudge template contains invalid characters"}
	}

	if mon.IntervalSeconds < 0 || mon.IntervalSeconds > maxWorkflowMonitorIntervalSeconds {
		return &WorkflowValidationError{Field: prefix + ".interval_seconds", Message: "interval_seconds must be between 0 and 2592000"}
	}
	if mon.CooldownSeconds < 0 || mon.CooldownSeconds > maxWorkflowMonitorCooldownSeconds {
		return &WorkflowValidationError{Field: prefix + ".cooldown_seconds", Message: "cooldown_seconds must be between 0 and 2592000"}
	}
	if mon.MaxAlerts < 0 {
		return &WorkflowValidationError{Field: prefix + ".max_alerts", Message: "max_alerts must be >= 0"}
	}
	if mon.TrailingMessagesCount < 0 {
		return &WorkflowValidationError{Field: prefix + ".trailing_messages_count", Message: "trailing_messages_count must be >= 0"}
	}

	return nil
}

// ValidateBundle checks that bundle structure, states, transitions, tools, and variables are valid.
func (s *Store) ValidateExportBundle(bundle WorkflowExportBundle) error {
	if bundle.SchemaVersion < 1 {
		return &WorkflowValidationError{Field: "schema_version", Message: "schema_version must be >= 1"}
	}

	name := strings.TrimSpace(bundle.Workflow.Name)
	if name == "" {
		return &WorkflowValidationError{Field: "workflow.name", Message: "workflow name must not be empty"}
	}
	if len([]byte(name)) > maxWorkflowNameBytes {
		return &WorkflowValidationError{Field: "workflow.name", Message: "workflow name exceeds maximum byte length"}
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return &WorkflowValidationError{Field: "workflow.name", Message: "workflow name contains invalid characters"}
	}

	if len([]byte(bundle.Workflow.Description)) > maxWorkflowDescriptionBytes {
		return &WorkflowValidationError{Field: "workflow.description", Message: "workflow description exceeds maximum byte length"}
	}
	if !utf8.ValidString(bundle.Workflow.Description) || strings.ContainsRune(bundle.Workflow.Description, 0) {
		return &WorkflowValidationError{Field: "workflow.description", Message: "workflow description contains invalid characters"}
	}

	if err := validateWorkflowVariables(bundle.Workflow.Variables); err != nil {
		return err
	}

	if len(bundle.States) == 0 {
		return &WorkflowValidationError{Field: "states", Message: "workflow bundle must contain at least one state"}
	}

	stateKeys := make(map[string]bool, len(bundle.States))
	stateNames := make(map[string]bool, len(bundle.States))

	for i, st := range bundle.States {
		k := strings.TrimSpace(st.Key)
		if k == "" {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].key", i), Message: "state key must not be empty"}
		}
		if stateKeys[k] {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].key", i), Message: fmt.Sprintf("duplicate state key %q", k)}
		}
		stateKeys[k] = true

		stName := strings.TrimSpace(st.Name)
		if stName == "" {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].name", i), Message: "state name must not be empty"}
		}
		if len([]byte(stName)) > maxWorkflowStateNameBytes {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].name", i), Message: "state name exceeds maximum byte length"}
		}
		if !utf8.ValidString(stName) || strings.ContainsRune(stName, 0) {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].name", i), Message: "state name contains invalid characters"}
		}
		if stateNames[stName] {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].name", i), Message: fmt.Sprintf("duplicate state name %q in bundle", stName)}
		}
		stateNames[stName] = true

		if len([]byte(st.Description)) > maxWorkflowStateDescriptionBytes {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].description", i), Message: "description exceeds maximum byte length"}
		}
		if !utf8.ValidString(st.Description) || strings.ContainsRune(st.Description, 0) {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].description", i), Message: "description contains invalid characters"}
		}

		if len([]byte(st.EntryText)) > maxWorkflowStateEntryTextBytes {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].entry_text", i), Message: "entry text exceeds maximum byte length"}
		}
		if !utf8.ValidString(st.EntryText) || strings.ContainsRune(st.EntryText, 0) {
			return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].entry_text", i), Message: "entry text contains invalid characters"}
		}

		// Validate reminders
		for ri, rem := range st.Reminders {
			remID := strings.TrimSpace(rem.ID)
			if remID == "" {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].id", i, ri), Message: "id must be non-empty"}
			}
			if rem.Type != WorkflowReminderOneShot && rem.Type != WorkflowReminderRecurring {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].type", i, ri), Message: "type must be 'one_shot' or 'recurring'"}
			}
			if rem.Body == "" {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].body", i, ri), Message: "body must not be empty"}
			}
			if len([]byte(rem.Body)) > maxWorkflowReminderBodyBytes {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].body", i, ri), Message: "body exceeds maximum byte length"}
			}
			if rem.DelaySeconds < 1 || rem.DelaySeconds > maxWorkflowReminderDelaySeconds {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].delay_seconds", i, ri), Message: "delay_seconds must be between 1 and 2592000"}
			}
			if rem.Type == WorkflowReminderRecurring {
				if rem.IntervalSeconds == nil || *rem.IntervalSeconds < 1 {
					return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].reminders[%d].interval_seconds", i, ri), Message: "interval_seconds must be >= 1 for recurring"}
				}
			}
		}

		// Validate monitors
		for mi, mon := range st.Monitors {
			if err := validateWorkflowMonitorConfig(fmt.Sprintf("states[%d].monitors[%d]", i, mi), mon); err != nil {
				return err
			}
		}

		// Validate allowed tools
		if _, err := sanitizeWorkflowAllowedTools(st.AllowedTools); err != nil {
			return err
		}
	}

	// Validate transitions target keys
	for i, st := range bundle.States {
		stKey := strings.TrimSpace(st.Key)
		seenTargets := make(map[string]bool, len(st.Transitions))
		for ti, tr := range st.Transitions {
			tKey := strings.TrimSpace(tr.TargetStateKey)
			if tKey == "" {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].target_state_key", i, ti), Message: "target_state_key must not be empty"}
			}
			if !stateKeys[tKey] {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].target_state_key", i, ti), Message: fmt.Sprintf("target state key %q not found in bundle", tKey)}
			}
			if tKey == stKey {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].target_state_key", i, ti), Message: "self-transition is not permitted"}
			}
			if seenTargets[tKey] {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].target_state_key", i, ti), Message: fmt.Sprintf("duplicate transition to key %q", tKey)}
			}
			seenTargets[tKey] = true

			desc := strings.TrimSpace(tr.Description)
			if len([]byte(desc)) > maxWorkflowTransitionDescriptionBytes {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].description", i, ti), Message: "description exceeds maximum byte length"}
			}
			if !utf8.ValidString(desc) || strings.ContainsRune(desc, 0) {
				return &WorkflowValidationError{Field: fmt.Sprintf("states[%d].transitions[%d].description", i, ti), Message: "description contains invalid characters"}
			}
		}
	}

	// Validate entry_state_key if provided
	if bundle.EntryStateKey != "" {
		if !stateKeys[bundle.EntryStateKey] {
			return &WorkflowValidationError{Field: "entry_state_key", Message: fmt.Sprintf("entry_state_key %q not found in bundle states", bundle.EntryStateKey)}
		}
	}

	return nil
}

// ImportWorkflow creates a new workflow with newly generated IDs from a bundle in a single transaction.
func (s *Store) ImportWorkflow(ctx context.Context, bundle WorkflowExportBundle) (*Workflow, []WorkflowState, error) {
	if err := s.ValidateExportBundle(bundle); err != nil {
		return nil, nil, err
	}

	var createdWorkflow *Workflow
	var createdStates []WorkflowState

	err := s.runImmediate(ctx, func() error {
		// Determine unique name if name already exists
		desiredName := strings.TrimSpace(bundle.Workflow.Name)
		candidateName := desiredName

		var existingID string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ?`, candidateName).Scan(&existingID)
		if err == nil {
			// Name exists; generate unique suffix: "Name (Imported)" or "Name (Imported 2)", etc.
			for suffix := 1; ; suffix++ {
				if suffix == 1 {
					candidateName = fmt.Sprintf("%s (Imported)", desiredName)
				} else {
					candidateName = fmt.Sprintf("%s (Imported %d)", desiredName, suffix)
				}
				var dummy string
				err := s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ?`, candidateName).Scan(&dummy)
				if errors.Is(err, sql.ErrNoRows) {
					break
				}
				if err != nil {
					return err
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		newWfID, err := s.ids.Next()
		if err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())

		// Generate new UUIDs for every state in the bundle
		keyToNewID := make(map[string]string, len(bundle.States))
		for _, st := range bundle.States {
			newID, err := s.ids.Next()
			if err != nil {
				return err
			}
			keyToNewID[st.Key] = newID
		}

		// Determine new entry_state_id
		var newEntryStateID *string
		if bundle.EntryStateKey != "" {
			if id, ok := keyToNewID[bundle.EntryStateKey]; ok {
				newEntryStateID = &id
			}
		}
		if newEntryStateID == nil {
			for _, st := range bundle.States {
				if st.IsEntryState {
					id := keyToNewID[st.Key]
					newEntryStateID = &id
					break
				}
			}
		}

		varsList := bundle.Workflow.Variables
		if varsList == nil {
			varsList = []WorkflowVariableDefinition{}
		}
		variablesJSON, err := marshalWorkflowVariables(varsList)
		if err != nil {
			return err
		}

		snoozeDurationMS := bundle.Workflow.SnoozeDurationMS
		if snoozeDurationMS <= 0 {
			snoozeDurationMS = 600000
		}

		// Insert workflow first (with NULL entry_state_id to satisfy FK constraint)
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflows (id, name, description, entry_state_id, snooze_duration_ms, variables_json, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?, ?)`,
			newWfID, candidateName, strings.TrimSpace(bundle.Workflow.Description), snoozeDurationMS, variablesJSON, now, now)
		if err != nil {
			if isConstraintError(err) {
				return &WorkflowNameConflictError{Name: candidateName}
			}
			return err
		}

		createdStates = make([]WorkflowState, 0, len(bundle.States))

		// Insert all states
		for _, st := range bundle.States {
			newStateID := keyToNewID[st.Key]

			// Remap transitions
			remappedTransitions := make([]WorkflowTransitionConfig, 0, len(st.Transitions))
			for _, tr := range st.Transitions {
				mappedTargetID, ok := keyToNewID[tr.TargetStateKey]
				if ok {
					remappedTransitions = append(remappedTransitions, WorkflowTransitionConfig{
						TargetStateID: mappedTargetID,
						Description:   tr.Description,
					})
				}
			}

			remindersJSON, err := marshalWorkflowReminders(st.Reminders)
			if err != nil {
				return err
			}

			monitorsCopy := st.Monitors
			if monitorsCopy == nil {
				monitorsCopy = []WorkflowMonitorConfig{}
			}
			monitorsJSON, err := marshalWorkflowMonitors(monitorsCopy)
			if err != nil {
				return err
			}

			sanitizedTools, err := sanitizeWorkflowAllowedTools(st.AllowedTools)
			if err != nil {
				return err
			}
			allowedToolsJSON, err := marshalWorkflowAllowedTools(sanitizedTools)
			if err != nil {
				return err
			}

			transitionsJSON, err := marshalWorkflowTransitions(remappedTransitions)
			if err != nil {
				return err
			}

			allowExitInt := 1
			if !st.AllowExit {
				allowExitInt = 0
			}

			_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_states (id, workflow_id, name, description, entry_text, reminders_json, monitors_json, allowed_tools_json, transitions_json, allow_exit, ui_pos_x, ui_pos_y, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				newStateID, newWfID, st.Name, st.Description, st.EntryText, remindersJSON, monitorsJSON, allowedToolsJSON, transitionsJSON, allowExitInt, st.UIPosX, st.UIPosY, now, now)
			if err != nil {
				return err
			}

			createdStates = append(createdStates, WorkflowState{
				ID:           newStateID,
				WorkflowID:   &newWfID,
				Name:         st.Name,
				Description:  st.Description,
				EntryText:    st.EntryText,
				Reminders:    st.Reminders,
				Monitors:     monitorsCopy,
				AllowedTools: sanitizedTools,
				Transitions:  remappedTransitions,
				AllowExit:    st.AllowExit,
				UIPosX:       st.UIPosX,
				UIPosY:       st.UIPosY,
				CreatedAt:    now,
				UpdatedAt:    now,
			})
		}

		// Update entry_state_id on workflow if determined
		if newEntryStateID != nil {
			_, err = s.db.ExecContext(ctx, `UPDATE workflows SET entry_state_id = ? WHERE id = ?`, *newEntryStateID, newWfID)
			if err != nil {
				return err
			}
		}

		createdWorkflow = &Workflow{
			ID:               newWfID,
			Name:             candidateName,
			Description:      strings.TrimSpace(bundle.Workflow.Description),
			EntryStateID:     newEntryStateID,
			SnoozeDurationMS: snoozeDurationMS,
			Variables:        varsList,
			CreatedAt:        now,
			UpdatedAt:        now,
			StateCount:       len(bundle.States),
		}

		return nil
	})

	return createdWorkflow, createdStates, err
}
