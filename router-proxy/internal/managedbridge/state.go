package managedbridge

import (
	"crypto/subtle"
	"sync"
)

// State is the disabled bridge connection state machine.
type State string

const (
	StateDisconnected  State = "disconnected"
	StateConnect       State = "connect"
	StateChallenged    State = "challenged"
	StateAuthenticated State = "authenticated"
	StateResuming      State = "resuming"
	StateActive        State = "active"
	StateStale         State = "stale"
	StateStopped       State = "stopped"
)

var transitions = map[State]map[State]bool{
	StateDisconnected:  {StateConnect: true, StateStopped: true},
	StateConnect:       {StateChallenged: true, StateStale: true, StateStopped: true},
	StateChallenged:    {StateAuthenticated: true, StateStale: true, StateStopped: true},
	StateAuthenticated: {StateResuming: true, StateStale: true, StateStopped: true},
	StateResuming:      {StateActive: true, StateStale: true, StateStopped: true},
	StateActive:        {StateStale: true, StateStopped: true},
	StateStale:         {StateConnect: true, StateStopped: true},
	StateStopped:       {},
}

func ValidTransition(from, to State) bool { return transitions[from][to] }

// StateMachine serializes lifecycle changes and rejects frames from any old
// router instance, connection, bridge, or Pi runtime fence.
type StateMachine struct {
	mu        sync.RWMutex
	state     State
	challenge Challenge
	identity  PostAuth
	bound     bool
}

func NewStateMachine() *StateMachine { return &StateMachine{state: StateDisconnected} }
func (m *StateMachine) State() State { m.mu.RLock(); defer m.mu.RUnlock(); return m.state }

func (m *StateMachine) Transition(to State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidTransition(m.state, to) {
		return ErrInvalidTransition
	}
	m.state = to
	if to == StateStale || to == StateStopped {
		m.bound = false
	}
	return nil
}

// BeginConnect enters the explicit connect state.
func (m *StateMachine) BeginConnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidTransition(m.state, StateConnect) {
		return ErrInvalidTransition
	}
	m.state = StateConnect
	m.bound = false
	return nil
}

// AcceptChallenge records a fresh challenge and advances connect to
// challenged. Keeping this transition explicit makes reconnect races visible.
func (m *StateMachine) AcceptChallenge(challenge Challenge) error {
	if challenge.Validate() != nil {
		return ErrInvalidFrame
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidTransition(m.state, StateChallenged) {
		return ErrInvalidTransition
	}
	m.state = StateChallenged
	m.challenge = challenge
	m.bound = false
	return nil
}

// Connect performs the two legal reconnect transitions from disconnected or
// stale and records the challenge used by the future transport.
func (m *StateMachine) Connect(challenge Challenge) error {
	if challenge.Validate() != nil {
		return ErrInvalidFrame
	}
	if err := m.BeginConnect(); err != nil {
		return err
	}
	return m.AcceptChallenge(challenge)
}

// Authenticate accepts the bridge only for the currently challenged nonces
// and verifier. It binds the complete runtime fence for later frame checks.
func (m *StateMachine) Authenticate(verifier Verifier, auth Authenticate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateChallenged || VerifyAuthenticate(verifier, m.challenge, auth) != nil {
		return ErrInvalidFrame
	}
	m.identity = PostAuth{Agent: auth.Agent, BridgeInstance: auth.BridgeInstance, RouterInstanceNonce: auth.RouterInstanceNonce, ConnectionNonce: auth.ConnectionNonce, Fence: auth.Fence}
	m.bound = true
	m.state = StateAuthenticated
	return nil
}

func (m *StateMachine) BeginResume(resume Resume) error {
	if resume.Validate() != nil {
		return ErrInvalidFrame
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != StateAuthenticated || !m.sameIdentityLocked(resume.PostAuth) {
		return ErrStaleFrame
	}
	m.state = StateResuming
	return nil
}

func (m *StateMachine) Activate() error { return m.Transition(StateActive) }

func (m *StateMachine) MarkStale() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == StateStopped || !ValidTransition(m.state, StateStale) {
		return ErrInvalidTransition
	}
	m.state = StateStale
	m.bound = false
	return nil
}

// CheckFrame validates a known authenticated frame and its complete fence
// against the current live binding. It does not change lifecycle state.
func (m *StateMachine) CheckFrame(frame any) error {
	var post PostAuth
	switch value := frame.(type) {
	case Resume:
		if value.Validate() != nil {
			return ErrInvalidFrame
		}
		post = value.PostAuth
	case Command:
		if value.Validate() != nil {
			return ErrInvalidFrame
		}
		post = value.PostAuth
	case Event:
		if value.Validate() != nil {
			return ErrInvalidFrame
		}
		post = value.PostAuth
	case Ack:
		if value.Validate() != nil {
			return ErrInvalidFrame
		}
		post = value.PostAuth
	case ReplayGap:
		if value.Validate() != nil {
			return ErrInvalidFrame
		}
		post = value.PostAuth
	default:
		return ErrInvalidFrame
	}
	allowed := StateActive
	if _, isResume := frame.(Resume); isResume {
		allowed = StateAuthenticated
	}
	return m.checkPostAuth(post, allowed)
}

// CheckPostAuth validates a frame's complete fence against the current live
// binding. It does not change lifecycle state.
func (m *StateMachine) CheckPostAuth(frame PostAuth) error {
	if frame.Validate() != nil {
		return ErrInvalidFrame
	}
	return m.checkPostAuth(frame, "")
}

func (m *StateMachine) checkPostAuth(frame PostAuth, allowed State) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.bound || m.state == StateStale || m.state == StateStopped || !m.sameIdentityLocked(frame) {
		return ErrStaleFrame
	}
	if allowed != "" && m.state != allowed {
		return ErrInvalidTransition
	}
	return nil
}

func (m *StateMachine) sameIdentityLocked(value PostAuth) bool {
	return equalSecretString(m.identity.Agent, value.Agent) && equalSecretString(m.identity.BridgeInstance, value.BridgeInstance) && equalSecretString(m.identity.RouterInstanceNonce, value.RouterInstanceNonce) && equalSecretString(m.identity.ConnectionNonce, value.ConnectionNonce) && m.identity.Fence == value.Fence
}

func equalSecretString(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
