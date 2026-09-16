package managedbridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
)

// Verifier is the only authentication secret retained by this package. Its
// bytes are private so it cannot accidentally appear in a wire frame or log.
type Verifier struct {
	key         [32]byte
	initialized bool
}

// NewVerifier derives a fixed-size verifier from exactly one 256-bit token.
// The token is used only by the caller's authentication setup; it is not part
// of any managedbridge message or error. An all-zero token is rejected because
// it is a recognizable placeholder, not a usable high-entropy secret.
func NewVerifier(token []byte) (Verifier, error) {
	if len(token) != 32 || subtle.ConstantTimeCompare(token, make([]byte, 32)) == 1 {
		return Verifier{}, ErrInvalidFrame
	}
	var input [32]byte
	copy(input[:], token)
	derived := hmac.New(sha256.New, []byte("mecha-managed-rpc-verifier-v1"))
	_, _ = derived.Write(input[:])
	var verifier Verifier
	copy(verifier.key[:], derived.Sum(nil))
	verifier.initialized = true
	return verifier, nil
}

func (Verifier) MarshalJSON() ([]byte, error) { return nil, ErrSecretSerialization }
func (Verifier) String() string               { return "[redacted]" }
func (Verifier) GoString() string             { return "[redacted]" }

// AuthenticateFor creates a bridge proof for a router challenge.
func AuthenticateFor(verifier Verifier, challenge Challenge, fence RuntimeFence) (Authenticate, error) {
	if !verifier.initialized || challenge.Validate() != nil || fence.Validate() != nil {
		return Authenticate{}, ErrInvalidFrame
	}
	auth := Authenticate{
		Protocol: ProtocolVersion, Type: string(FrameAuthenticate),
		Agent: fence.Agent, BridgeInstance: fence.BridgeInstance,
		RouterInstanceNonce: challenge.RouterInstanceNonce,
		ConnectionNonce:     challenge.ConnectionNonce, Fence: fence,
	}
	auth.Proof = proof(verifier, bridgeTranscript(challenge, auth))
	return auth, nil
}

// VerifyAuthenticate checks the challenge binding and proof in constant time.
func VerifyAuthenticate(verifier Verifier, challenge Challenge, auth Authenticate) error {
	if !verifier.initialized || auth.ValidateFor(challenge) != nil {
		return ErrInvalidFrame
	}
	want := proof(verifier, bridgeTranscript(challenge, auth))
	if !constantTimeHexEqual(want, auth.Proof) {
		return ErrInvalidFrame
	}
	return nil
}

// AuthenticatedFor creates the router acceptance proof. Cursors are the
// router's already durable positions; zero means no prior sequence.
func AuthenticatedFor(verifier Verifier, challenge Challenge, auth Authenticate, commandCursor, eventCursor uint64) (Authenticated, error) {
	if !verifier.initialized || VerifyAuthenticate(verifier, challenge, auth) != nil {
		return Authenticated{}, ErrInvalidFrame
	}
	accepted := Authenticated{
		Protocol: ProtocolVersion, Type: string(FrameAuthenticated),
		Agent: auth.Agent, BridgeInstance: auth.BridgeInstance,
		RouterInstanceNonce: challenge.RouterInstanceNonce,
		ConnectionNonce:     challenge.ConnectionNonce, Fence: auth.Fence,
		CommandCursor: commandCursor, EventCursor: eventCursor,
	}
	accepted.Proof = proof(verifier, acceptanceTranscript(challenge, auth, accepted))
	return accepted, nil
}

// VerifyAuthenticated checks the router's mutual proof and cursor response.
func VerifyAuthenticated(verifier Verifier, challenge Challenge, auth Authenticate, accepted Authenticated) error {
	if !verifier.initialized || VerifyAuthenticate(verifier, challenge, auth) != nil || accepted.Validate() != nil {
		return ErrInvalidFrame
	}
	if accepted.Agent != auth.Agent || accepted.BridgeInstance != auth.BridgeInstance || accepted.RouterInstanceNonce != challenge.RouterInstanceNonce || accepted.ConnectionNonce != challenge.ConnectionNonce || accepted.Fence != auth.Fence {
		return ErrInvalidFrame
	}
	want := proof(verifier, acceptanceTranscript(challenge, auth, accepted))
	if !constantTimeHexEqual(want, accepted.Proof) {
		return ErrInvalidFrame
	}
	return nil
}

// transcriptWriter uses a fixed version, role, and length-delimited fields.
// Length prefixes keep concatenated values unambiguous and make the complete
// fence part of the proof without relying on JSON encoder behavior.
type transcriptWriter struct{ data []byte }

func (w *transcriptWriter) field(value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	w.data = append(w.data, length[:]...)
	w.data = append(w.data, value...)
}

func (w *transcriptWriter) string(value string) { w.field([]byte(value)) }
func (w *transcriptWriter) uint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	w.field(encoded[:])
}

func (w *transcriptWriter) fence(fence RuntimeFence) {
	w.string(fence.Agent)
	w.string(fence.BridgeInstance)
	w.string(fence.PiSessionID)
	w.uint64(fence.PiSessionEpoch)
	w.uint64(fence.RuntimeGeneration)
	w.uint64(uint64(fence.PiPID))
	w.string(fence.PiStartMarker)
	w.string(fence.PiIncarnation)
	w.uint64(fence.ManifestRevision)
	w.string(fence.ManifestHash)
}

func bridgeTranscript(challenge Challenge, auth Authenticate) []byte {
	// Explicit field ordering prevents JSON encoder changes from becoming an
	// accidental authentication-version change. No payload or secret is here.
	w := transcriptWriter{data: make([]byte, 0, 512)}
	w.string(ProtocolVersion)
	w.string("bridge-authenticate")
	w.string(challenge.RouterInstanceNonce)
	w.string(challenge.ConnectionNonce)
	w.string(auth.Agent)
	w.string(auth.BridgeInstance)
	w.fence(auth.Fence)
	return w.data
}

func acceptanceTranscript(challenge Challenge, auth Authenticate, accepted Authenticated) []byte {
	w := transcriptWriter{data: make([]byte, 0, 544)}
	w.string(ProtocolVersion)
	w.string("router-acceptance")
	w.string(challenge.RouterInstanceNonce)
	w.string(challenge.ConnectionNonce)
	w.string(auth.Agent)
	w.string(auth.BridgeInstance)
	w.fence(auth.Fence)
	w.uint64(accepted.CommandCursor)
	w.uint64(accepted.EventCursor)
	return w.data
}

func proof(verifier Verifier, transcript []byte) string {
	mac := hmac.New(sha256.New, verifier.key[:])
	_, _ = mac.Write(transcript)
	return hex.EncodeToString(mac.Sum(nil))
}

func constantTimeHexEqual(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	if leftErr != nil || rightErr != nil || len(leftBytes) != 32 || len(rightBytes) != 32 {
		return false
	}
	return hmac.Equal(leftBytes, rightBytes)
}
