package managed

import (
	"strings"
	"sync"
	"sync/atomic"
)

// ClaudeProtocolDiagnostic contains only closed-vocabulary metadata.
type ClaudeProtocolDiagnostic struct {
	Reason  string
	Pending int
	Recent  string
}

type claudeDiagnosticQueue struct{ records chan ClaudeProtocolDiagnostic }

func newClaudeDiagnosticQueue(sink func(ClaudeProtocolDiagnostic)) *claudeDiagnosticQueue {
	q := &claudeDiagnosticQueue{records: make(chan ClaudeProtocolDiagnostic, 64)}
	go func() {
		for record := range q.records {
			sink(record)
		}
	}()
	return q
}

// The router configures this once before starting managed workers. There is one
// consumer for the entire router, not one goroutine per event or process.
var claudeDiagnostics atomic.Pointer[claudeDiagnosticQueue]
var claudeDiagnosticsOnce sync.Once

func ConfigureClaudeProtocolDiagnostics(sink func(ClaudeProtocolDiagnostic)) {
	if sink != nil {
		claudeDiagnosticsOnce.Do(func() { claudeDiagnostics.Store(newClaudeDiagnosticQueue(sink)) })
	}
}

// Called under p.mu. Never retain or log arbitrary wire strings or identities.
func (p *ClaudeCodeProcess) recordProtocolEvent(event ClaudeCodeEvent) {
	category := "other"
	switch event.Type {
	case "system", "assistant", "result":
		category = event.Type
	case "user":
		category = "user"
		if event.IsReplay && event.ParentToolUseID == nil {
			category = "user_replay"
		}
	}
	if len(p.protocolRecent) == 8 {
		copy(p.protocolRecent, p.protocolRecent[1:])
		p.protocolRecent = p.protocolRecent[:7]
	}
	p.protocolRecent = append(p.protocolRecent, category)
}

func (p *ClaudeCodeProcess) logProtocolDiagnostic(flag uint8, reason string) {
	if p.protocolReported&flag != 0 {
		return
	}
	p.protocolReported |= flag
	q := p.protocolDiagnostics
	if q == nil {
		q = claudeDiagnostics.Load()
	}
	if q == nil {
		return
	}
	record := ClaudeProtocolDiagnostic{Reason: reason, Pending: len(p.inputs), Recent: strings.Join(p.protocolRecent, ",")}
	select {
	case q.records <- record:
	default:
	}
}
