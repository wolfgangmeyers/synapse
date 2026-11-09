package main

import (
	"path/filepath"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

func claudeDiagnosticLogPath(routerLog string) string {
	return filepath.Join(filepath.Dir(routerLog), "claude-protocol-"+filepath.Base(routerLog))
}

// Called only by the single diagnostic consumer. Opening and every filesystem
// operation occur off the delivery path; failures are best effort and silent.
func claudeDiagnosticSink(path string) func(managed.ClaudeProtocolDiagnostic) {
	return claudeDiagnosticSinkWithFactory(func() (*logWriter, error) { return newLogWriter(path) })
}

func claudeDiagnosticSinkWithFactory(open func() (*logWriter, error)) func(managed.ClaudeProtocolDiagnostic) {
	var writer *logWriter
	return func(d managed.ClaudeProtocolDiagnostic) {
		if writer == nil {
			var err error
			writer, err = open()
			if err != nil {
				return
			}
		}
		_ = writer.Log("claude_protocol_diagnostic", map[string]any{"reason": d.Reason, "pending_count": d.Pending, "recent": d.Recent})
	}
}
