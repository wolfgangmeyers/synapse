package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// RED authored before the primitive; do not execute until the coordinator opens
// the stable test window. These cross the real filesystem and OS group boundary.
func orphanDummy(t *testing.T) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("local Linux identity verification")
	}
	cmd := exec.Command("sh", "-c", "sleep 120 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	return cmd
}

func TestClaudeCodeOrphanRestartReapsAndSweeps(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0755))
	path, err := persistClaudeCodeOrphan(root, cmd.Process.Pid, "dummy-incarnation")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	for _, dir := range []string{"claude-processes", "claude-prompts"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, dir, "leftover"), []byte("private"), 0600))
	}
	// No in-memory process handle is passed to recovery (simulated router restart).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, reapClaudeCodeOrphans(ctx, root))
	data, err := processStartMarker(cmd.Process.Pid)
	if err == nil {
		require.Equal(t, ProcessAbsent, (defaultProcessGroupOps{}).Probe(ProcessIdentity{PID: cmd.Process.Pid, StartMarker: data, Incarnation: "test"}).State)
	}
	for _, dir := range []string{"claude-orphans", "claude-processes", "claude-prompts"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	require.NoError(t, reapClaudeCodeOrphans(ctx, root))
}

func TestClaudeCodeOrphanPIDReuseMismatchNeverSignals(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	path, err := persistClaudeCodeOrphan(root, cmd.Process.Pid, "dummy-incarnation")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var record claudeCodeOrphanRecord
	require.NoError(t, json.Unmarshal(data, &record))
	record.Identity.StartMarker += "-reused"
	data, err = json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	require.Error(t, reapClaudeCodeOrphans(context.Background(), root))
	require.NoError(t, syscall.Kill(cmd.Process.Pid, 0))
	require.FileExists(t, path, "unsafe identity retained; replacement must stay blocked")
}

func TestClaudeCodeOrphanSweepDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("local Linux identity verification")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "keep")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "claude-prompts"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "claude-prompts", "stale")))
	require.NoError(t, reapClaudeCodeOrphans(context.Background(), root))
	require.FileExists(t, outside)
}

// RED acceptance for the actual constructor boundary, not a manually invoked
// recovery helper. An unsafe record must prevent obtaining any admitting manager.
func TestClaudeCodeOrphanManagerStartupBoundary(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint("mismatch=", mismatch), func(t *testing.T) {
			cmd := orphanDummy(t)
			root := t.TempDir()
			ctx := context.Background()
			durable, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
			require.NoError(t, err)
			defer durable.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			sessions := filepath.Join(root, "sessions")
			path, err := persistClaudeCodeOrphan(sessions, cmd.Process.Pid, "prior-router")
			require.NoError(t, err)
			if mismatch {
				record, err := readClaudeCodeOrphan(path)
				require.NoError(t, err)
				record.Identity.StartMarker += "-reused"
				data, err := json.Marshal(record)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0600))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(sessions, "claude-prompts"), 0700))
			prompt := filepath.Join(sessions, "claude-prompts", "stale.prompt")
			require.NoError(t, os.WriteFile(prompt, []byte("private"), 0600))
			manager, err := NewManager(ManagerOptions{Store: durable, Manifest: manifest, Config: Config{SessionRoot: sessions, AllowedWorkingDirectoryRoots: []string{root}}})
			if mismatch {
				require.ErrorContains(t, err, "Claude orphan")
				require.Nil(t, manager, "no manager can admit/reacquire Claude on unsafe identity")
				require.NoError(t, syscall.Kill(cmd.Process.Pid, 0))
			} else {
				require.NoError(t, err)
				require.NoError(t, manager.Close(ctx))
				require.NoFileExists(t, path)
			}
			require.NoFileExists(t, prompt)
		})
	}
}

func TestClaudeCodeOrphanVerifiedZombieLeader(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux zombie identity")
	}
	cmd := exec.Command("sh", "-c", "sleep 120 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	marker, err := processStartMarker(cmd.Process.Pid)
	require.NoError(t, err)
	identity := ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: "zombie"}
	require.Eventually(t, func() bool { state, _, _ := linuxProcStat(identity.PID); return state == 'Z' }, time.Second, time.Millisecond)
	ops := defaultProcessGroupOps{}
	mismatch := identity
	mismatch.StartMarker += "-mismatch"
	require.Equal(t, ProcessMismatch, ops.Probe(mismatch).State)
	require.Equal(t, ProcessPresent, ops.Probe(identity).State)
	require.Error(t, ops.SignalVerifiedProcessGroup(identity, syscall.SIGKILL))
	require.Error(t, reapClaudeCodeOrphan(context.Background(), identity))
	require.NoError(t, syscall.Kill(-identity.PID, 0), "recovery must leave the live group untouched")
	// The local owner has a pinned child and can safely clean up this group.
	require.NoError(t, waitClaudeCodeCommand(cmd, identity))
	reaped = true
	require.Equal(t, ProcessAbsent, ops.Probe(identity).State)
}

func TestClaudeCodeOrphanGateEOFPreventsNativeExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist")
	cmd, gate, err := claudeCodeGatedCommand("/bin/sh", []string{"-c", `echo executed > "$1"`, "test", path})
	require.NoError(t, err)
	defer cmd.ExtraFiles[0].Close()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	cmd.ExtraFiles[0].Close()
	require.NoError(t, gate.Close())
	require.Error(t, cmd.Wait())
	require.NoFileExists(t, path)
}

func TestClaudeCodeOrphanCloseMismatchNeverSignals(t *testing.T) {
	cmd := orphanDummy(t)
	marker, err := processStartMarker(cmd.Process.Pid)
	require.NoError(t, err)
	exited, done := make(chan struct{}), make(chan struct{})
	close(exited)
	close(done)
	stdin, err := os.Open(os.DevNull)
	require.NoError(t, err)
	p := &ClaudeCodeProcess{cmd: cmd, stdin: stdin, orphanIdentity: ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker + "reused", Incarnation: "test"}, exited: exited, done: done, closeDone: make(chan struct{})}
	require.NoError(t, p.Close())
	require.NoError(t, syscall.Kill(cmd.Process.Pid, 0))
}

// Authored RED: delayed disappearance is a deterministic OS-boundary double;
// signal issuance must not stand in for confirmed group absence.
func TestClaudeCodeOrphanWaitRequiresAbsence(t *testing.T) {
	probes := 0
	err := confirmClaudeCodeGroupAbsent(context.Background(), func() ProcessProbeState {
		probes++
		if probes < 3 {
			return ProcessOwned
		}
		return ProcessAbsent
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, probes, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, confirmClaudeCodeGroupAbsent(ctx, func() ProcessProbeState { return ProcessOwned }))
}

func TestClaudeCodeOrphanWaitChildWithoutInheritedPipes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux WNOWAIT")
	}
	childPath := filepath.Join(t.TempDir(), "child.pid")
	// Hold the leader on a builtin read until the child's identity is captured.
	// No inherited output pipes may accidentally make cmd.Wait wait for it.
	cmd := exec.Command("sh", "-c", `sleep 120 </dev/null >/dev/null 2>&1 & printf '%s' "$!" > "$1"; read -r release; exit 0`, "test", childPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	marker, err := processStartMarker(cmd.Process.Pid)
	require.NoError(t, err)
	identity := ProcessIdentity{PID: cmd.Process.Pid, StartMarker: marker, Incarnation: "closed-pipes"}
	t.Cleanup(func() {
		_ = input.Close()
		_ = (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(identity, syscall.SIGKILL)
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	var childPID int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(childPath)
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && childPID > 0
	}, time.Second, time.Millisecond)
	childMarker, err := processStartMarker(childPID)
	require.NoError(t, err)
	state, group, err := linuxProcStat(childPID)
	require.NoError(t, err)
	require.Equal(t, identity.PID, group)
	require.NotContains(t, "ZX", string(state), "fixture must start with a live child")
	require.NoError(t, input.Close())

	path := filepath.Join(t.TempDir(), "orphan.json")
	boot, err := claudeCodeOrphanBoot()
	require.NoError(t, err)
	encoded, err := json.Marshal(claudeCodeOrphanRecord{Version: 1, Boot: boot, Identity: identity})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0600))
	process := &ClaudeCodeProcess{orphanPath: path, orphanIdentity: identity}
	require.NoError(t, waitClaudeCodeCommandConfirmed(cmd, identity, process.forgetClaudeOrphanConfirmed))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "pinned absence proof removes matching record")

	// Wait reaps the pinned leader only after every live group member is gone.
	// The orphan child's reaper can retain its zombie: a generic leaderless
	// Probe then correctly says Present, not Absent. Never relax that probe or
	// infer ownership from a numeric group after the leader has been reaped.
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(childPID), "stat"))
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	child, err := parseLinuxProcessStat(data)
	require.NoError(t, err)
	require.Equal(t, childMarker, "linux:"+child.startMarker, "do not mistake PID reuse for the verified child")
	require.Equal(t, identity.PID, child.pgrp)
	require.Contains(t, "ZX", string(child.state), "verified child must no longer be executable")
}

func TestClaudeCodeOrphanFailedStartClosesDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("proc descriptor evidence")
	}
	cfg := claudeTestConfig(t, `{}`)
	cfg.OrphanRoot = filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(cfg.OrphanRoot, []byte("block"), 0600))
	before, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err := StartClaudeCodeProcess(context.Background(), cfg)
		require.Error(t, err)
	}
	after, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	require.LessOrEqual(t, len(after), len(before)+2)
}

// RED: admission closure is in-memory and does not depend on writing a marker.
func TestClaudeCodeOrphanUnconfirmedBlocksPoolWithUnwritableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-directory")
	require.NoError(t, os.WriteFile(root, []byte("unwritable child path"), 0600))
	pool := &claudeCodeProcessPool{}
	cfg := ClaudeCodeProcessConfig{OrphanRoot: root}
	cfg.OnUnconfirmedStop = func() { pool.mu.Lock(); pool.closed = true; pool.mu.Unlock() }
	cfg.blockUnconfirmedClaudeStop()
	_, err := pool.acquire(context.Background(), store.EmbeddedBinding{}, cfg, nil)
	require.ErrorContains(t, err, "pool closed")
}

func TestClaudeCodeOrphanRecoveryHasNoPermanentPoison(t *testing.T) {
	cmd := orphanDummy(t)
	root := t.TempDir()
	path, err := persistClaudeCodeOrphan(root, cmd.Process.Pid, "unconfirmed-old-router")
	require.NoError(t, err)
	pool := &claudeCodeProcessPool{}
	cfg := ClaudeCodeProcessConfig{OrphanRoot: root, OnUnconfirmedStop: func() { pool.mu.Lock(); pool.closed = true; pool.mu.Unlock() }}
	cfg.blockUnconfirmedClaudeStop()
	require.True(t, pool.closed)
	require.NoError(t, reapClaudeCodeOrphans(context.Background(), root))
	require.NoFileExists(t, path)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Empty(t, entries)
	// A new manager owns a new pool after regular startup recovery: no persisted
	// poison bit can block it after the exact old group has been proven absent.
	next := &claudeCodeProcessPool{}
	require.False(t, next.closed)
}
