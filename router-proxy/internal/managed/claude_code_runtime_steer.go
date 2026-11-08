package managed

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

// Mid-turn steering for Claude. A steered input is reserved by its caller (a
// mail attempt or a prompt request), registered here before its stdin write, and
// consumed only by its exact checked echo in readTurn. A reservation or a write
// is never consumption.

type claudeSteerOutcome int

const (
	// claudeSteerRefused: nothing reached stdin. The caller releases its reservation.
	claudeSteerRefused claudeSteerOutcome = iota
	// claudeSteerAccepted: written to stdin, awaiting its checked echo.
	claudeSteerAccepted
	// claudeSteerUncertain: a write started and failed. The caller must not release.
	claudeSteerUncertain
)

// claudeSteerLimit bounds in-flight steered inputs per process.
const claudeSteerLimit = 16

type claudeSteer struct {
	id        string
	attempt   *store.EmbeddedMailAttempt
	requestID string
	message   ai.UserMessage
	raw       json.RawMessage
	started   bool
	echoed    bool
}

// steerInput writes one reserved input into the active turn. Exactly one of
// attempt (mail) or requestID (direct prompt) must identify the reservation.
func (r *claudeCodeRuntime) steerInput(ctx context.Context, message ai.UserMessage, attempt *store.EmbeddedMailAttempt, requestID string) (claudeSteerOutcome, error) {
	if (attempt == nil) == (requestID == "") {
		return claudeSteerRefused, errors.New("Claude steer requires exactly one reservation")
	}
	if requestID != "" {
		// A direct prompt's write must finish even if its HTTP client disconnects.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), steerWriteTimeout)
		defer cancel()
	}
	raw, err := claudeCodeRender(message.Content)
	if err != nil {
		return claudeSteerRefused, err
	}
	if err := r.beforeSubmit(ctx, len(raw)); err != nil {
		return claudeSteerRefused, err
	}
	id := claudeCodeNewInputID()
	st := &claudeSteer{id: id, attempt: attempt, requestID: requestID, message: message, raw: raw}

	r.mu.Lock()
	if !r.active || !r.steerOpen || r.interrupted || r.process == nil || len(r.steers) >= claudeSteerLimit {
		r.mu.Unlock()
		return claudeSteerRefused, nil
	}
	if !claudeHeadroomFits(r.snapshot.ContextWindow, r.snapshot.ContextTokens, r.admissionBytes+len(raw)) {
		r.mu.Unlock()
		return claudeSteerRefused, errClaudeInputBounds
	}
	if r.steers == nil {
		r.steers = make(map[string]*claudeSteer)
	}
	r.steers[id] = st
	r.admissionBytes += len(raw)
	proc := r.process
	r.mu.Unlock()

	write := func() error {
		r.mu.Lock()
		if r.interrupted || !r.steerOpen {
			r.mu.Unlock()
			return context.Canceled
		}
		writeCtx, cancel := context.WithCancel(ctx)
		if r.inputCancels == nil {
			r.inputCancels = make(map[string]context.CancelFunc)
		}
		r.inputCancels[id] = cancel
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.inputCancels, id)
			r.mu.Unlock()
			cancel()
		}()
		return proc.sendUserWithStart(writeCtx, id, raw, func(c context.Context) error {
			// Fence the result and later runs across the durable write-start marks.
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.steerOpen || r.interrupted || r.steers[id] != st {
				return context.Canceled
			}
			if err := r.p.db.MarkClaudeBindingStarted(c, r.p.binding); err != nil {
				return err
			}
			if attempt != nil {
				if err := r.p.db.MarkClaudeMailWriteStarted(c, r.p.binding, attempt.AttemptID); err != nil {
					return err
				}
			}
			st.started = true
			return nil
		})
	}
	if r.submit != nil {
		err = r.submit(ctx, len(raw), write)
	} else {
		err = write()
	}

	r.mu.Lock()
	started := st.started
	if err != nil && !started {
		delete(r.steers, id)
		r.admissionBytes -= len(raw)
	}
	r.mu.Unlock()
	switch {
	case err == nil:
		return claudeSteerAccepted, nil
	case started:
		// Outcome unknown: end the process closed. Turn end holds this input.
		proc.fail(err)
		return claudeSteerUncertain, err
	default:
		return claudeSteerRefused, err
	}
}

// takeEchoedSteer returns the registered steer for an echo, exactly once.
func (r *claudeCodeRuntime) takeEchoedSteer(id string) *claudeSteer {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.steers[id]
	if st == nil || !st.started || st.echoed {
		return nil
	}
	st.echoed = true
	return st
}

// commitSteerEcho durably records a checked steered echo. Mail is consumed and
// ACKed in one store transaction; a direct prompt is a checked user entry.
func (r *claudeCodeRuntime) commitSteerEcho(ctx context.Context, st *claudeSteer) error {
	if st.attempt != nil {
		return r.p.appendClaudeSteerMail(ctx, st.attempt, st.message)
	}
	return r.appendChecked(ctx, st.message)
}

// settleSteersAtResult closes steering for the current process at a result.
// Echoed inputs are answered by that result. A write that started without an
// echo is uncertain and is counted, not released.
func (r *claudeCodeRuntime) settleSteersAtResult() (answered []*claudeSteer, uncertain int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steerOpen = false
	for id, st := range r.steers {
		if st.echoed {
			answered = append(answered, st)
			delete(r.steers, id)
		} else if st.started {
			uncertain++
		}
	}
	return answered, uncertain
}

// completeSteeredPrompts finalizes direct prompts whose steered input was answered.
func (r *claudeCodeRuntime) completeSteeredPrompts(ctx context.Context, answered []*claudeSteer) error {
	for _, st := range answered {
		if st.requestID == "" {
			continue
		}
		if err := r.p.db.CompleteEmbeddedPrompt(ctx, r.p.binding, st.requestID); err != nil {
			return err
		}
	}
	return nil
}

// canSteer is a read-only pre-check for steerInput. It does not reserve a slot,
// so callers must still handle a refusal from steerInput.
func (r *claudeCodeRuntime) canSteer() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active && r.steerOpen && !r.interrupted && r.process != nil && len(r.steers) < claudeSteerLimit
}

// steerWriteTimeout bounds a direct-prompt steer write. It detaches from the
// HTTP request so a client disconnect cannot cancel a started write.
const steerWriteTimeout = 5 * time.Second
