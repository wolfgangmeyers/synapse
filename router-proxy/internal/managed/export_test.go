package managed

// SetEmbeddedBashAfterStartForTest installs a per-manager gate callback.
// Restore it only after the manager's runs have stopped.
func SetEmbeddedBashAfterStartForTest(m *Manager, hook func(int)) func() {
	previous := m.embeddedBashAfterStart
	m.embeddedBashAfterStart = hook
	return func() { m.embeddedBashAfterStart = previous }
}
