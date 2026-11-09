package managed

// Startup-only primitive. Integration is deliberately deferred to the runtime
// owner. Never call recovery while this router is admitting Claude processes.
// Spec: claude-code-workers-spec.md §3.7, §3.13, AC7/AC12.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const claudeCodeOrphanLimit = 1024

// Router startup owns the session root exclusively. Serialize in-process
// reservations through publication so concurrent launches cannot exceed the cap.
var claudeOrphanPublishMu sync.Mutex

type claudeCodeOrphanRecord struct {
	Version  int
	Boot     string
	Identity ProcessIdentity
}

// persistClaudeCodeOrphan must complete after Start and before any stdin or
// runtime publication. On failure the caller must stop the just-started group.
// One record per live group; remove its returned path ONLY after verified stop.
// Records contain no prompt, credential, command line or conversation content.
func persistClaudeCodeOrphan(root string, pid int, incarnation string) (string, error) {
	claudeOrphanPublishMu.Lock()
	defer claudeOrphanPublishMu.Unlock()
	boot, err := claudeCodeOrphanBoot()
	if err != nil {
		return "", err
	}
	dir, err := claudeCodeOrphanDir(root, "claude-orphans")
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) >= claudeCodeOrphanLimit {
		return "", errors.New("Claude orphan record capacity reached")
	}
	marker, err := processStartMarker(pid)
	if err != nil {
		return "", err
	}
	identity := ProcessIdentity{PID: pid, StartMarker: marker, Incarnation: incarnation}
	if (defaultProcessGroupOps{}).Probe(identity).State != ProcessOwned {
		return "", errors.New("Claude group identity unavailable")
	}
	f, err := os.CreateTemp(dir, ".orphan-tmp-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(claudeCodeOrphanRecord{Version: 1, Boot: boot, Identity: identity})
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	// Link is an atomic, no-replace publication in the same directory. The
	// temporary name is never an authoritative recovery record.
	published := filepath.Join(dir, "group-"+strings.TrimPrefix(filepath.Base(path), ".orphan-tmp-")+".json")
	if err = os.Link(path, published); err != nil {
		return "", err
	}
	if err = os.Remove(path); err != nil {
		return "", err
	}
	if err = claudeCodeOrphanSync(dir); err != nil {
		return "", err
	}
	ok = true
	return published, nil
}

// reapClaudeCodeOrphans runs under exclusive router startup ownership before
// token issuance/process admission. Returns error for any unsafe identity, so
// callers MUST NOT admit replacements on error. No leaderless-group inference:
// persisted identity alone cannot prove surviving descendants still belong to us.
// Success/absence removes records immediately; unsafe ones remain, capped at
// 1024 (no history). All private config/prompt leaves are swept on every startup,
// including files created before a crash could persist a process record.
func reapClaudeCodeOrphans(ctx context.Context, root string) error {
	if ctx == nil {
		return errors.New("Claude recovery context required")
	}
	boot, err := claudeCodeOrphanBoot()
	if err != nil {
		return err
	}
	dir, err := claudeCodeOrphanDir(root, "claude-orphans")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) > claudeCodeOrphanLimit {
		return errors.New("Claude orphan record capacity exceeded")
	}
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, entry.Name())
		if strings.HasPrefix(entry.Name(), ".orphan-tmp-") {
			info, statErr := os.Lstat(path)
			if statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
				statErr = errors.New("invalid private Claude orphan temporary file")
			}
			if statErr == nil {
				statErr = os.Remove(path)
			}
			if statErr != nil {
				failures = append(failures, fmt.Errorf("Claude orphan %s: %w", entry.Name(), statErr))
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), "group-") || !strings.HasSuffix(entry.Name(), ".json") || len(entry.Name()) <= len("group-")+len(".json") {
			failures = append(failures, fmt.Errorf("Claude orphan %s: unexpected filename", entry.Name()))
			continue
		}
		record, err := readClaudeCodeOrphan(path)
		if err == nil {
			if record.Boot == boot {
				err = reapClaudeCodeOrphan(ctx, record.Identity)
			} else if runtime.GOOS != "linux" || !claudeLinuxBootID(record.Boot) {
				err = errors.New("Claude orphan boot identity unsafe")
			}
		}
		if err == nil {
			err = os.Remove(path)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("Claude orphan %s: %w", entry.Name(), err))
		}
	}
	for _, name := range []string{"claude-processes", "claude-prompts"} {
		tempDir, e := claudeCodeOrphanDir(root, name)
		if e == nil {
			var files []os.DirEntry
			files, e = os.ReadDir(tempDir)
			if e == nil {
				for _, file := range files {
					// Remove, not RemoveAll: no following links or descending into unknown trees.
					if removeErr := os.Remove(filepath.Join(tempDir, file.Name())); removeErr != nil {
						failures = append(failures, removeErr)
					}
				}
			}
		}
		if e == nil {
			e = claudeCodeOrphanSync(tempDir)
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	if e := claudeCodeOrphanSync(dir); e != nil {
		failures = append(failures, e)
	}
	return errors.Join(failures...)
}

func reapClaudeCodeOrphan(ctx context.Context, identity ProcessIdentity) error {
	ops := defaultProcessGroupOps{}
	switch ops.Probe(identity).State {
	case ProcessAbsent:
		return nil
	case ProcessOwned:
		// SignalVerifiedProcessGroup immediately rechecks the persisted identity.
		if err := ops.SignalVerifiedProcessGroup(identity, syscall.SIGKILL); err != nil {
			if ops.Probe(identity).State == ProcessAbsent {
				return nil
			}
			return err
		}
		confirmCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return confirmClaudeCodeGroupAbsent(confirmCtx, func() ProcessProbeState { return ops.Probe(identity).State })
	default:
		return errors.New("Claude orphan identity unsafe; group untouched")
	}
}

// Linux boot IDs are UUIDs. A legacy constant or malformed value cannot
// establish that a record predates the current boot.
func claudeLinuxBootID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func readClaudeCodeOrphan(path string) (claudeCodeOrphanRecord, error) {
	var record claudeCodeOrphanRecord
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return record, errors.New("invalid private Claude orphan record")
	}
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&record); err != nil {
		return record, err
	}
	var extra interface{}
	if decoder.Decode(&extra) != io.EOF {
		return record, errors.New("trailing orphan record data")
	}
	if record.Version != 1 || record.Boot == "" {
		return record, errors.New("unsupported Claude orphan record")
	}
	return record, ValidateIdentity(record.Identity)
}

func claudeCodeOrphanDir(root, name string) (string, error) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	// Only this dedicated leaf is private. Ordinary 0755 ancestors are accepted.
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", errors.New("Claude private leaf must be a real 0700 directory")
	}
	// Sync each ancestor: MkdirAll may have created the root as well as the
	// private leaf. Syncing only the leaf does not persist its directory entry.
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		if err := claudeCodeOrphanSync(parent); err != nil {
			return "", err
		}
		if filepath.Dir(parent) == parent || parent == "." {
			break
		}
	}
	return dir, nil
}
func claudeCodeOrphanBoot() (string, error) {
	if runtime.GOOS != "linux" {
		// Other Unix platforms use the existing ps start marker. Never discard
		// a record merely because boot identity is unavailable.
		return "unix-start-marker-v1", nil
	}
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	boot := strings.TrimSpace(string(data))
	if boot == "" {
		return "", errors.New("missing boot identity")
	}
	return boot, nil
}
func claudeCodeOrphanSync(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Recovery errors can contain private paths, filenames, or malformed record
// contents. Expose only a bounded OS error category, never the raw error.
type claudeSafeRecoveryError struct{ message string }

func (e *claudeSafeRecoveryError) Error() string { return e.message }

func safeClaudeRecoveryError(err error) error {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return &claudeSafeRecoveryError{message: fmt.Sprintf("Claude orphan recovery failed (operating system error: %s)", errno.Error())}
	}
	return &claudeSafeRecoveryError{message: "Claude orphan recovery failed (unsafe or unavailable recovery records)"}
}

// RecoverClaudeCodeOrphans must be called once during router startup, before
// Claude runtime construction/admission. Error blocks Claude admission, while
// unrelated runtimes may continue. The startup caller owns
// exclusive access to SessionRoot (the same ownership as the router database).
func (m *Manager) RecoverClaudeCodeOrphans(ctx context.Context) error {
	return reapClaudeCodeOrphans(ctx, m.config.SessionRoot)
}

func (p *ClaudeCodeProcess) forgetClaudeOrphanIfAbsent() {
	if p.orphanPath != "" && (defaultProcessGroupOps{}).Probe(p.orphanIdentity).State == ProcessAbsent {
		p.forgetClaudeOrphanConfirmed()
	}
}

// Caller holds either the pinned leader's absence proof or an absence probe.
// SessionRoot has exclusive router ownership; never remove a replaced record.
func (p *ClaudeCodeProcess) forgetClaudeOrphanConfirmed() {
	if p.orphanPath == "" {
		return
	}
	p.orphanCleanupOnce.Do(func() {
		if err := removeClaudeCodeOrphan(p.orphanPath, p.orphanIdentity); err != nil {
			p.orphanCleanupErr = err
			if p.onUnconfirmedStop != nil {
				p.onUnconfirmedStop(err)
			}
		}
	})
}

func removeClaudeCodeOrphan(path string, identity ProcessIdentity) error {
	record, err := readClaudeCodeOrphan(path)
	if err != nil {
		return err
	}
	if record.Identity != identity {
		return errors.New("Claude orphan identity changed")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return claudeCodeOrphanSync(filepath.Dir(path))
}

// The shell executes builtins only until the durable identity is synced. EOF
// (router SIGKILL) exits without ever executing the CLI or spawning descendants.
// exec preserves PID/start marker/group. FD 3 is closed before native CLI exec.
func claudeCodeGatedCommand(bin string, args []string) (*exec.Cmd, *os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	commandArgs := append([]string{"-c", `IFS= read -r ready <&3 || exit 125; [ "$ready" = ready ] || exit 125; exec 3<&-; exec "$@"`, "claude-launch", bin}, args...)
	cmd := exec.Command("/bin/sh", commandArgs...)
	cmd.ExtraFiles = []*os.File{r}
	// Parent's read end is closed after Start by caller's deferred cleanup.
	// The write end alone controls release; no durable record means no exec.
	return cmd, w, nil
}

var errClaudeCodeGroupUnconfirmed = errors.New("Claude group absence unconfirmed")

func confirmClaudeCodeGroupAbsent(ctx context.Context, probe func() ProcessProbeState) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if probe() == ProcessAbsent {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (cfg ClaudeCodeProcessConfig) blockUnconfirmedClaudeStop(err error) {
	if cfg.OnUnconfirmedStop != nil {
		cfg.OnUnconfirmedStop(err)
	}
}
