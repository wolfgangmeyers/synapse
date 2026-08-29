package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LifecycleParityTest {
    @Test
    fun capabilityDiscoveryUsesOneSecondBoundAndFailsClosedOnTimeout() {
        assertEquals(1_000L, CAPABILITY_DISCOVERY_TIMEOUT_MS)
        val discovered = ControlCapabilities(goalControlV1 = true)
        assertEquals(discovered, capabilityDiscoveryFallback(discovered))
        assertEquals(ControlCapabilities(), capabilityDiscoveryFallback(null))
    }

    @Test
    fun capabilityAndScopeChecksFailClosedForMissingOrStaleFetch() {
        assertFalse(parseCapabilities(CapabilityData()).subagentLifecycleV2)
        assertTrue(parseCapabilities(CapabilityData(subagentLifecycleV2 = true)).subagentLifecycleV2)
        assertFalse(lifecycleScopeMatches("router-a", "router-a", 3, 4, "android", "android", "instance", "instance", "lease", "lease"))
        assertFalse(lifecycleScopeMatches("router-a", "router-b", 3, 3, "android", "android", "instance", "instance", "lease", "lease"))
        assertFalse(lifecycleScopeMatches("router-a", "router-a", 3, 3, "android", "android", "instance", "instance", "old", "lease"))
        assertTrue(lifecycleScopeMatches("router-a", "router-a", 3, 3, "android", "android", "instance", "instance", "lease", "lease"))
    }

    @Test
    fun syncFrameRequiresCurrentLeaseAndUsesExactOutgoingFields() {
        val frame = buildSubagentSyncEnableFrameData("subagent-enable-1", "android", "instance", "lease", 8)
        assertEquals(setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "after_version"), frame.wireFields.keys)
        assertEquals("subagent_sync_enable", frame.wireFields["type"])
        assertEquals(8L, frame.afterVersion)
        assertTrue(lifecycleScopeMatches("router", "router", 1, 1, "android", "android", "instance", "instance", "lease", "lease"))
        assertFalse(lifecycleScopeMatches("router", "router", 1, 1, "android", "android", "instance", "instance", "lease", "bad-lease"))
    }

    @Test
    fun snapshotUpdateAndReplayRejectStaleVersionsAndAllowReplayGaps() {
        val peers = listOf(PeerStatus("worker"))
        val first = LifecycleAgentUpdate("worker", 2, 1, listOf(SubagentSummary("child", "First")))
        val stale = LifecycleAgentUpdate("worker", 1, 0, emptyList())
        val current = upsertLifecycleAgent(peers, first, "android")
        assertEquals(2L, current.single().subagentVersion)
        assertEquals(current, upsertLifecycleAgent(current, stale, "android"))

        val replay = applyLifecycleReplay(
            current,
            listOf(LifecycleAgentUpdate("worker", 4, 1, listOf(SubagentSummary("child", "Fourth"))), LifecycleAgentUpdate("worker", 6, 0, emptyList())),
            routerVersion = 9,
            lastVersion = 2,
            localAgentId = "android",
        )
        assertEquals(9L, replay.second)
        assertEquals(0, replay.first.single().subagentCount)
        assertEquals(6L, replay.first.single().subagentVersion)

        val oldSnapshot = applyLifecycleSnapshot(replay.first, emptyList(), 8, replay.second, "android")
        assertEquals(9L, oldSnapshot.lastSnapshotVersion)
        assertEquals(replay.first, oldSnapshot.peers)
    }

    @Test
    fun snapshotClearsOnlyDurablePeersAtOrBelowSnapshotVersion() {
        val peers = listOf(
            PeerStatus("worker", subagentCount = 1, subagents = listOf(SubagentSummary("child", "Child")), subagentVersion = 3),
            PeerStatus("legacy", subagentCount = 1, subagents = listOf(SubagentSummary("child", "Child"))),
        )
        val result = applyLifecycleSnapshot(peers, emptyList(), 4, 0, "android")
        assertEquals(0, result.peers.first { it.id == "worker" }.subagentCount)
        assertEquals(1, result.peers.first { it.id == "legacy" }.subagentCount)
    }

    @Test
    fun unknownLifecycleFrameAndMalformedLeaseAreIgnored() {
        assertNull(parseLifecycleStateFrame(LifecycleStateFrameData("v1", "frame", "subagent_lifecycle_event", 1)))
        assertNull(parseLifecycleStateFrame(LifecycleStateFrameData("v1", "frame", "subagent_state_update", 1, agent = null)))
        assertNull(parseMessageArrivalFrame(MessageArrivalFrameData("v1", type = "message_arrival"), "android"))
    }

    @Test
    fun arrivalsAreDeduplicatedBoundedFilteredAndFullyDetailed() {
        val first = parseMessageArrivalFrame(arrival("worker", "discord", "message-1", "Build status"), "android")!!
        val replacement = parseMessageArrivalFrame(arrival("worker", "reminder", "message-2", "Reminder status"), "android")!!
        val parsed = replacement
        assertNotNull(parsed)
        val one = recordMessageArrival(emptyMap(), first)
        val two = recordMessageArrival(one, replacement)
        assertEquals(1, two.size)
        assertEquals("message-2", two.getValue("worker").messageId)
        assertEquals(listOf("worker"), visibleMessageArrivals(two, "reminder").map { it.agent })
        assertTrue(visibleMessageArrivals(two, "status").isNotEmpty())

        val bounded = (0..1_000).map { parseMessageArrivalFrame(arrival("worker-$it", "agent", "message-$it", "subject"), "android")!! }
            .fold(emptyMap<String, MessageArrival>(), ::recordMessageArrival)
        assertEquals(999, bounded.size)
        assertFalse(bounded.containsKey("worker-0"))
    }

    private fun arrival(agent: String, kind: String, id: String, subject: String): MessageArrivalFrameData = MessageArrivalFrameData(
        version = "v1",
        frameId = "arrival-$id",
        type = "message_arrival",
        agentId = agent,
        from = "sender",
        sourceKind = kind,
        subject = subject,
        subjectPresent = true,
        preview = "preview",
        body = "full body",
        createdAt = "2026-08-03T00:00:00.000Z",
        messageId = id,
    )
}
