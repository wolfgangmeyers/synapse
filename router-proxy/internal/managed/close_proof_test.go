package managed

import (
	"context"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRealCloseProof(t *testing.T) {
	for _, claude := range []bool{false, true} {
		t.Run(map[bool]string{false: "Pi", true: "Claude"}[claude], func(t *testing.T) {
			if os.Getenv("MECHA_CLOSE_PROOF_CHILD") != "1" {
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRealCloseProof$/^"+t.Name()[len("TestRealCloseProof/"):]+"$", "-test.v")
				cmd.Env = append(os.Environ(), "MECHA_CLOSE_PROOF_CHILD=1", "MECHA_CLOSE_PROOF_DIR="+t.TempDir())
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("close proof subprocess timed out: %v\n%s", ctx.Err(), output)
				}
				if err != nil {
					t.Fatalf("close proof subprocess failed: %v\n%s", err, output)
				}
				if !strings.Contains(string(output), "--- PASS: "+t.Name()) {
					t.Fatalf("subprocess did not run proof: %s", output)
				}
				return
			}
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(os.Getenv("MECHA_CLOSE_PROOF_DIR"), "state.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			parent := subagentTestParent(t, db, "proof-parent")
			if claude {
				require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "claude-code", ID: "sonnet"}))
			}
			require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: "proof-child", ParentAgentID: parent.AgentID, Status: "queued"}))
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			m := makeSubagentTestManager(t, db, reg, 1)
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseBarrier := func() { releaseOnce.Do(func() { close(release) }) }
			m.subagentAdmissionTestHook = func() { close(entered); <-release }
			// Registered after the database/provider defers: always unblock and join
			// admission and Close before either dependency is torn down.
			admissionDone := make(chan struct{})
			closeDone := make(chan struct{})
			closeStarted := false
			defer func() {
				releaseBarrier()
				select {
				case <-admissionDone:
				case <-time.After(8 * time.Second):
					t.Log("cleanup: admission did not exit; terminating isolated process")
					os.Exit(1)
				}
				if !closeStarted {
					closeStarted = true
					go func() {
						c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						_ = m.Close(c)
						close(closeDone)
					}()
				}
				select {
				case <-closeDone:
				case <-time.After(8 * time.Second):
					t.Log("cleanup: Close did not exit; terminating isolated process")
					os.Exit(1)
				}
				// Rejected admission finalizes asynchronously outside startWG.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					rec, err := db.ManagedSubagent(context.Background(), parent, "proof-child")
					if err == nil && (rec.Status == "stopped" || rec.Status == "completed" || rec.Status == "failed") {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Log("cleanup: child did not settle; terminating isolated process")
				os.Exit(1)
			}()
			type outcome struct {
				r *SubagentSpawnResult
				e error
			}
			got := make(chan outcome, 1)
			go func() {
				defer close(admissionDone)
				r, e := m.admitSubagent(ctx, &subagentTask{parent: parent, childID: "proof-child", attempt: 1, status: "queued", claude: claude})
				got <- outcome{r, e}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("CAS barrier not reached")
			}
			done := make(chan error, 1)
			closeStarted = true
			go func() {
				defer close(closeDone)
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done <- m.Close(closeCtx)
			}()
			select {
			case <-m.runCtx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not cancel")
			}
			releaseBarrier()
			select {
			case o := <-got:
				if o.e == nil || o.r != nil {
					t.Errorf("admission = (%+v, %v), want nil result and closing error", o.r, o.e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admission blocked")
			}
			select {
			case e := <-done:
				require.NoError(t, e)
			case <-time.After(5 * time.Second):
				t.Fatal("Close blocked")
			}
			rec, e := db.ManagedSubagent(ctx, parent, "proof-child")
			require.NoError(t, e)
			require.Eventually(t, func() bool {
				rec, e = db.ManagedSubagent(ctx, parent, "proof-child")
				return e == nil && rec.Status == "stopped"
			}, 3*time.Second, 10*time.Millisecond)
		})
	}
}
