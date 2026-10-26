package life.meyers.mecha

import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class AgentControlClientTest {
    @Test fun selectedAgentRequestsCrossRealHttpWithUuidAndExactBodies() {
        val server = MockWebServer(); server.start()
        try {
            val client = AgentControlClient(server.url("/").toString())
            val operations = listOf(
                Triple("PUT", "workflow-state", JSONObject().put("action", "activate").put("workflow_id", "flow").put("variables", JSONObject().put("name", "line\nsecond"))),
                Triple("PUT", "workflow-state", JSONObject().put("action", "deactivate")),
                Triple("POST", "allow-inline-tools", JSONObject().put("allow_inline_tools", true)),
                Triple("PUT", "inline-tools", JSONObject().put("enabled", false)),
                Triple("PUT", "goal", JSONObject().put("action", "set").put("goal", "Complete task")),
                Triple("PUT", "tools", JSONObject().put("category", "worker").put("enabled", false)),
                Triple("POST", "restart", JSONObject()), Triple("POST", "reset", JSONObject()), Triple("POST", "interrupt", JSONObject()),
                Triple("POST", "subagents/child/stop", JSONObject()),
            )
            for ((method, action, body) in operations) {
                server.enqueue(MockResponse().setBody("{\"ok\":true}"))
                val latch = CountDownLatch(1); var result: Result<String>? = null
                client.request(method, "/v1/managed-agents/worker/$action", body) { result = it; latch.countDown() }
                assertTrue(latch.await(5, TimeUnit.SECONDS)); assertTrue(result!!.isSuccess)
                val request = server.takeRequest(5, TimeUnit.SECONDS)!!
                assertEquals(method, request.method); assertEquals("/v1/managed-agents/worker/$action", request.path)
                UUID.fromString(request.getHeader("X-Request-ID"))
                assertEquals(body.toString(), request.body.readUtf8())
            }
        } finally { server.shutdown() }
    }
    @Test fun readMonitorCrudFailureAndOversizedResponsesAreHonest() {
        val server = MockWebServer(); server.start()
        try {
            val client = AgentControlClient(server.url("/").toString())
            val cases = listOf(
                Triple("GET", "/v1/workflows", true), Triple("GET", "/v1/managed-agents/a/scratchpad?limit=100", true),
                Triple("GET", "/v1/managed-agents/a/children", true), Triple("GET", "/v1/agents/a/progress/history", true),
                Triple("GET", "/v1/agents/a/monitors", true), Triple("POST", "/v1/agents/a/monitors", true),
                Triple("PUT", "/v1/agents/a/monitors/m", true), Triple("POST", "/v1/agents/a/monitors/m/evaluate", true),
                Triple("DELETE", "/v1/agents/a/monitors/m", true), Triple("PUT", "/v1/managed-agents/a/tools", false),
            )
            for ((method, path, ok) in cases) {
                server.enqueue(MockResponse().setResponseCode(if (ok) 200 else 409).setBody(if (ok) "{}" else "conflict"))
                val latch = CountDownLatch(1); var result: Result<String>? = null
                client.request(method, path, JSONObject()) { result = it; latch.countDown() }
                assertTrue(latch.await(5, TimeUnit.SECONDS)); assertEquals(ok, result!!.isSuccess)
                val request = server.takeRequest()!!; assertEquals(path, request.path)
                if (method == "GET") assertNull(request.getHeader("X-Request-ID"))
            }
            server.enqueue(MockResponse().setBody("x".repeat(1_048_577)))
            val latch = CountDownLatch(1); var failure: Throwable? = null
            client.request("GET", "/v1/workflows") { failure = it.exceptionOrNull(); latch.countDown() }
            assertTrue(latch.await(5, TimeUnit.SECONDS)); assertTrue(failure!!.message!!.contains("too large"))
            assertEquals("a%2Fb%20c", AgentControlClient.segment("a/b c"))
        } finally { server.shutdown() }
    }
}
