package managedbridge

// String methods deliberately return type-only diagnostics. Proofs, nonces,
// fences, IDs, and opaque payloads must not be copied into logs by accident.
func (Challenge) String() string     { return "managed RPC challenge" }
func (Authenticate) String() string  { return "managed RPC authenticate" }
func (Authenticated) String() string { return "managed RPC authenticated" }
func (Resume) String() string        { return "managed RPC resume" }
func (Command) String() string       { return "managed RPC command" }
func (Event) String() string         { return "managed RPC event" }
func (Ack) String() string           { return "managed RPC ack" }
func (ReplayGap) String() string     { return "managed RPC replay gap" }

func (Challenge) GoString() string     { return "managed RPC challenge" }
func (Authenticate) GoString() string  { return "managed RPC authenticate" }
func (Authenticated) GoString() string { return "managed RPC authenticated" }
func (Resume) GoString() string        { return "managed RPC resume" }
func (Command) GoString() string       { return "managed RPC command" }
func (Event) GoString() string         { return "managed RPC event" }
func (Ack) GoString() string           { return "managed RPC ack" }
func (ReplayGap) GoString() string     { return "managed RPC replay gap" }
