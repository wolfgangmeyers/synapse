//go:build linux

package managed

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A separate test process stands in for the router: only it owns the shell.
// No running router, shared state, or external services are involved.
func TestClaudePromotableBashRouterCrashRecovery(t *testing.T) {
	if os.Getenv("MECHA_BASH_CRASH_FIXTURE") == "1" {
		root, mode := os.Getenv("MECHA_BASH_CRASH_ROOT"), os.Getenv("MECHA_BASH_CRASH_MODE")
		jobs := NewBackgroundBashManager(nil)
		m := &Manager{backgroundBash: jobs}
		owner := embeddedOwner{identity: "claude-child", claudeOrphanRoot: root, backgroundBash: &backgroundBashOwnerCapability{}}
		command := fmt.Sprintf("echo $$ > %q; exec sleep 120", filepath.Join(root, "pid"))
		switch mode {
		case "explicit":
			_, err := jobs.Launch("child", BackgroundBashLaunch{Command: command, Dir: root, claudeOrphanRoot: root})
			if err != nil {
				os.Exit(2)
			}
			_ = os.WriteFile(filepath.Join(root, "launched"), []byte("yes"), 0600)
			select {}
		case "promoted":
			_, err := m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: command, timeout: 100 * time.Millisecond})
			if err != nil {
				os.Exit(2)
			}
			_ = os.WriteFile(filepath.Join(root, "launched"), []byte("yes"), 0600)
			select {}
		default:
			_, _ = m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: command, timeout: time.Minute})
		}
		os.Exit(0)
	}
	for _, mode := range []string{"foreground", "promoted", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestClaudePromotableBashRouterCrashRecovery$")
			cmd.Env = append(os.Environ(), "MECHA_BASH_CRASH_FIXTURE=1", "MECHA_BASH_CRASH_ROOT="+root, "MECHA_BASH_CRASH_MODE="+mode)
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			var pid int
			require.Eventually(t, func() bool {
				b, err := os.ReadFile(filepath.Join(root, "pid"))
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
				return err == nil && pid > 0
			}, 5*time.Second, 10*time.Millisecond)
			marker, err := processStartMarker(pid)
			require.NoError(t, err)
			id := ProcessIdentity{PID: pid, StartMarker: marker, Incarnation: "fixture"}
			t.Cleanup(func() {
				if (defaultProcessGroupOps{}).Probe(id).State == ProcessOwned {
					_ = (defaultProcessGroupOps{}).SignalVerifiedProcessGroup(id, syscall.SIGKILL)
				}
			})
			if mode != "foreground" {
				require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(root, "launched")); return err == nil }, 5*time.Second, 10*time.Millisecond)
			}
			records, err := os.ReadDir(filepath.Join(root, "claude-orphans"))
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.NoError(t, cmd.Process.Kill())
			_ = cmd.Wait()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, reapClaudeCodeOrphans(ctx, root))
			require.Eventually(t, func() bool { return (defaultProcessGroupOps{}).Probe(id).State == ProcessAbsent }, 2*time.Second, 10*time.Millisecond, "Claude shell survived router crash and orphan recovery")
			records, err = os.ReadDir(filepath.Join(root, "claude-orphans"))
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

func TestClaudeBashGatePreventsExecutionOnRecordFailure(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "claude-orphans"), []byte("block"), 0600))
	target := filepath.Join(root, "effect")
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	owner := embeddedOwner{identity: "claude-child", claudeOrphanRoot: root, backgroundBash: &backgroundBashOwnerCapability{}}
	m := &Manager{backgroundBash: jobs}
	_, err := m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: fmt.Sprintf("touch %q", target), timeout: time.Second})
	require.Error(t, err)
	require.NoFileExists(t, target)
	_, err = jobs.Launch("child", BackgroundBashLaunch{Command: fmt.Sprintf("touch %q", target), Dir: root, claudeOrphanRoot: root})
	require.Error(t, err)
	require.NoFileExists(t, target)
}

func TestClaudeBashRecordsRemovedAfterCompletion(t *testing.T) {
	for _, mode := range []string{"foreground", "promoted", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			jobs := NewBackgroundBashManager(nil)
			defer jobs.Close()
			owner := embeddedOwner{identity: "claude-child", claudeOrphanRoot: root, backgroundBash: &backgroundBashOwnerCapability{}}
			m := &Manager{backgroundBash: jobs}
			if mode == "explicit" {
				job, err := jobs.Launch("child", BackgroundBashLaunch{Command: "exit 0", Dir: root, claudeOrphanRoot: root})
				require.NoError(t, err)
				require.Eventually(t, func() bool { j, e := jobs.Get("child", job.ID); return e == nil && j.Status == "succeeded" }, 3*time.Second, 10*time.Millisecond)
			} else {
				command, timeout := "exit 0", time.Second
				if mode == "promoted" {
					command = "sleep 0.3"
					timeout = 50 * time.Millisecond
				}
				_, err := m.executePromotableBash(context.Background(), owner, root, embeddedBashCommand{command: command, timeout: timeout})
				require.NoError(t, err)
				if mode == "promoted" {
					require.Eventually(t, func() bool {
						j := jobs.List(backgroundBashOwnerKey(owner))
						return len(j) == 1 && j[0].Status == "succeeded"
					}, 3*time.Second, 10*time.Millisecond)
				}
			}
			entries, err := os.ReadDir(filepath.Join(root, "claude-orphans"))
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}
