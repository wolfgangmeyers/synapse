package managed

// claudeShortCommandConfig shares the managed process recovery fence.
func (m *Manager) claudeShortCommandConfig(cwd string) ClaudeCodeProcessConfig {
	return ClaudeCodeProcessConfig{Cwd: cwd, OrphanRoot: m.config.SessionRoot, OnUnconfirmedStop: m.claudeCodeProcesses.fenceStartup, admitExternal: m.claudeCodeProcesses.admitExternal, releaseGate: m.claudeCodeProcesses.releaseGate}
}
