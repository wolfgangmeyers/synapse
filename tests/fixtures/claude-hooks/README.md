# Captured Claude Code hook payloads

Real hook stdin from Claude Code 2.1.226, dumped field-for-field. These are the evidence behind
the activity-indicator design, and the tests replay the state machine over them so a rule that
misclassifies real data fails rather than passing on a hand-written case.

Each line is `{"__probe_event__": <hook name>, "__t__": <epoch seconds>, "payload": <raw stdin>}`.
Lines with `__probe_event__: "transcript_sample"` are not hook events; they are the session
transcript's size sampled once a second, written into the same file so a claim about the
transcript and the events that explain it can be checked against one artifact.

| File | What it shows |
|---|---|
| `probe1` | Two subagents of different types. FIFO pairing, confirmed by `tool_response.agentId`. |
| `probe2` | Two subagents of the *same* type launched together, the case where ordering is the only discriminator. |
| `probe3-blocking` | Hook latency across events. Captured with a cut-down dumper, so it backs the timing and not the record shape. |
| `probe5-transcript-inflight` | Contains an `idle_prompt` Notification. Also spans two sessions, so a session-id reset is exercised. |
| `probe6-interrupt` | An interrupt. Three events and then nothing, with no transcript samples. |
| `probe7-compact`, `probe8-postcompact` | Compaction. The unmatched `SubagentStop` with an empty `agent_type` is compaction's own summarizer. `probe8` has `PostCompact`. |
| `probe9-multihook` | Three hooks on one event running concurrently, not serially. |
| `probe10-transcript-evidence` | Transcript flat for a whole tool call, growing only alongside a hook event. |
| `probe11-interrupt-evidence` | Transcript growing on interrupt with no hook event, then ~105s of silence. |
| `probe12-blocked-*` | Blocked on `ExitPlanMode` and on `AskUserQuestion`. A `permission_prompt` Notification, and a transcript that does not move while blocked. |
