package managed

import (
	"context"
	"sync"
)

type goalEvaluationFlight struct {
	revision int64
	cancel   context.CancelFunc
}

// goalFlightsMu protects registration against concurrent superseding updates.
var goalFlightsMu sync.Mutex
var goalFlights = map[*Manager]map[string]map[*goalEvaluationFlight]struct{}{}

func (m *Manager) registerGoalEvaluation(agentID string, revision int64) (context.Context, func()) {
	ctx, cancel := context.WithCancel(m.runCtx)
	flight := &goalEvaluationFlight{revision: revision, cancel: cancel}
	goalFlightsMu.Lock()
	if goalFlights[m] == nil {
		goalFlights[m] = map[string]map[*goalEvaluationFlight]struct{}{}
	}
	if goalFlights[m][agentID] == nil {
		goalFlights[m][agentID] = map[*goalEvaluationFlight]struct{}{}
	}
	goalFlights[m][agentID][flight] = struct{}{}
	goalFlightsMu.Unlock()
	return ctx, func() {
		cancel()
		goalFlightsMu.Lock()
		delete(goalFlights[m][agentID], flight)
		if len(goalFlights[m][agentID]) == 0 {
			delete(goalFlights[m], agentID)
		}
		if len(goalFlights[m]) == 0 {
			delete(goalFlights, m)
		}
		goalFlightsMu.Unlock()
	}
}

// cancelGoalEvaluations cancels current flights after a successful Claude restart reservation.
func (m *Manager) cancelGoalEvaluations(agentID string) {
	goalFlightsMu.Lock()
	defer goalFlightsMu.Unlock()
	for flight := range goalFlights[m][agentID] {
		flight.cancel()
	}
}
