package managed

import "time"

// claudeRetryTimer registers a wake for an absolute deadline. Checking the
// clock again after registration closes the gap in which a virtual clock may
// advance past the deadline between calculating the delay and registering the timer.
// Only AfterFuncAt can align registration exactly with a custom clock: relative
// timers may wake late if that clock advances during registration.
func claudeRetryTimer(clock interface{ Now() time.Time }, deadline time.Time) (<-chan struct{}, func()) {
	fired := make(chan struct{}, 1)
	fire := func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	}
	var cancel func()
	if c, ok := clock.(interface {
		AfterFuncAt(time.Time, func()) func()
	}); ok {
		cancel = c.AfterFuncAt(deadline, fire)
	} else if c, ok := clock.(interface {
		AfterFunc(time.Duration, func()) func()
	}); ok {
		cancel = c.AfterFunc(deadline.Sub(clock.Now()), fire)
	} else {
		timer := time.AfterFunc(deadline.Sub(clock.Now()), fire)
		cancel = func() { timer.Stop() }
	}
	if !clock.Now().Before(deadline) {
		cancel()
		fire()
	}
	return fired, cancel
}
