package managed

// Simulate reopening admission while retaining reset ownership during wake.
func (lane *managedAgentLane) reopenEmbeddedResetAdmissions() {
	lane.admission.Lock()
	lane.resetPending = false
	lane.admission.Unlock()
}
