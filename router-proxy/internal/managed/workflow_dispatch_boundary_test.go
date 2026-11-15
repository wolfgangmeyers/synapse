package managed

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type nudgeTestDoer func(*http.Request) (*http.Response, error)

func (d nudgeTestDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }

func TestWorkflowDispatchBoundaryPreventsSendOnAdmissionFailure(t *testing.T) {
	sent := false
	denied := errors.New("denied")
	d := workflowDispatchDoer{before: func(context.Context) error { return denied }, inner: nudgeTestDoer(func(*http.Request) (*http.Response, error) { sent = true; return nil, nil })}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://localhost", nil)
	require.NoError(t, err)
	_, err = d.Do(req)
	require.ErrorIs(t, err, denied)
	require.False(t, sent)
}

func TestWorkflowCertainDeliveryFailureRefund(t *testing.T) {
	var lane managedAgentLane
	id := lane.beginWorkflowNudge()
	require.True(t, lane.dispatchWorkflowNudge(id, 20))
	lane.refundWorkflowNudge(id)
	require.Zero(t, lane.workflowUnanswered.Load())
	require.False(t, lane.respondWorkflowNudge(id))
}

func TestWorkflowHTTPPreSendFailureRefundsOnlyCertain(t *testing.T) {
	for _, tc := range []struct {
		name              string
		err               error
		connected, refund bool
	}{
		{"dial", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, false, true},
		{"dial cancellation", &net.OpError{Op: "dial", Err: context.Canceled}, false, true},
		{"dial deadline", &net.OpError{Op: "dial", Err: context.DeadlineExceeded}, false, true},
		{"bare refused ambiguous", syscall.ECONNREFUSED, false, false},
		{"dial error after connection", &net.OpError{Op: "dial", Err: context.Canceled}, true, false},
		{"connection reset after connect", syscall.ECONNRESET, true, false},
		{"ambiguous", errors.New("unknown"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lane managedAgentLane
			id := lane.beginWorkflowNudge()
			require.True(t, lane.dispatchWorkflowNudge(id, 20))
			d := workflowDispatchDoer{refund: func() { lane.refundWorkflowNudge(id) }, inner: nudgeTestDoer(func(req *http.Request) (*http.Response, error) {
				if tc.connected {
					httptrace.ContextClientTrace(req.Context()).GotConn(httptrace.GotConnInfo{})
				}
				return nil, tc.err
			})}
			req, _ := http.NewRequest(http.MethodPost, "http://localhost", nil)
			_, err := d.Do(req)
			require.Error(t, err)
			if tc.refund {
				require.Zero(t, lane.workflowUnanswered.Load())
			} else {
				require.EqualValues(t, 1, lane.workflowUnanswered.Load())
			}
		})
	}
}

type shortNudgeWriter struct{ n int }

func (w shortNudgeWriter) Write([]byte) (int, error) { return w.n, errors.New("write failed") }
func (shortNudgeWriter) Close() error                { return nil }

func TestWorkflowClaudeWriteFailureDistinguishesZeroAndPartial(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		zero bool
	}{{"zero", 0, true}, {"partial", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &ClaudeCodeProcess{stdin: shortNudgeWriter{tc.n}, notify: make(chan struct{}, 1)}
			err := p.writeWithStart(context.Background(), map[string]string{"type": "user"}, func(context.Context) error { return nil })
			var zero claudeZeroWriteError
			require.Equal(t, tc.zero, errors.As(err, &zero))
		})
	}
}

func TestWorkflowHTTPDialFailureAfterEarlierSendDoesNotRefund(t *testing.T) {
	var lane managedAgentLane
	id := lane.beginWorkflowNudge()
	require.True(t, lane.dispatchWorkflowNudge(id, 20))
	o := &workflowNudgeObservation{lane: &lane, generation: id, decided: true, workflow: true}
	o.markDeliveryPossible()
	o.refund()
	require.EqualValues(t, 1, lane.workflowUnanswered.Load())
}

func TestWorkflowHTTPCancelledBeforeAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", nil)
	require.NoError(t, err)
	called := false
	d := workflowDispatchDoer{before: func(context.Context) error { called = true; return nil }, inner: nudgeTestDoer(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}
	_, err = d.Do(req)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called)
}

func TestWorkflowHTTPCancellationBeforeAndAfterSend(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		cancelBefore, connected, wantRefund bool
	}{
		{"cancelled before send", true, false, true},
		{"cancelled during dial", false, false, false},
		{"cancelled after connection", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost", nil)
			require.NoError(t, err)
			refunded, sent := false, false
			d := workflowDispatchDoer{before: func(context.Context) error {
				if tc.cancelBefore {
					cancel()
				}
				return nil
			}, refund: func() { refunded = true }, inner: nudgeTestDoer(func(r *http.Request) (*http.Response, error) {
				sent = true
				if tc.connected {
					httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{})
				}
				cancel()
				return nil, context.Canceled
			})}
			_, err = d.Do(req)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, tc.wantRefund, refunded)
			if tc.cancelBefore {
				require.False(t, sent, "pre-cancelled request must never enter transport")
			} else {
				require.True(t, sent)
			}
		})
	}
}
