# I4/M2 persistence gate: blocked

Pinned `github.com/sky-valley/pi` revision: `e6b56e7223cfab707b21a0a19e0a9a2417ac960a` (`/home/wolfgang/code/pi-go`, clean at probe). **I4: fail. M2: fail.** This probes the public recorder seam, not router integration or crash durability. The disposable host was removed after the probe.

## Reproduce

Create a temporary directory (`mktemp -d /tmp/pi-i4m2-gate.XXXXXXXX`), put these two files there, and run the command below from that directory. The host uses a faux provider stream: no network or API keys. Its data directory is also temporary.

`go.mod`:

```go
module gate

go 1.26

require github.com/sky-valley/pi v0.0.0
replace github.com/sky-valley/pi => /home/wolfgang/code/pi-go
```

`main.go` (the final, corrected harness from the probe):

```go
package main

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/sky-valley/pi/ai"
    "github.com/sky-valley/pi/coding"
)

func must(err error) { if err != nil { panic(err) } }

func main() {
    root, err := os.MkdirTemp("", "pi-i4m2-data-"); must(err); defer os.RemoveAll(root)
    must(os.Setenv("HOME", filepath.Join(root, "home")))
    cwd := filepath.Join(root, "cwd"); must(os.MkdirAll(cwd, 0755))
    model := &ai.Model{ID: "faux", Provider: "faux", Api: "faux"}
    marker := "I4M2-MARKER-exact-6b81"
    fake := func(ctx context.Context, m *ai.Model, req ai.TranscriptContext, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
        stream := ai.NewAssistantMessageEventStream()
        go func() {
            msg := &ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "FAUX-ANSWER"}}, Api: m.Api, Provider: m.Provider, Model: m.ID, StopReason: ai.StopStop, Timestamp: 2}
            stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: msg})
            stream.End()
        }()
        return stream
    }
    for _, failed := range []bool{false, true} {
        label := "success"; if failed { label = "failed-create" }
        sess := coding.NewSession(coding.SessionOptions{Cwd: cwd, Model: model, NoTools: coding.NoToolsAll, StreamFn: fake})
        rec, err := coding.StartSession(cwd, model); must(err); sess.Record(rec)
        id := rec.RecordMessage(ai.NewUserText(marker, 1))
        _, statErr := os.Stat(rec.Path())
        fmt.Printf("%s pre-assistant: id_nonempty=%t file_absent=%t history_len=%d\n", label, id != "", os.IsNotExist(statErr), len(sess.History()))
        if id == "" || !os.IsNotExist(statErr) || len(sess.History()) != 0 { panic("pre-assistant invariant") }
        if failed { must(os.Mkdir(rec.Path(), 0755)) } // Occupy intended file path; O_EXCL creation fails.
        res, runErr := sess.Run(context.Background(), "faux prompt")
        closeErr := rec.Close()
        fmt.Printf("%s run: err=%v text=%q close_err=%v history_len=%d\n", label, runErr, res.Text, closeErr, len(sess.History()))
        if runErr != nil || closeErr != nil || res.Text != "FAUX-ANSWER" { panic("faux run invariant") }
        if failed {
            _, readErr := coding.LoadSessionMessages(rec.Path())
            fmt.Printf("%s reopen: load_err_nonempty=%t\n", label, readErr != nil)
            if readErr == nil { panic("expected failure reading directory") }
        } else {
            path, found := coding.FindSessionByID(cwd, rec.ID(), "")
            messages, err := coding.LoadSessionMessages(path); must(err)
            foundMarker := false; count := 0
            for _, m := range messages {
                if u, ok := m.(ai.UserMessage); ok { for _, c := range u.Content {
                    if text, ok := c.(ai.TextContent); ok && text.Text == marker { foundMarker = true; count++ }
                } }
            }
            fmt.Printf("%s reopen: found=%t same_path=%t marker_exact=%t marker_count=%d messages=%d\n", label, found, path == rec.Path(), foundMarker, count, len(messages))
            if !found || path != rec.Path() || !foundMarker || count != 1 { panic("reopen invariant") }
        }
    }
}
```

```sh
cd /tmp/pi-i4m2-gate.XXXXXXXX # use the directory actually returned by mktemp
GOROOT=/home/wolfgang/.local/share/mise/installs/go/1.26.0 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /home/wolfgang/.local/share/mise/installs/go/1.26.0/bin/go run -mod=mod .
```

`GOROOT` matters here: inherited `GOROOT` pointed at Go 1.25.1 and the first attempt failed with `compile: version "go1.25.1" does not match go tool version "go1.26.0"`. The corrected command produced:

```text
success pre-assistant: id_nonempty=true file_absent=true history_len=0
success run: err=<nil> text="FAUX-ANSWER" close_err=<nil> history_len=3
success reopen: found=true same_path=true marker_exact=true marker_count=1 messages=4
failed-create pre-assistant: id_nonempty=true file_absent=true history_len=0
failed-create run: err=<nil> text="FAUX-ANSWER" close_err=<nil> history_len=3
failed-create reopen: load_err_nonempty=true
```

The initial `RecordMessage` returned an ID before any file existed, and did not add the marker to `Session.History()`. After assistant output, the successful case reopened via public APIs with exactly one user text marker. In the failed-create case, `Run` and `Close` returned nil despite an unreadable session destination. This does not test power loss, SQLite acknowledgment, or whether a post-assistant append survives a crash.

## Why the public seam fails

At this revision, [`coding/session_store.go`](../../pi-go/coding/session_store.go) `StartSession` buffers the header, `persist` withholds disk creation until an assistant message and silently returns on create failure, `writeLine` discards marshal/write errors, and `RecordMessage` returns only a branch entry ID. There is no explicit `Sync` in that recorder path; `Close` closes only an opened file. [`coding/session.go`](../../pi-go/coding/session.go) `Record` subscribes to message-end events without reporting recorder errors; `Run` reports the model turn, not a durable receipt. An initial instruction or direct mail cannot be acknowledged on this evidence. Stop before router integration or a candidate patch.

Next prerequisite: a public, checked append-and-flush/sync (or equivalent durable transaction) that returns verifiable evidence for the intended session and exact message/attempt, including failure propagation. Then rerun the persistence seam and interrupted-append reconciliation gate in the [requirements](embedded-pi-go-requirements.md#acceptance-gates). Probe evidence: [original transcript](/home/wolfgang/.pi/agent/sessions/--home-wolfgang--/2026-09-26T21-59-48-044Z_01a0dfbb-484b-7090-96be-0d958df1f2ee/tasks/2026-09-27T00-58-06-515Z_01a0e05e-8733-7090-96be-0dbc95674c8c.jsonl).
