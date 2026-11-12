package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
)

// ReplayWorkflowJournalNote checks the durable receipt before mutable tool
// admission. A miss does not reserve an operation or perform a mutation.
func (s *Store) ReplayWorkflowJournalNote(ctx context.Context, agent, operationID string, in WorkflowJournalNoteInput) (WorkflowJournalMutationResult, bool, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return WorkflowJournalMutationResult{}, false, err
	}
	miss := errors.New("workflow journal receipt not found")
	encoded, replay, err := s.WorkflowJournalOperation(ctx, agent, "note", operationID, fmt.Sprintf("%x", sha256.Sum256(payload)), func() (string, error) { return "", miss })
	if errors.Is(err, miss) {
		return WorkflowJournalMutationResult{}, false, nil
	}
	var result WorkflowJournalMutationResult
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &result)
	}
	return result, replay, err
}
