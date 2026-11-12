package store

import "context"

// SweepWorkflowJournal enforces bounded journal retention.
func (s *Store) SweepWorkflowJournal(ctx context.Context) error {
	return s.runImmediate(ctx, func() error { return s.journalSweepTx(ctx) })
}
