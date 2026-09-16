package managedbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	uuid1 = "01234567-89ab-4cde-8fab-0123456789ab"
	uuid2 = "11234567-89ab-4cde-8fab-0123456789ab"
	uuid3 = "21234567-89ab-4cde-8fab-0123456789ab"
)

func validFence() RuntimeFence {
	return RuntimeFence{Agent: "worker.one", BridgeInstance: uuid1, PiSessionID: uuid2, PiSessionEpoch: 1, RuntimeGeneration: 2, PiPID: 42, PiStartMarker: "start-42", PiIncarnation: uuid3, ManifestRevision: 3, ManifestHash: strings.Repeat("a", 64)}
}
func validPostAuth(challenge Challenge) PostAuth {
	fence := validFence()
	return PostAuth{Agent: fence.Agent, BridgeInstance: fence.BridgeInstance, RouterInstanceNonce: challenge.RouterInstanceNonce, ConnectionNonce: challenge.ConnectionNonce, Fence: fence}
}
func mustVerifier(t *testing.T, fill byte) Verifier {
	t.Helper()
	verifier, err := NewVerifier([]byte(strings.Repeat(string(fill), 32)))
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}
func mustChallenge(t *testing.T) Challenge {
	t.Helper()
	challenge := Challenge{Protocol: ProtocolVersion, Type: string(FrameChallenge), RouterInstanceNonce: strings.Repeat("1", 64), ConnectionNonce: strings.Repeat("2", 64)}
	if err := challenge.Validate(); err != nil {
		t.Fatal(err)
	}
	return challenge
}
func mustAuth(t *testing.T, verifier Verifier, challenge Challenge) Authenticate {
	t.Helper()
	auth, err := AuthenticateFor(verifier, challenge, validFence())
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestNewChallengeCreatesFreshValidNonces(t *testing.T) {
	first, err := NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if first.Validate() != nil || second.Validate() != nil || first == second {
		t.Fatal("challenge nonces were not fresh")
	}
}

func TestProtocolStrictDecodeAndBounds(t *testing.T) {
	challenge := mustChallenge(t)
	data, err := EncodeFrame(challenge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeFrame(data); err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(data), "}") + `,"extra":1}`
	if _, err := DecodeFrame([]byte(unknown)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := DecodeFrame(append(data, []byte("{}")...)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("trailing JSON error = %v", err)
	}
	wrong := strings.Replace(string(data), ProtocolVersion, "other.v1", 1)
	if _, err := DecodeFrame([]byte(wrong)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("wrong protocol error = %v", err)
	}
	if _, err := DecodeFrame(make([]byte, MaxFrameBytes+1)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestDecodeUsesExactFrameKeysAndRejectsDeepJSON(t *testing.T) {
	challenge := mustChallenge(t)
	data, err := EncodeFrame(challenge)
	if err != nil {
		t.Fatal(err)
	}
	caseVariant := strings.Replace(string(data), `"protocol"`, `"Protocol"`, 1)
	if _, err := DecodeFrame([]byte(caseVariant)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("case-variant frame error = %v", err)
	}
	deep := strings.Repeat("[", MaxJSONDepth+1) + "true" + strings.Repeat("]", MaxJSONDepth+1)
	if _, err := HashPayload([]byte(deep)); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("deep payload error = %v", err)
	}
}

func TestProtocolRejectsNestedUnknownTypeDuplicateAndPayloadBounds(t *testing.T) {
	challenge := mustChallenge(t)
	data, err := EncodeFrame(challenge)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`{"protocol":"` + ProtocolVersion + `","type":1,"router_instance_nonce":"` + challenge.RouterInstanceNonce + `","connection_nonce":"` + challenge.ConnectionNonce + `"}`,
		`{"protocol":"` + ProtocolVersion + `","type":"challenge","router_instance_nonce":1,"connection_nonce":"` + challenge.ConnectionNonce + `"}`,
		`{"protocol":"` + ProtocolVersion + `","type":"challenge","router_instance_nonce":"` + challenge.RouterInstanceNonce + `","connection_nonce":"` + challenge.ConnectionNonce + `","router_instance_nonce":"` + challenge.RouterInstanceNonce + `"}`,
		string(data[:len(data)-1]) + `,"nested":{"x":1}}`,
	}
	for _, input := range cases {
		if _, err := DecodeFrame([]byte(input)); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("accepted malformed frame %q: %v", input, err)
		}
	}
	payload := strings.Repeat(" ", MaxPayloadBytes-2) + "{}"
	if _, err := HashPayload([]byte(payload)); err != nil {
		t.Fatalf("bounded whitespace payload rejected: %v", err)
	}
	if _, err := HashPayload([]byte("null")); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("null payload error = %v", err)
	}
}

func TestProtocolPostAuthAndClosedVocabularies(t *testing.T) {
	challenge := mustChallenge(t)
	post := validPostAuth(challenge)
	payload := json.RawMessage(`{"message":"hello"}`)
	payloadHash, err := HashPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{PostAuth: post, Protocol: ProtocolVersion, Type: string(FrameCommand), Seq: 1, ID: uuid3, Name: CommandPrompt, Payload: payload, Hash: payloadHash}
	data, err := EncodeFrame(command)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrame(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.(Command); !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
	command.Name = "future-command"
	if _, err := EncodeFrame(command); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("closed command error = %v", err)
	}
	command.Name = CommandPrompt
	command.Payload = json.RawMessage(strings.Repeat("x", MaxPayloadBytes+1))
	if _, err := EncodeFrame(command); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("oversize payload error = %v", err)
	}
	if post.Validate() != nil {
		t.Fatal("valid post-auth rejected")
	}
	post.Fence.RuntimeGeneration++
	if err := post.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifierRejectsPlaceholderAndZeroVerifier(t *testing.T) {
	if _, err := NewVerifier(make([]byte, 32)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("zero token error = %v", err)
	}
	challenge := mustChallenge(t)
	if _, err := AuthenticateFor(Verifier{}, challenge, validFence()); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("zero verifier error = %v", err)
	}
}

func TestMutualProofWrongTokenNonceAndFenceTampering(t *testing.T) {
	verifier := mustVerifier(t, 'x')
	wrong := mustVerifier(t, 'y')
	challenge := mustChallenge(t)
	auth := mustAuth(t, verifier, challenge)
	if err := VerifyAuthenticate(verifier, challenge, auth); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuthenticate(wrong, challenge, auth); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("wrong token error = %v", err)
	}
	tampered := auth
	tampered.ConnectionNonce = challenge.RouterInstanceNonce
	if err := VerifyAuthenticate(verifier, challenge, tampered); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("nonce tamper error = %v", err)
	}
	tampered = auth
	tampered.Fence.RuntimeGeneration++
	if err := VerifyAuthenticate(verifier, challenge, tampered); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("fence tamper error = %v", err)
	}
	accepted, err := AuthenticatedFor(verifier, challenge, auth, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuthenticated(verifier, challenge, auth, accepted); err != nil {
		t.Fatal(err)
	}
	accepted.Proof = strings.Repeat("b", 64)
	if err := VerifyAuthenticated(verifier, challenge, auth, accepted); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("acceptance tamper error = %v", err)
	}
	accepted, err = AuthenticatedFor(verifier, challenge, auth, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	accepted.CommandCursor++
	if err := VerifyAuthenticated(verifier, challenge, auth, accepted); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("command cursor tamper error = %v", err)
	}
	for name, mutate := range map[string]func(*RuntimeFence){
		"agent":       func(f *RuntimeFence) { f.Agent = "worker.two" },
		"bridge":      func(f *RuntimeFence) { f.BridgeInstance = uuid2 },
		"session":     func(f *RuntimeFence) { f.PiSessionID = uuid1 },
		"epoch":       func(f *RuntimeFence) { f.PiSessionEpoch++ },
		"generation":  func(f *RuntimeFence) { f.RuntimeGeneration++ },
		"pid":         func(f *RuntimeFence) { f.PiPID++ },
		"start":       func(f *RuntimeFence) { f.PiStartMarker = "start-43" },
		"incarnation": func(f *RuntimeFence) { f.PiIncarnation = uuid1 },
		"revision":    func(f *RuntimeFence) { f.ManifestRevision++ },
		"hash":        func(f *RuntimeFence) { f.ManifestHash = strings.Repeat("b", 64) },
	} {
		tampered := auth
		mutate(&tampered.Fence)
		if err := VerifyAuthenticate(verifier, challenge, tampered); !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("%s fence mutation accepted: %v", name, err)
		}
	}
}

func TestVerifierCreationDoesNotSerializeSecret(t *testing.T) {
	if _, err := NewVerifier(make([]byte, 31)); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("short token error = %v", err)
	}
	verifier := mustVerifier(t, 'z')
	data, err := json.Marshal(verifier)
	if !errors.Is(err, ErrSecretSerialization) || data != nil {
		t.Fatalf("verifier marshal = %q, %v", data, err)
	}
	if strings.Contains(verifier.String(), "z") || strings.Contains(fmt.Sprintf("%+v", verifier), "z") || strings.Contains(fmt.Sprintf("%#v", verifier), "z") {
		t.Fatal("verifier diagnostic exposed secret")
	}
	challenge := mustChallenge(t)
	auth := mustAuth(t, verifier, challenge)
	if strings.Contains(fmt.Sprintf("%+v", auth), auth.Proof) {
		t.Fatal("authenticate diagnostic exposed proof")
	}
	if strings.Contains(fmt.Sprintf("%#v", auth), auth.Proof) {
		t.Fatal("authenticate Go diagnostic exposed proof")
	}
	if strings.Contains(ErrInvalidFrame.Error(), "z") {
		t.Fatal("diagnostic contains token")
	}
}

func TestStateMachineTransitionsAndStaleFenceRejection(t *testing.T) {
	verifier := mustVerifier(t, 's')
	challenge := mustChallenge(t)
	machine := NewStateMachine()
	if machine.State() != StateDisconnected {
		t.Fatal(machine.State())
	}
	invalidChallenge := challenge
	invalidChallenge.ConnectionNonce = "short"
	if err := machine.Connect(invalidChallenge); !errors.Is(err, ErrInvalidFrame) || machine.State() != StateDisconnected {
		t.Fatalf("invalid connect = %v, state=%s", err, machine.State())
	}
	if err := machine.Connect(challenge); err != nil {
		t.Fatal(err)
	}
	if machine.State() != StateChallenged {
		t.Fatal(machine.State())
	}
	auth := mustAuth(t, verifier, challenge)
	if err := machine.Authenticate(verifier, auth); err != nil {
		t.Fatal(err)
	}
	if err := machine.CheckFrame(Ack{PostAuth: validPostAuth(challenge), Protocol: ProtocolVersion, Type: string(FrameAck), Domain: DomainCommands, Seq: 1}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pre-active frame error = %v", err)
	}
	resume := Resume{PostAuth: validPostAuth(challenge), Protocol: ProtocolVersion, Type: string(FrameResume), CommandCursor: 0, EventCursor: 0}
	if err := machine.BeginResume(resume); err != nil {
		t.Fatal(err)
	}
	if err := machine.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := machine.CheckPostAuth(validPostAuth(challenge)); err != nil {
		t.Fatal(err)
	}
	stale := validPostAuth(challenge)
	for name, mutate := range map[string]func(*PostAuth){
		"agent":            func(p *PostAuth) { p.Agent = "worker.two"; p.Fence.Agent = p.Agent },
		"bridge":           func(p *PostAuth) { p.BridgeInstance = uuid2; p.Fence.BridgeInstance = p.BridgeInstance },
		"router nonce":     func(p *PostAuth) { p.RouterInstanceNonce = strings.Repeat("3", 64) },
		"connection nonce": func(p *PostAuth) { p.ConnectionNonce = strings.Repeat("4", 64) },
		"fence":            func(p *PostAuth) { p.Fence.RuntimeGeneration++ },
	} {
		stale = validPostAuth(challenge)
		mutate(&stale)
		if err := machine.CheckPostAuth(stale); !errors.Is(err, ErrStaleFrame) {
			t.Fatalf("stale %s error = %v", name, err)
		}
	}
	if err := machine.MarkStale(); err != nil {
		t.Fatal(err)
	}
	if err := machine.CheckPostAuth(validPostAuth(challenge)); !errors.Is(err, ErrStaleFrame) {
		t.Fatalf("stale state error = %v", err)
	}
	if err := machine.Transition(StateActive); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("illegal transition error = %v", err)
	}
	if err := machine.Transition(StateStopped); err != nil {
		t.Fatal(err)
	}
	if err := machine.Connect(mustChallenge(t)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("stopped reconnect error = %v", err)
	}
}

func eventID(seq uint64) string {
	return []string{uuid1, uuid2, uuid3}[seq%3]
}
func cursorHash(seq uint64) string { return strings.Repeat(string('a'+byte(seq%6)), 64) }

func TestCursorDurableAdmissionDuplicateConflictGapAndReplay(t *testing.T) {
	engine, err := NewCursorEngine(3)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fail := true
	result, err := engine.Admit(CursorCommands, 1, uuid1, cursorHash(1), func() error {
		calls.Add(1)
		if fail {
			return errors.New("private durable error")
		}
		return nil
	})
	if !errors.Is(err, ErrDurableAdmission) || result.AckSeq != 0 {
		t.Fatalf("failed admission = %+v, %v", result, err)
	}
	next, _ := engine.Next(CursorCommands)
	if next != 1 || calls.Load() != 1 {
		t.Fatalf("failed admission mutated cursor: next=%d calls=%d", next, calls.Load())
	}
	fail = false
	result, err = engine.Admit(CursorCommands, 1, uuid1, cursorHash(1), func() error { calls.Add(1); return nil })
	if err != nil || !result.Accepted || result.AckSeq != 1 {
		t.Fatalf("admission = %+v, %v", result, err)
	}
	result, err = engine.Admit(CursorCommands, 1, uuid1, cursorHash(1), func() error { calls.Add(1); return nil })
	if err != nil || !result.Duplicate || calls.Load() != 2 {
		t.Fatalf("duplicate = %+v, %v calls=%d", result, err, calls.Load())
	}
	if _, err := engine.Admit(CursorCommands, 3, uuid3, cursorHash(3), func() error { return nil }); !errors.Is(err, ErrCursorGap) {
		t.Fatalf("gap = %v", err)
	}
	if _, err := engine.Admit(CursorCommands, 1, uuid2, cursorHash(1), func() error { return nil }); !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("conflict = %v", err)
	}
	for seq := uint64(2); seq <= 4; seq++ {
		if _, err := engine.Admit(CursorCommands, seq, eventID(seq), cursorHash(seq), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	floor, _ := engine.ReplayFloor(CursorCommands)
	if floor != 2 {
		t.Fatalf("floor = %d", floor)
	}
	if _, err := engine.Admit(CursorCommands, 1, uuid1, cursorHash(1), func() error { return nil }); !errors.Is(err, ErrReplayGap) {
		t.Fatalf("replay gap = %v", err)
	}
}

func TestCursorDomainsAreIndependentAndBoundedUnderConcurrency(t *testing.T) {
	engine, err := NewCursorEngine(128)
	if err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	var wait sync.WaitGroup
	for _, domain := range []CursorDomain{CursorCommands, CursorEvents} {
		domain := domain
		wait.Add(2)
		go func() {
			defer wait.Done()
			for seq := uint64(1); seq <= 200; seq++ {
				_, admissionErr := engine.Admit(domain, seq, eventID(seq), cursorHash(seq), func() error { admitted.Add(1); return nil })
				if admissionErr != nil {
					t.Errorf("domain %s seq %d id %s hash %s: %v", domain, seq, eventID(seq), cursorHash(seq), admissionErr)
					return
				}
			}
		}()
		go func() {
			defer wait.Done()
			for i := 0; i < 2000; i++ {
				_, _ = engine.Next(domain)
				_, _ = engine.ReplayFloor(domain)
			}
		}()
	}
	wait.Wait()
	if admitted.Load() != 400 {
		t.Fatalf("admitted = %d", admitted.Load())
	}
	for _, domain := range []CursorDomain{CursorCommands, CursorEvents} {
		next, _ := engine.Next(domain)
		floor, _ := engine.ReplayFloor(domain)
		if next != 201 || floor != 73 {
			t.Fatalf("domain %s next=%d floor=%d", domain, next, floor)
		}
	}
}

func TestCursorPanicAndSequenceOverflowDoNotAdvance(t *testing.T) {
	engine, err := NewCursorEngine(2)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Admit(CursorCommands, 1, uuid1, cursorHash(1), func() error { panic("private callback marker") })
	if !errors.Is(err, ErrDurableAdmission) || result.AckSeq != 0 {
		t.Fatalf("panic admission = %+v, %v", result, err)
	}
	if next, _ := engine.Next(CursorCommands); next != 1 {
		t.Fatalf("panic advanced cursor to %d", next)
	}
	var calls atomic.Int32
	engine.commands.mu.Lock()
	engine.commands.next = ^uint64(0)
	engine.commands.mu.Unlock()
	_, err = engine.Admit(CursorCommands, ^uint64(0), uuid1, cursorHash(1), func() error { calls.Add(1); return nil })
	if !errors.Is(err, ErrCursorInvalid) || calls.Load() != 0 {
		t.Fatalf("overflow admission = %v, calls=%d", err, calls.Load())
	}
	if next, _ := engine.Next(CursorCommands); next != ^uint64(0) {
		t.Fatalf("overflow changed cursor to %d", next)
	}
}

func TestCursorExplicitPruneAndPayloadHashBound(t *testing.T) {
	engine := NewDefaultCursorEngine()
	for seq := uint64(1); seq <= 3; seq++ {
		if _, err := engine.Admit(CursorEvents, seq, eventID(seq), cursorHash(seq), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := engine.PruneThrough(CursorEvents, 2); err != nil {
		t.Fatal(err)
	}
	floor, _ := engine.ReplayFloor(CursorEvents)
	if floor != 3 {
		t.Fatalf("floor = %d", floor)
	}
	payloadHash, err := HashPayload([]byte(`{"ok":true}`))
	if err != nil || len(payloadHash) != 64 {
		t.Fatalf("payload hash = %q, %v", payloadHash, err)
	}
	if _, err := HashPayload([]byte(strings.Repeat("x", MaxPayloadBytes+1))); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("payload bound = %v", err)
	}
	payload := []byte(` { "ok": true } `)
	hash, err := HashPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	post := validPostAuth(mustChallenge(t))
	command := Command{PostAuth: post, Protocol: ProtocolVersion, Type: string(FrameCommand), Seq: 1, ID: uuid1, Name: CommandPrompt, Payload: json.RawMessage(payload), Hash: hash}
	encoded, err := EncodeFrame(command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeFrame(encoded); err != nil {
		t.Fatalf("whitespace payload frame rejected after encoding: %v", err)
	}
}

func FuzzDecodeFrameNeverPanics(f *testing.F) {
	challenge := Challenge{Protocol: ProtocolVersion, Type: string(FrameChallenge), RouterInstanceNonce: strings.Repeat("1", 64), ConnectionNonce: strings.Repeat("2", 64)}
	encoded, err := EncodeFrame(challenge)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte(`{"protocol":"mecha.managed-rpc.v1","type":"future"}`))
	f.Add([]byte("{"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeFrame(data)
	})
}
