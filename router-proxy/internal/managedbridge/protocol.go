// Package managedbridge defines the disabled Phase 0 managed RPC contract.
//
// It contains protocol validation, authentication primitives, session fencing,
// and bounded cursor state. It has no transport or runtime integration.
package managedbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	ProtocolVersion = "mecha.managed-rpc.v1"
	MaxFrameBytes   = 64 * 1024
	MaxStringBytes  = 1024
	MaxPayloadBytes = 32 * 1024
	MaxJSONDepth    = 64
)

var (
	ErrInvalidFrame        = errors.New("managed RPC frame is invalid")
	ErrInvalidIdentity     = errors.New("managed RPC identity is invalid")
	ErrInvalidFence        = errors.New("managed RPC runtime fence is invalid")
	ErrInvalidTransition   = errors.New("managed RPC state transition is invalid")
	ErrStaleFrame          = errors.New("managed RPC frame is stale")
	ErrSecretSerialization = errors.New("managed RPC secret cannot be serialized")
)

var (
	identityRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hex64RE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	markerRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
)

// RuntimeFence identifies one exact Pi runtime incarnation.
type RuntimeFence struct {
	Agent             string `json:"agent"`
	BridgeInstance    string `json:"bridge_instance"`
	PiSessionID       string `json:"pi_session_id"`
	PiSessionEpoch    uint64 `json:"pi_session_epoch"`
	RuntimeGeneration uint64 `json:"runtime_generation"`
	PiPID             int64  `json:"pi_pid"`
	PiStartMarker     string `json:"pi_start_marker"`
	PiIncarnation     string `json:"pi_incarnation"`
	ManifestRevision  uint64 `json:"manifest_revision"`
	ManifestHash      string `json:"manifest_hash"`
}

func (f RuntimeFence) Validate() error {
	if !validIdentity(f.Agent) || !validUUID(f.BridgeInstance) || !validUUID(f.PiSessionID) ||
		f.PiSessionEpoch == 0 || f.RuntimeGeneration == 0 || f.PiPID <= 0 ||
		!validString(f.PiStartMarker) || !markerRE.MatchString(f.PiStartMarker) ||
		!validUUID(f.PiIncarnation) || f.ManifestRevision == 0 || !hex64RE.MatchString(f.ManifestHash) {
		return ErrInvalidFence
	}
	return nil
}

// PostAuth is the identity and freshness fence carried by every authenticated
// frame. Fence.Agent and Fence.BridgeInstance must match the outer identity.
type PostAuth struct {
	Agent               string       `json:"agent"`
	BridgeInstance      string       `json:"bridge_instance"`
	RouterInstanceNonce string       `json:"router_instance_nonce"`
	ConnectionNonce     string       `json:"connection_nonce"`
	Fence               RuntimeFence `json:"fence"`
}

func (p PostAuth) Validate() error {
	if !validIdentity(p.Agent) || !validUUID(p.BridgeInstance) || !validNonce(p.RouterInstanceNonce) || !validNonce(p.ConnectionNonce) {
		return ErrInvalidIdentity
	}
	if err := p.Fence.Validate(); err != nil || p.Fence.Agent != p.Agent || p.Fence.BridgeInstance != p.BridgeInstance {
		return ErrInvalidFence
	}
	return nil
}

// Challenge is sent by the future router. Nonces are generated per router
// instance and connection; they are never reused as authentication identity.
type Challenge struct {
	Protocol            string `json:"protocol"`
	Type                string `json:"type"`
	RouterInstanceNonce string `json:"router_instance_nonce"`
	ConnectionNonce     string `json:"connection_nonce"`
}

func (c Challenge) Validate() error {
	if c.Protocol != ProtocolVersion || c.Type != string(FrameChallenge) || !validNonce(c.RouterInstanceNonce) || !validNonce(c.ConnectionNonce) {
		return ErrInvalidFrame
	}
	return nil
}

// Authenticate proves possession of the verifier derived from the out-of-band
// token. The token and verifier are not fields in this or any wire type.
type Authenticate struct {
	Protocol            string       `json:"protocol"`
	Type                string       `json:"type"`
	Agent               string       `json:"agent"`
	BridgeInstance      string       `json:"bridge_instance"`
	RouterInstanceNonce string       `json:"router_instance_nonce"`
	ConnectionNonce     string       `json:"connection_nonce"`
	Fence               RuntimeFence `json:"fence"`
	Proof               string       `json:"proof"`
}

func (a Authenticate) Validate() error {
	if a.Protocol != ProtocolVersion || a.Type != string(FrameAuthenticate) ||
		!validIdentity(a.Agent) || !validUUID(a.BridgeInstance) ||
		!validNonce(a.RouterInstanceNonce) || !validNonce(a.ConnectionNonce) ||
		!hex64RE.MatchString(a.Proof) {
		return ErrInvalidFrame
	}
	p := PostAuth{Agent: a.Agent, BridgeInstance: a.BridgeInstance, RouterInstanceNonce: a.RouterInstanceNonce, ConnectionNonce: a.ConnectionNonce, Fence: a.Fence}
	return p.Validate()
}

func (a Authenticate) ValidateFor(c Challenge) error {
	if c.Validate() != nil || a.Validate() != nil ||
		a.RouterInstanceNonce != c.RouterInstanceNonce || a.ConnectionNonce != c.ConnectionNonce {
		return ErrInvalidFrame
	}
	return nil
}

// Authenticated is the router's proof that it accepted the authenticated
// bridge and the initial cursor positions.
type Authenticated struct {
	Protocol            string       `json:"protocol"`
	Type                string       `json:"type"`
	Agent               string       `json:"agent"`
	BridgeInstance      string       `json:"bridge_instance"`
	RouterInstanceNonce string       `json:"router_instance_nonce"`
	ConnectionNonce     string       `json:"connection_nonce"`
	Fence               RuntimeFence `json:"fence"`
	CommandCursor       uint64       `json:"command_cursor"`
	EventCursor         uint64       `json:"event_cursor"`
	Proof               string       `json:"proof"`
}

func (a Authenticated) Validate() error {
	if a.Protocol != ProtocolVersion || a.Type != string(FrameAuthenticated) || !hex64RE.MatchString(a.Proof) {
		return ErrInvalidFrame
	}
	return (PostAuth{Agent: a.Agent, BridgeInstance: a.BridgeInstance, RouterInstanceNonce: a.RouterInstanceNonce, ConnectionNonce: a.ConnectionNonce, Fence: a.Fence}).Validate()
}

// Resume requests replay from the last durably admitted command and event.
type Resume struct {
	PostAuth
	Protocol      string `json:"protocol"`
	Type          string `json:"type"`
	CommandCursor uint64 `json:"command_cursor"`
	EventCursor   uint64 `json:"event_cursor"`
}

func (r Resume) Validate() error {
	if r.Protocol != ProtocolVersion || r.Type != string(FrameResume) {
		return ErrInvalidFrame
	}
	return r.PostAuth.Validate()
}

// Command and Event use bounded opaque JSON payloads. Their names are closed;
// the payload is owned by the layer named by that vocabulary.
type Command struct {
	PostAuth
	Protocol string          `json:"protocol"`
	Type     string          `json:"type"`
	Seq      uint64          `json:"seq"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Payload  json.RawMessage `json:"payload"`
	Hash     string          `json:"hash"`
}

func (c Command) Validate() error {
	if c.Protocol != ProtocolVersion || c.Type != string(FrameCommand) || c.Seq == 0 || !validUUID(c.ID) || !validCommand(c.Name) || !validPayload(c.Payload) || !payloadHashMatches(c.Payload, c.Hash) {
		return ErrInvalidFrame
	}
	return c.PostAuth.Validate()
}

type Event struct {
	PostAuth
	Protocol string          `json:"protocol"`
	Type     string          `json:"type"`
	Seq      uint64          `json:"seq"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Payload  json.RawMessage `json:"payload"`
	Hash     string          `json:"hash"`
}

func (e Event) Validate() error {
	if e.Protocol != ProtocolVersion || e.Type != string(FrameEvent) || e.Seq == 0 || !validUUID(e.ID) || !validEvent(e.Name) || !validPayload(e.Payload) || !payloadHashMatches(e.Payload, e.Hash) {
		return ErrInvalidFrame
	}
	return e.PostAuth.Validate()
}

type Ack struct {
	PostAuth
	Protocol string `json:"protocol"`
	Type     string `json:"type"`
	Domain   string `json:"domain"`
	Seq      uint64 `json:"seq"`
}

func (a Ack) Validate() error {
	if a.Protocol != ProtocolVersion || a.Type != string(FrameAck) || a.Seq == 0 || !validDomain(a.Domain) {
		return ErrInvalidFrame
	}
	return a.PostAuth.Validate()
}

type ReplayGap struct {
	PostAuth
	Protocol string `json:"protocol"`
	Type     string `json:"type"`
	Domain   string `json:"domain"`
	Expected uint64 `json:"expected"`
	Floor    uint64 `json:"floor"`
}

func (r ReplayGap) Validate() error {
	if r.Protocol != ProtocolVersion || r.Type != string(FrameReplayGap) || r.Expected == 0 || r.Floor == 0 || r.Expected < r.Floor || !validDomain(r.Domain) {
		return ErrInvalidFrame
	}
	return r.PostAuth.Validate()
}

type FrameType string

const (
	FrameChallenge     FrameType = "challenge"
	FrameAuthenticate  FrameType = "authenticate"
	FrameAuthenticated FrameType = "authenticated"
	FrameResume        FrameType = "resume"
	FrameCommand       FrameType = "command"
	FrameEvent         FrameType = "event"
	FrameAck           FrameType = "ack"
	FrameReplayGap     FrameType = "replay_gap"
)

const (
	DomainCommands = "commands"
	DomainEvents   = "events"

	CommandPrompt   = "prompt"
	CommandAbort    = "abort"
	CommandShutdown = "shutdown"
	CommandManifest = "manifest"

	EventReady   = "ready"
	EventState   = "state"
	EventMessage = "message"
	EventTool    = "tool"
	EventError   = "error"
	EventStopped = "stopped"
)

func validCommand(name string) bool {
	switch name {
	case CommandPrompt, CommandAbort, CommandShutdown, CommandManifest:
		return true
	}
	return false
}
func validEvent(name string) bool {
	switch name {
	case EventReady, EventState, EventMessage, EventTool, EventError, EventStopped:
		return true
	}
	return false
}
func validDomain(domain string) bool  { return domain == DomainCommands || domain == DomainEvents }
func validIdentity(value string) bool { return validString(value) && identityRE.MatchString(value) }
func validUUID(value string) bool {
	return uuidRE.MatchString(value) && strings.ToLower(value) == value
}
func validString(value string) bool {
	return utf8.ValidString(value) && len([]byte(value)) > 0 && len([]byte(value)) <= MaxStringBytes
}
func validNonce(value string) bool { return len(value) == 64 && hex64RE.MatchString(value) }
func validPayload(value []byte) bool {
	if len(value) == 0 || len(value) > MaxPayloadBytes || rejectDuplicateKeys(value) != nil {
		return false
	}
	var decoded any
	return json.Unmarshal(value, &decoded) == nil && decoded != nil
}

func payloadHashMatches(payload []byte, hash string) bool {
	if !hex64RE.MatchString(hash) || !validPayload(payload) {
		return false
	}
	digest, _ := HashPayload(payload)
	return digest == hash
}

// NewChallenge creates fresh random nonces. It is deterministic only in its
// shape; the future transport owns when and where it is sent.
func NewChallenge() (Challenge, error) {
	var values [64]byte
	if _, err := rand.Read(values[:]); err != nil {
		return Challenge{}, ErrInvalidFrame
	}
	return Challenge{Protocol: ProtocolVersion, Type: string(FrameChallenge), RouterInstanceNonce: hex.EncodeToString(values[:32]), ConnectionNonce: hex.EncodeToString(values[32:])}, nil
}

// EncodeFrame validates and strictly bounds a known wire frame.
func EncodeFrame(frame any) ([]byte, error) {
	if !knownFrame(frame) {
		return nil, ErrInvalidFrame
	}
	data, err := json.Marshal(frame)
	if err != nil || len(data) > MaxFrameBytes || rejectDuplicateKeys(data) != nil {
		return nil, ErrInvalidFrame
	}
	return data, nil
}

// DecodeFrame rejects unknown fields, wrong protocol/type envelopes, trailing
// JSON, malformed known frames, and oversized input.
func DecodeFrame(data []byte) (any, error) {
	if len(data) == 0 || len(data) > MaxFrameBytes || rejectDuplicateKeys(data) != nil {
		return nil, ErrInvalidFrame
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return nil, ErrInvalidFrame
	}
	var header struct {
		Protocol string `json:"protocol"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Protocol != ProtocolVersion ||
		!hasExactFrameKeys(envelope, FrameType(header.Type)) {
		return nil, ErrInvalidFrame
	}
	var frame any
	switch FrameType(header.Type) {
	case FrameChallenge:
		frame = Challenge{}
	case FrameAuthenticate:
		frame = Authenticate{}
	case FrameAuthenticated:
		frame = Authenticated{}
	case FrameResume:
		frame = Resume{}
	case FrameCommand:
		frame = Command{}
	case FrameEvent:
		frame = Event{}
	case FrameAck:
		frame = Ack{}
	case FrameReplayGap:
		frame = ReplayGap{}
	default:
		return nil, ErrInvalidFrame
	}
	switch frame.(type) {
	case Challenge:
		var value Challenge
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Authenticate:
		var value Authenticate
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Authenticated:
		var value Authenticated
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Resume:
		var value Resume
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Command:
		var value Command
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Event:
		var value Event
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case Ack:
		var value Ack
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	case ReplayGap:
		var value ReplayGap
		if decodeStrict(data, &value) != nil || value.Validate() != nil {
			return nil, ErrInvalidFrame
		}
		return value, nil
	}
	return nil, ErrInvalidFrame
}

func knownFrame(frame any) bool {
	switch value := frame.(type) {
	case *Challenge:
		return value != nil && knownFrame(*value)
	case *Authenticate:
		return value != nil && knownFrame(*value)
	case *Authenticated:
		return value != nil && knownFrame(*value)
	case *Resume:
		return value != nil && knownFrame(*value)
	case *Command:
		return value != nil && knownFrame(*value)
	case *Event:
		return value != nil && knownFrame(*value)
	case *Ack:
		return value != nil && knownFrame(*value)
	case *ReplayGap:
		return value != nil && knownFrame(*value)
	case Challenge:
		return value.Validate() == nil
	case Authenticate:
		return value.Validate() == nil
	case Authenticated:
		return value.Validate() == nil
	case Resume:
		return value.Validate() == nil
	case Command:
		return value.Validate() == nil
	case Event:
		return value.Validate() == nil
	case Ack:
		return value.Validate() == nil
	case ReplayGap:
		return value.Validate() == nil
	default:
		return false
	}
}

func hasExactFrameKeys(envelope map[string]json.RawMessage, frameType FrameType) bool {
	var allowed []string
	switch frameType {
	case FrameChallenge:
		allowed = []string{"protocol", "type", "router_instance_nonce", "connection_nonce"}
	case FrameAuthenticate:
		allowed = []string{"protocol", "type", "agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "proof"}
	case FrameAuthenticated:
		allowed = []string{"protocol", "type", "agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "command_cursor", "event_cursor", "proof"}
	case FrameResume:
		allowed = []string{"agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "protocol", "type", "command_cursor", "event_cursor"}
	case FrameCommand, FrameEvent:
		allowed = []string{"agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "protocol", "type", "seq", "id", "name", "payload", "hash"}
	case FrameAck:
		allowed = []string{"agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "protocol", "type", "domain", "seq"}
	case FrameReplayGap:
		allowed = []string{"agent", "bridge_instance", "router_instance_nonce", "connection_nonce", "fence", "protocol", "type", "domain", "expected", "floor"}
	default:
		return false
	}
	if !containsOnlyKeys(envelope, allowed) {
		return false
	}
	if rawFence, ok := envelope["fence"]; ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawFence, &fields); err != nil || fields == nil ||
			!containsOnlyKeys(fields, []string{"agent", "bridge_instance", "pi_session_id", "pi_session_epoch", "runtime_generation", "pi_pid", "pi_start_marker", "pi_incarnation", "manifest_revision", "manifest_hash"}) {
			return false
		}
	}
	return true
}

func containsOnlyKeys(value map[string]json.RawMessage, allowed []string) bool {
	for key := range value {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			if depth >= MaxJSONDepth {
				return ErrInvalidFrame
			}
			switch delimiter {
			case '{':
				seen := make(map[string]struct{})
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok {
						return ErrInvalidFrame
					}
					if _, exists := seen[name]; exists {
						return ErrInvalidFrame
					}
					seen[name] = struct{}{}
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return ErrInvalidFrame
				}
			case '[':
				for decoder.More() {
					if err := walk(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return ErrInvalidFrame
				}
			default:
				return ErrInvalidFrame
			}
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidFrame
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidFrame
	}
	return nil
}
