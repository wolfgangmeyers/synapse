package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Build an actual pre-124 database: final cleanup retains mail accepted after
// the termination cutoff, while removing the managed identity.
func TestAgentUrgencyMigrationRetainedDeletedIdentity(t *testing.T) {
	for _, evidence := range []string{"both", "action-only", "archive-only", "later-lease", "extant-managed"} {
		t.Run(evidence, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			clock := deterministic.NewFixedClock(now)
			path := filepath.Join(t.TempDir(), "upgrade.db")
			migrations := DefaultMigrations()
			old, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "124_agent_urgency")]})
			require.NoError(t, err)
			old.clock = clock
			t.Cleanup(func() {
				if !old.closed {
					require.NoError(t, old.Close())
				}
			})
			const target = "pre124-retired"
			urgencyLaunch(t, old, target, "pre124-launch")
			require.NoError(t, old.UpdateManagedAgentStarting(ctx, target, "session", ProcessIdentityRecord{PID: 42, StartMarker: "start", Incarnation: "incarnation"}, "generation"))
			require.NoError(t, old.MarkManagedReady(ctx, target))
			require.NoError(t, old.MarkManagedOperationTerminal(ctx, "pre124-launch"))
			agent, err := old.ManagedAgent(ctx, target)
			require.NoError(t, err)
			source := restartFenceFromAgent(agent)
			id := lifecycleUUID(970)
			op := ManagedOperation{OperationID: id, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + target, RequestID: id, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
			admission, err := old.ReserveManagedLifecycle(ctx, ManagedLifecycleReservation{AgentID: target, Kind: "terminate", Source: "direct", Method: op.Method, CanonicalPath: op.CanonicalPath, RequestID: id, CanonicalBodyHash: op.CanonicalBodyHash, Fence: source, Operation: op})
			require.NoError(t, err)
			for _, step := range [][4]string{{"terminating", "abort", "terminating", "terminate_reap"}, {"terminating", "terminate_reap", "cleanup", "direct_mail_retire"}, {"cleanup", "direct_mail_retire", "cleanup", "agent_cleanup"}, {"cleanup", "agent_cleanup", "cleanup", "durable_write"}} {
				require.NoError(t, old.AdvanceManagedTermination(ctx, id, step[0], step[1], step[2], step[3], &source))
			}
			_, err = old.Enqueue(ctx, protocol.Envelope{ID: lifecycleUUID(971), From: "sender", To: target, Body: "retained post-cutoff mail", CreatedAt: formatTimestamp(now)})
			require.NoError(t, err)
			require.NoError(t, old.CompleteManagedTermination(ctx, admission.Action.ActionID))
			var count int
			require.NoError(t, old.db.QueryRow(`SELECT count(*) FROM managed_agents WHERE agent_id=?`, target).Scan(&count))
			require.Zero(t, count)
			require.NoError(t, old.db.QueryRow(`SELECT count(*) FROM agents WHERE agent_id=?`, target).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, old.db.QueryRow(`SELECT count(*) FROM messages WHERE recipient=?`, target).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, old.db.QueryRow(`SELECT count(*) FROM managed_agent_archives WHERE agent_id=?`, target).Scan(&count))
			require.Equal(t, 1, count)
			switch evidence {
			case "action-only":
				_, err = old.db.Exec(`DELETE FROM managed_agent_archives`)
				require.NoError(t, err)
			case "archive-only":
				_, err = old.db.Exec(`DELETE FROM managed_http_operations WHERE operation_id=?`, id)
				require.NoError(t, err)
			case "later-lease":
				require.NoError(t, clock.Advance(time.Second))
				_, err = old.AcquireAgentLease(ctx, LeaseRequest{AgentID: target, InstanceID: "new", LeaseID: "new", LeaseDuration: time.Hour})
				require.NoError(t, err)
			case "extant-managed":
				urgencyLaunch(t, old, target, "pre124-readmission")
			}
			if evidence == "archive-only" {
				require.NoError(t, old.db.QueryRow(`SELECT count(*) FROM managed_lifecycle_actions WHERE agent_id=?`, target).Scan(&count))
				require.Zero(t, count)
			}
			_, err = old.db.Exec(`UPDATE messages SET expires_at=? WHERE recipient=?`, formatTimestamp(now.Add(60*24*time.Hour)), target)
			require.NoError(t, err)
			require.NoError(t, old.Close())
			s, err := Open(ctx, path, Options{})
			require.NoError(t, err)
			defer s.Close()
			s.clock = clock
			_, b, g, _, origin, _ := urgencyInvocationOnStore(t, s)
			userSeq, userDigest := origin.origin.userSeq, origin.origin.userDigest
			active := evidence == "later-lease" || evidence == "extant-managed"
			check := func(stage string) {
				t.Helper()
				saved, err := s.SetAgentUrgency(ctx, target, true)
				if active {
					require.NoError(t, err)
					require.True(t, saved)
				} else {
					assert.ErrorIs(t, err, ErrAgentNotFound, "public save")
					assert.False(t, saved)
				}
				call := providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true, "agent_id": target}, fmt.Sprintf("upgrade-%s", stage))
				receiptSDKAppend(t, s, b, *providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))
				w, err := s.RecordEmbeddedToolInvocation(ctx, b, g, userSeq, userDigest, "", call)
				require.NoError(t, err)
				require.NoError(t, s.StartEmbeddedToolInvocation(ctx, w, call))
				result, err := s.SetEmbeddedAgentUrgency(ctx, w)
				if active {
					require.NoError(t, err)
					require.True(t, result.Urgent)
				} else {
					require.ErrorIs(t, err, ErrAgentNotFound)
					require.False(t, ClaimEmbeddedToolHandoff(w))
					require.Empty(t, w.domainContent)
				}
			}
			check("upgraded")
			if active {
				return
			}
			later := now.Add(31 * 24 * time.Hour)
			s.clock = deterministic.NewFixedClock(later)
			require.NoError(t, s.runImmediate(ctx, func() error {
				if _, err := s.pruneStaleAgents(ctx, formatTimestamp(later)); err != nil {
					return err
				}
				if _, err := s.sweepManagedOperations(ctx, formatTimestamp(later)); err != nil {
					return err
				}
				_, err := pruneManagedArchivesTx(ctx, s.db, later)
				return err
			}))
			for _, table := range []string{"managed_lifecycle_actions", "managed_agent_archives"} {
				require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE agent_id=?`, target).Scan(&count))
				require.Zero(t, count)
			}
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM agents WHERE agent_id=?`, target).Scan(&count))
			require.Equal(t, 1, count)
			// Refresh only the caller's lease. Retired-target authority must remain the bit.
			_, err = s.AcquireAgentLease(ctx, LeaseRequest{AgentID: b.AgentID, InstanceID: "fixture", LeaseID: "fixture", LeaseDuration: time.Hour})
			require.NoError(t, err)
			// A new provider turn supplies current caller evidence after 31 days.
			user := receiptSDKAppend(t, s, b, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "retry target explicitly"}}})
			require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
			userSeq, userDigest = user.Sequence, user.Digest
			check("pruned")
			_, err = s.AcquireAgentLease(ctx, LeaseRequest{AgentID: target, InstanceID: "readmitted", LeaseID: "readmitted", LeaseDuration: time.Hour})
			require.NoError(t, err)
			var urgent bool
			require.NoError(t, s.db.QueryRow(`SELECT urgent FROM agents WHERE agent_id=?`, target).Scan(&urgent))
			require.False(t, urgent)
			active = true
			check("readmitted")
		})
	}
}
