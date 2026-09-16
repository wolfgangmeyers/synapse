package managedbridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ManagedRuntimeStatusVersion = "mecha.managed-runtime-status.v1"
	ManagedRuntimeStatusMode    = "managed_bridge"
	MaxStatusBytes              = 16 * 1024
)

var (
	ErrInvalidStatus = errors.New("managed bridge runtime status is invalid")
	ErrStatusIO      = errors.New("managed bridge runtime status is unavailable")
	ErrStatusProof   = errors.New("managed bridge runtime status proof is invalid")
)

// ManagedRuntimeStatus is the signed, local-only identity descriptor for one
// bridge and its Pi child. It deliberately has no launch or connection data.
type ManagedRuntimeStatus struct {
	Version        string          `json:"version"`
	Mode           string          `json:"mode"`
	AgentID        string          `json:"agent_id"`
	BridgeInstance string          `json:"bridge_instance"`
	Fence          RuntimeFence    `json:"fence"`
	InnerPi        ProcessIdentity `json:"inner_pi"`
	OuterBridge    ProcessIdentity `json:"outer_bridge"`
	CreatedAt      string          `json:"created_at"`
	Proof          string          `json:"proof"`
}

func (s ManagedRuntimeStatus) String() string   { return "managed bridge runtime status [redacted]" }
func (s ManagedRuntimeStatus) GoString() string { return "managed bridge runtime status [redacted]" }

// NewManagedRuntimeStatus creates an unsigned descriptor. Sign it before
// writing it to disk.
func NewManagedRuntimeStatus(fence RuntimeFence, inner, outer ProcessIdentity, now time.Time) ManagedRuntimeStatus {
	return ManagedRuntimeStatus{Version: ManagedRuntimeStatusVersion, Mode: ManagedRuntimeStatusMode,
		AgentID: fence.Agent, BridgeInstance: fence.BridgeInstance, Fence: fence,
		InnerPi: inner, OuterBridge: outer, CreatedAt: now.UTC().Format(time.RFC3339Nano)}
}

func (s ManagedRuntimeStatus) validateUnsigned() error {
	if s.Version != ManagedRuntimeStatusVersion || s.Mode != ManagedRuntimeStatusMode ||
		!validIdentity(s.AgentID) || !validUUID(s.BridgeInstance) || s.Fence.Validate() != nil ||
		s.AgentID != s.Fence.Agent || s.BridgeInstance != s.Fence.BridgeInstance ||
		!validProcessIdentity(s.InnerPi) || !validProcessIdentity(s.OuterBridge) ||
		s.InnerPi.PID != int(s.Fence.PiPID) || s.InnerPi.StartMarker != s.Fence.PiStartMarker || s.InnerPi.Incarnation != s.Fence.PiIncarnation {
		return ErrInvalidStatus
	}
	parsed, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != s.CreatedAt {
		return ErrInvalidStatus
	}
	return nil
}

func validProcessIdentity(identity ProcessIdentity) bool {
	return identity.PID > 0 && validString(identity.StartMarker) && markerRE.MatchString(identity.StartMarker) && validUUID(identity.Incarnation)
}

func statusTranscript(s ManagedRuntimeStatus) []byte {
	w := transcriptWriter{data: make([]byte, 0, 768)}
	w.string(ManagedRuntimeStatusVersion)
	w.string("managed-runtime-status")
	w.string(s.Mode)
	w.string(s.AgentID)
	w.string(s.BridgeInstance)
	w.fence(s.Fence)
	w.uint64(uint64(s.InnerPi.PID))
	w.string(s.InnerPi.StartMarker)
	w.string(s.InnerPi.Incarnation)
	w.uint64(uint64(s.OuterBridge.PID))
	w.string(s.OuterBridge.StartMarker)
	w.string(s.OuterBridge.Incarnation)
	w.string(s.CreatedAt)
	return w.data
}

func SignManagedRuntimeStatus(verifier Verifier, status ManagedRuntimeStatus) (ManagedRuntimeStatus, error) {
	if !verifier.initialized || status.validateUnsigned() != nil {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	status.Proof = proofKey(verifier.key[:], statusTranscript(status))
	return status, nil
}

func VerifyManagedRuntimeStatus(verifier Verifier, status ManagedRuntimeStatus) error {
	if !verifier.initialized || status.validateUnsigned() != nil || !hex64RE.MatchString(status.Proof) {
		return ErrStatusProof
	}
	want := proofKey(verifier.key[:], statusTranscript(status))
	left, leftErr := hexDecode(status.Proof)
	right, rightErr := hexDecode(want)
	if leftErr != nil || rightErr != nil || len(left) != len(right) || subtle.ConstantTimeCompare(left, right) != 1 {
		return ErrStatusProof
	}
	return nil
}

func hexDecode(value string) ([]byte, error) {
	decoded := make([]byte, len(value)/2)
	for i := range decoded {
		var high, low byte
		if value[2*i] >= '0' && value[2*i] <= '9' {
			high = value[2*i] - '0'
		} else if value[2*i] >= 'a' && value[2*i] <= 'f' {
			high = value[2*i] - 'a' + 10
		} else {
			return nil, ErrStatusProof
		}
		if value[2*i+1] >= '0' && value[2*i+1] <= '9' {
			low = value[2*i+1] - '0'
		} else if value[2*i+1] >= 'a' && value[2*i+1] <= 'f' {
			low = value[2*i+1] - 'a' + 10
		} else {
			return nil, ErrStatusProof
		}
		decoded[i] = high<<4 | low
	}
	return decoded, nil
}

func statusExactKeys(value map[string]json.RawMessage) bool {
	return containsOnlyKeys(value, []string{"version", "mode", "agent_id", "bridge_instance", "fence", "inner_pi", "outer_bridge", "created_at", "proof"})
}

func statusScalar(raw json.RawMessage, target any) bool {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

func DecodeManagedRuntimeStatus(data []byte) (ManagedRuntimeStatus, error) {
	if len(data) == 0 || len(data) > MaxStatusBytes || rejectDuplicateKeys(data) != nil {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil || !statusExactKeys(fields) {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	var version, mode, agent, bridge, created, proof string
	if !statusScalar(fields["version"], &version) || !statusScalar(fields["mode"], &mode) || !statusScalar(fields["agent_id"], &agent) || !statusScalar(fields["bridge_instance"], &bridge) || !statusScalar(fields["created_at"], &created) || !statusScalar(fields["proof"], &proof) {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	var status ManagedRuntimeStatus
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&status) != nil {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	// Reject scalar coercions and missing nested fields explicitly. encoding/json
	// otherwise silently accepts absent values as zero values.
	var fence map[string]json.RawMessage
	var inner, outer map[string]json.RawMessage
	if json.Unmarshal(fields["fence"], &fence) != nil || fence == nil || !containsOnlyKeys(fence, []string{"agent", "bridge_instance", "pi_session_id", "pi_session_epoch", "runtime_generation", "pi_pid", "pi_start_marker", "pi_incarnation", "manifest_revision", "manifest_hash"}) || json.Unmarshal(fields["inner_pi"], &inner) != nil || inner == nil || !containsOnlyKeys(inner, []string{"pid", "start_marker", "incarnation"}) || json.Unmarshal(fields["outer_bridge"], &outer) != nil || outer == nil || !containsOnlyKeys(outer, []string{"pid", "start_marker", "incarnation"}) {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	if len(fence) != 10 || len(inner) != 3 || len(outer) != 3 {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	for _, key := range []string{"agent", "bridge_instance", "pi_session_id", "pi_start_marker", "pi_incarnation", "manifest_hash"} {
		var value string
		if !statusScalar(fence[key], &value) {
			return ManagedRuntimeStatus{}, ErrInvalidStatus
		}
	}
	for _, key := range []string{"pi_session_epoch", "runtime_generation", "pi_pid", "manifest_revision"} {
		var value json.Number
		if !statusScalar(fence[key], &value) || value.String() != string(bytes.TrimSpace(fence[key])) {
			return ManagedRuntimeStatus{}, ErrInvalidStatus
		}
	}
	for _, object := range []map[string]json.RawMessage{inner, outer} {
		var pid json.Number
		if !statusScalar(object["pid"], &pid) || pid.String() != string(bytes.TrimSpace(object["pid"])) {
			return ManagedRuntimeStatus{}, ErrInvalidStatus
		}
		for _, key := range []string{"start_marker", "incarnation"} {
			var value string
			if !statusScalar(object[key], &value) {
				return ManagedRuntimeStatus{}, ErrInvalidStatus
			}
		}
	}
	if status.Version != version || status.Mode != mode || status.AgentID != agent || status.BridgeInstance != bridge || status.CreatedAt != created || status.Proof != proof || status.validateUnsigned() != nil {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	return status, nil
}

const (
	statusLockName   = ".managed-runtime-status.lock"
	statusTempPrefix = ".managed-runtime-status.tmp."
)

func isReservedStatusBasename(name string) bool {
	return name == statusLockName || strings.HasPrefix(name, statusTempPrefix)
}

type statusFileOps struct {
	fsync   func(int) error
	rename  func(int, string, int, string) error
	unlink  func(int, string, int) error
	fstatat func(int, string, *unix.Stat_t, int) error
}

var nativeStatusFileOps = statusFileOps{
	fsync:   unix.Fsync,
	rename:  unix.Renameat,
	unlink:  unix.Unlinkat,
	fstatat: unix.Fstatat,
}

func normalizeStatusFileOps(ops statusFileOps) statusFileOps {
	if ops.fsync == nil {
		ops.fsync = nativeStatusFileOps.fsync
	}
	if ops.rename == nil {
		ops.rename = nativeStatusFileOps.rename
	}
	if ops.unlink == nil {
		ops.unlink = nativeStatusFileOps.unlink
	}
	if ops.fstatat == nil {
		ops.fstatat = nativeStatusFileOps.fstatat
	}
	return ops
}

func openStatusLock(parent int) (*os.File, error) {
	fd, err := unix.Openat(parent, statusLockName, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Getuid()) {
		_ = unix.Close(fd)
		return nil, ErrStatusIO
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "managed runtime status lock"), nil
}

func exactStatusDescriptor(parent int, name string, expected *unix.Stat_t, ops statusFileOps) error {
	var current unix.Stat_t
	if err := ops.fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if current.Dev != expected.Dev || current.Ino != expected.Ino || current.Mode&unix.S_IFMT != unix.S_IFREG || current.Mode&0777 != 0600 || current.Uid != uint32(os.Getuid()) {
		return ErrStatusProof
	}
	if err := ops.unlink(parent, name, 0); err != nil {
		return err
	}
	return ops.fsync(parent)
}

func publishManagedRuntimeStatusWithOps(path string, status ManagedRuntimeStatus, verifier Verifier, rawOps statusFileOps) error {
	ops := normalizeStatusFileOps(rawOps)
	if !absolutePath(path) || isReservedStatusBasename(filepath.Base(path)) || VerifyManagedRuntimeStatus(verifier, status) != nil {
		return ErrInvalidStatus
	}
	name := filepath.Base(path)
	parent, err := openSecureParent(path)
	if err != nil {
		return ErrStatusIO
	}
	defer unix.Close(parent)
	lock, err := openStatusLock(parent)
	if err != nil {
		return ErrStatusIO
	}
	defer lock.Close()
	data, err := json.Marshal(status)
	if err != nil || len(data) > MaxStatusBytes {
		return ErrStatusIO
	}
	tempID, err := newUUID()
	if err != nil {
		return ErrStatusIO
	}
	tempName := statusTempPrefix + tempID
	fd, err := unix.Openat(parent, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrStatusIO
	}
	file := os.NewFile(uintptr(fd), "managed runtime status temporary")
	if file == nil {
		_ = unix.Close(fd)
		return ErrStatusIO
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = ops.unlink(parent, tempName, 0)
		}
	}()
	n, writeErr := file.Write(data)
	syncErr := file.Sync()
	chmodErr := unix.Fchmod(fd, 0600)
	var published unix.Stat_t
	statErr := unix.Fstat(fd, &published)
	closeErr := file.Close()
	if n != len(data) || writeErr != nil || syncErr != nil || chmodErr != nil || statErr != nil || published.Mode&unix.S_IFMT != unix.S_IFREG || published.Mode&0777 != 0600 || published.Uid != uint32(os.Getuid()) || closeErr != nil {
		return ErrStatusIO
	}
	// Never replace a symlink at the final name. The lock serializes normal
	// publishers/removers; rename still gives the final replacement atomicity.
	var existing unix.Stat_t
	if err := ops.fstatat(parent, name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil && existing.Mode&unix.S_IFMT == unix.S_IFLNK {
		return ErrStatusIO
	}
	if err := ops.rename(parent, tempName, parent, name); err != nil {
		return ErrStatusIO
	}
	removeTemp = false
	if err := ops.fsync(parent); err != nil {
		cleanupErr := exactStatusDescriptor(parent, filepath.Base(path), &published, ops)
		if cleanupErr != nil {
			return errors.Join(ErrStatusIO, err, cleanupErr)
		}
		return errors.Join(ErrStatusIO, err)
	}
	return nil
}

func PublishManagedRuntimeStatus(path string, status ManagedRuntimeStatus, verifier Verifier) error {
	return publishManagedRuntimeStatusWithOps(path, status, verifier, nativeStatusFileOps)
}

func ReadManagedRuntimeStatus(path string, verifier Verifier) (ManagedRuntimeStatus, error) {
	if !absolutePath(path) || isReservedStatusBasename(filepath.Base(path)) || !verifier.initialized {
		return ManagedRuntimeStatus{}, ErrInvalidStatus
	}
	data, err := load0600(path, MaxStatusBytes)
	if err != nil {
		return ManagedRuntimeStatus{}, ErrStatusIO
	}
	status, err := DecodeManagedRuntimeStatus(data)
	if err != nil || VerifyManagedRuntimeStatus(verifier, status) != nil {
		return ManagedRuntimeStatus{}, ErrStatusProof
	}
	return status, nil
}

// RemoveManagedRuntimeStatus is explicit and conservative: it removes only an
// exact signed descriptor after the recorded Pi identity is absent and the
// recorded bridge identity is absent or is this verified caller.
func readStatusAt(parent int, name string, verifier Verifier) (ManagedRuntimeStatus, unix.Stat_t, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ManagedRuntimeStatus{}, unix.Stat_t{}, ErrStatusIO
	}
	file := os.NewFile(uintptr(fd), "managed runtime status")
	if file == nil {
		_ = unix.Close(fd)
		return ManagedRuntimeStatus{}, unix.Stat_t{}, ErrStatusIO
	}
	defer file.Close()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Mode&0777 != 0600 || opened.Uid != uint32(os.Getuid()) {
		return ManagedRuntimeStatus{}, unix.Stat_t{}, ErrStatusIO
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxStatusBytes+1))
	if err != nil || len(data) > MaxStatusBytes {
		return ManagedRuntimeStatus{}, unix.Stat_t{}, ErrStatusIO
	}
	status, err := DecodeManagedRuntimeStatus(data)
	if err != nil || VerifyManagedRuntimeStatus(verifier, status) != nil {
		return ManagedRuntimeStatus{}, unix.Stat_t{}, ErrStatusProof
	}
	return status, opened, nil
}

func RemoveManagedRuntimeStatus(ctx context.Context, path string, expected ManagedRuntimeStatus, verifier Verifier) error {
	return removeManagedRuntimeStatusWithOps(ctx, path, expected, verifier, nativeStatusFileOps)
}

func removeManagedRuntimeStatusWithOps(ctx context.Context, path string, expected ManagedRuntimeStatus, verifier Verifier, rawOps statusFileOps) error {
	ops := normalizeStatusFileOps(rawOps)
	if ctx == nil || !absolutePath(path) || isReservedStatusBasename(filepath.Base(path)) || VerifyManagedRuntimeStatus(verifier, expected) != nil {
		return ErrInvalidStatus
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, err := openSecureParent(path)
	if err != nil {
		return ErrStatusIO
	}
	defer unix.Close(parent)
	lock, err := openStatusLock(parent)
	if err != nil {
		return ErrStatusIO
	}
	defer lock.Close()
	actual, opened, err := readStatusAt(parent, filepath.Base(path), verifier)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrStatusProof
	}
	// The bridge descriptor names two different process roles. The outer
	// bridge must be a group leader; Pi is intentionally only a member of that
	// group and must be checked with the member probe. These assertions are
	// mode-specific: standalone requires a leader, managed Pi only requires
	// group membership.
	outerState := probeProcess(actual.OuterBridge)
	if outerState != processAbsent && (outerState != processOwned || actual.OuterBridge.PID != os.Getpid()) {
		return ErrStatusIO
	}
	if probeManagedMember(actual.InnerPi, actual.OuterBridge.PID) != processAbsent {
		return ErrStatusIO
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := exactStatusDescriptor(parent, filepath.Base(path), &opened, ops); err != nil {
		return ErrStatusIO
	}
	return nil
}
