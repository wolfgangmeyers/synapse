package life.meyers.mecha

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class ActionDispatcherTest {
    private lateinit var server: MockWebServer

    @Before
    fun setUp() {
        server = MockWebServer()
        server.start()
    }

    @After
    fun tearDown() {
        server.shutdown()
    }

    @Test
    fun resolvesRelativeAndAbsoluteUrls() {
        val state = SduiState()
        val slotController = SlotController()
        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = state,
            slotController = slotController,
            mainDispatcher = Dispatchers.Unconfined,
        )

        val relative = dispatcher.resolveUrl("/v1/ui/views/sidebar")
        assertTrue(relative.startsWith("http://"))
        assertTrue(relative.endsWith("/v1/ui/views/sidebar"))

        val absolute = dispatcher.resolveUrl("https://example.com/custom/view")
        assertEquals("https://example.com/custom/view", absolute)
    }

    @Test
    fun dispatchesActionAndHandlesUpdateSlot() = runBlocking {
        val state = SduiState()
        val slotController = SlotController()

        val responseJson = """
            {
                "response_action": "update_slot",
                "slot_id": "main_slot",
                "view_url": "/v1/ui/views/agents/beta/conversation"
            }
        """.trimIndent()

        server.enqueue(MockResponse().setResponseCode(200).setBody(responseJson))

        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = state,
            slotController = slotController,
            mainDispatcher = Dispatchers.Unconfined,
        )

        val payload = ActionPayload(
            actionId = "select_peer",
            blockId = "peer_beta",
            value = "beta",
            slotId = "main_slot"
        )

        val resp = dispatcher.dispatchAction(payload)
        assertEquals(ResponseAction.UPDATE_SLOT, resp.responseAction)
        assertEquals("main_slot", resp.slotId)
        assertEquals("/v1/ui/views/agents/beta/conversation", slotController.slotUrls["main_slot"])

        val recorded = server.takeRequest()
        assertEquals("/v1/ui/action", recorded.path)
        val requestBody = JSONObject(recorded.body.readUtf8())
        assertEquals("select_peer", requestBody.getString("action_id"))
        assertEquals("beta", requestBody.getString("value"))
    }

    @Test
    fun dispatchesActionAndHandlesClearInputs() = runBlocking {
        val state = SduiState()
        state.setValue("composer_input", "message_text", "Ready to send")
        val slotController = SlotController()

        val responseJson = """
            {
                "response_action": "clear_inputs",
                "clear_inputs": ["composer_input"]
            }
        """.trimIndent()

        server.enqueue(MockResponse().setResponseCode(200).setBody(responseJson))

        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = state,
            slotController = slotController,
            mainDispatcher = Dispatchers.Unconfined,
        )

        val payload = ActionPayload(
            actionId = "send_message",
            blockId = "composer_input",
            values = state.toSnapshot()
        )

        dispatcher.dispatchAction(payload)
        assertEquals(null, state.getValue("composer_input", "message_text"))
    }

    @Test
    fun dispatchesActionAndHandlesValidationErrors() = runBlocking {
        val state = SduiState()
        val slotController = SlotController()

        val responseJson = """
            {
                "response_action": "errors",
                "errors": {
                    "cfg_temp": "Value must be between 0.0 and 1.0"
                }
            }
        """.trimIndent()

        server.enqueue(MockResponse().setResponseCode(200).setBody(responseJson))

        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = state,
            slotController = slotController,
            mainDispatcher = Dispatchers.Unconfined,
        )

        val payload = ActionPayload(actionId = "save_config")
        dispatcher.dispatchAction(payload)

        assertEquals("Value must be between 0.0 and 1.0", state.getError("cfg_temp"))
    }

    @Test
    fun fetchesSurfaceFromEndpoint() = runBlocking {
        val surfaceJson = """
            {
                "surface_id": "remote_view",
                "title": "Hydrated View",
                "blocks": [
                    {
                        "type": "section",
                        "text": "Hello remote"
                    }
                ]
            }
        """.trimIndent()

        server.enqueue(MockResponse().setResponseCode(200).setBody(surfaceJson))

        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = SduiState(),
            slotController = SlotController(),
            mainDispatcher = Dispatchers.Unconfined,
        )

        val surface = dispatcher.fetchSurface("/v1/ui/views/test")
        assertEquals("remote_view", surface.surfaceId)
        assertEquals("Hydrated View", surface.title)
        assertEquals(1, surface.blocks.size)
    }

    @Test
    fun uploadsAttachmentMultipart() = runBlocking {
        val uploadResp = """
            {
                "attachment_id": "att_test_123",
                "thumbnail_url": "/v1/attachments/att_test_123/thumb"
            }
        """.trimIndent()

        server.enqueue(MockResponse().setResponseCode(200).setBody(uploadResp))

        val dispatcher = ActionDispatcher(
            baseUrl = server.url("/").toString(),
            state = SduiState(),
            slotController = SlotController(),
            mainDispatcher = Dispatchers.Unconfined,
        )

        val (attId, thumbUrl) = dispatcher.uploadAttachment(
            endpoint = "/v1/attachments",
            bytes = "test payload".toByteArray(),
            filename = "test.txt",
            mimeType = "text/plain"
        )

        assertEquals("att_test_123", attId)
        assertEquals("/v1/attachments/att_test_123/thumb", thumbUrl)

        val recorded = server.takeRequest()
        assertEquals("/v1/attachments", recorded.path)
        assertTrue(recorded.getHeader("Content-Type")?.contains("multipart/form-data") == true)
    }
}
