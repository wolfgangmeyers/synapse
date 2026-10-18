package life.meyers.mecha

import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class ManagedAgentClientTest {
    private lateinit var server: MockWebServer
    private lateinit var client: ManagedAgentClient

    @Before
    fun setUp() {
        server = MockWebServer()
        server.start()
        val routerUrl = server.url("").toString().removeSuffix("/")
        client = ManagedAgentClient(OkHttpClient(), routerBaseUrlProvider = { routerUrl })
    }

    @After
    fun tearDown() {
        server.shutdown()
    }

    @Test
    fun checkWorktreeParsesResponseCorrectly() {
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setBody("""{"is_git_repo":true,"is_main_worktree":true,"has_uncommitted_changes":false}""")
        )

        val latch = CountDownLatch(1)
        var git = false
        var main = false
        var uncommitted = true

        client.checkWorktree("/Users/test/repo") { isGit, isMain, hasUncommitted ->
            git = isGit
            main = isMain
            uncommitted = hasUncommitted
            latch.countDown()
        }

        assertTrue(latch.await(2, TimeUnit.SECONDS))
        assertTrue(git)
        assertTrue(main)
        assertFalse(uncommitted)

        val recorded = server.takeRequest()
        assertEquals("/v1/git/worktree-check?path=%2FUsers%2Ftest%2Frepo", recorded.path)
        assertEquals("GET", recorded.method)
    }

    @Test
    fun fetchModelsParsesDefaultAndAvailableModels() {
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setBody("""
                    {
                        "default_model": {"provider": "google-vertex", "id": "gemini-3.8-flash", "name": "Gemini 3.8 Flash"},
                        "models": [
                            {"provider": "google-vertex", "id": "gemini-3.8-flash", "available": true},
                            {"provider": "openai-codex", "id": "gpt-5", "available": false}
                        ]
                    }
                """.trimIndent())
        )

        val latch = CountDownLatch(1)
        var defaultModel: ModelSummary? = null
        var models: List<ModelSummary> = emptyList()

        client.fetchModels { defModel, mList ->
            defaultModel = defModel
            models = mList
            latch.countDown()
        }

        assertTrue(latch.await(2, TimeUnit.SECONDS))
        assertNotNull(defaultModel)
        assertEquals("google-vertex", defaultModel?.provider)
        assertEquals("gemini-3.8-flash", defaultModel?.id)
        assertEquals(1, models.size)
        assertEquals("gemini-3.8-flash", models.first().id)
    }

    @Test
    fun fetchDirectoriesParsesCurrentParentAndDirectories() {
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setBody("""
                    {
                        "path": "/Users/test",
                        "parent": "/Users",
                        "breadcrumbs": [{"name": "Users", "path": "/Users"}, {"name": "test", "path": "/Users/test"}],
                        "directories": [{"name": "code", "path": "/Users/test/code"}]
                    }
                """.trimIndent())
        )

        val latch = CountDownLatch(1)
        var cur = ""
        var parent = ""
        var bcCount = 0
        var dirCount = 0

        client.fetchDirectories("/Users/test") { currentPath, parentPath, breadcrumbs, dirs ->
            cur = currentPath
            parent = parentPath
            bcCount = breadcrumbs.size
            dirCount = dirs.size
            latch.countDown()
        }

        assertTrue(latch.await(2, TimeUnit.SECONDS))
        assertEquals("/Users/test", cur)
        assertEquals("/Users", parent)
        assertEquals(2, bcCount)
        assertEquals(1, dirCount)
    }

    @Test
    fun launchManagedAgentSendsCorrectPayloadAndHeaders() {
        server.enqueue(
            MockResponse()
                .setResponseCode(202)
                .setBody("""{"operation_id":"op-1"}""")
        )

        val latch = CountDownLatch(1)
        var success = false
        var error: String? = null

        client.launchManagedAgent(
            name = "worker alpha",
            cwd = "/path/to/project",
            model = ModelSummary("google-vertex", "gemini-3.8-flash"),
            instruction = "start work",
            worktree = true,
            worktreeBranch = "worker-alpha",
        ) { ok, err ->
            success = ok
            error = err
            latch.countDown()
        }

        assertTrue(latch.await(2, TimeUnit.SECONDS))
        assertTrue(success)
        assertNull(error)

        val recorded = server.takeRequest()
        assertEquals("/v1/managed-agents", recorded.path)
        assertEquals("POST", recorded.method)
        assertNotNull(recorded.getHeader("X-Request-ID"))
        assertEquals("application/json; charset=utf-8", recorded.getHeader("Content-Type"))

        val body = JSONObject(recorded.body.readUtf8())
        assertEquals("worker-alpha", body.getString("name"))
        assertEquals("/path/to/project", body.getString("cwd"))
        assertEquals("embedded", body.getString("runtime"))
        assertTrue(body.getBoolean("worktree"))
        assertEquals("worker-alpha", body.getString("worktree_branch"))
        assertEquals("start work", body.getString("initial_instruction"))
        assertEquals("google-vertex", body.getJSONObject("model").getString("provider"))
    }

    @Test
    fun terminateManagedAgentSendsZeroByteBodyAndDeleteMethod() {
        server.enqueue(
            MockResponse()
                .setResponseCode(202)
                .setBody("""{"operation_id":"op-term"}""")
        )

        val latch = CountDownLatch(1)
        var success = false

        client.terminateManagedAgent("agent-to-delete", cleanupWorktree = true) { ok, _ ->
            success = ok
            latch.countDown()
        }

        assertTrue(latch.await(2, TimeUnit.SECONDS))
        assertTrue(success)

        val recorded = server.takeRequest()
        assertEquals("/v1/managed-agents/agent-to-delete?cleanup_worktree=true", recorded.path)
        assertEquals("DELETE", recorded.method)
        assertNotNull(recorded.getHeader("X-Request-ID"))
        assertEquals(0L, recorded.bodySize)
    }
}
