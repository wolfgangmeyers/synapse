package life.meyers.mecha

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SduiModelsTest {

    @Test
    fun parsesBootstrapSurfaceWithRemoteSlots() {
        val json = """
            {
                "surface_id": "root",
                "title": "Mecha Workspace",
                "blocks": [
                    {
                        "type": "section",
                        "block_id": "root_header",
                        "text": "# MECHA WORKSPACE"
                    },
                    {
                        "type": "remote_slot",
                        "block_id": "sidebar_slot_block",
                        "slot_id": "sidebar_slot",
                        "view_url": "http://127.0.0.1:8765/v1/ui/views/sidebar"
                    },
                    {
                        "type": "divider",
                        "style": "glow"
                    },
                    {
                        "type": "remote_slot",
                        "block_id": "main_slot_block",
                        "slot_id": "main_slot",
                        "view_url": "http://127.0.0.1:8765/v1/ui/views/agents/default/conversation"
                    }
                ]
            }
        """.trimIndent()

        val surface = Surface.fromJson(json)
        assertEquals("root", surface.surfaceId)
        assertEquals("Mecha Workspace", surface.title)
        assertEquals(4, surface.blocks.size)

        val header = surface.blocks[0]
        assertEquals(BlockType.SECTION, header.type)
        assertEquals("root_header", header.blockId)
        assertEquals("# MECHA WORKSPACE", header.text)

        val sidebarSlot = surface.blocks[1]
        assertEquals(BlockType.REMOTE_SLOT, sidebarSlot.type)
        assertEquals("sidebar_slot", sidebarSlot.slotId)
        assertEquals("http://127.0.0.1:8765/v1/ui/views/sidebar", sidebarSlot.viewUrl)

        val divider = surface.blocks[2]
        assertEquals(BlockType.DIVIDER, divider.type)
        assertEquals("glow", divider.style)

        val mainSlot = surface.blocks[3]
        assertEquals(BlockType.REMOTE_SLOT, mainSlot.type)
        assertEquals("main_slot", mainSlot.slotId)
    }

    @Test
    fun parsesCardWithCollapsibleAndActions() {
        val json = """
            {
                "surface_id": "sidebar",
                "title": "Active Agents",
                "blocks": [
                    {
                        "type": "card",
                        "block_id": "peer_card_alpha",
                        "title": "Alpha Agent",
                        "subtitle": "Status: online",
                        "badge": "online",
                        "blocks": [
                            {
                                "type": "section",
                                "text": "Primary supervisor"
                            },
                            {
                                "type": "collapsible",
                                "block_id": "subagents_alpha",
                                "title": "2 Subagents",
                                "badge": "active",
                                "expanded": false,
                                "blocks": [
                                    {
                                        "type": "section",
                                        "text": "Subagent workers running"
                                    }
                                ]
                            },
                            {
                                "type": "actions",
                                "block_id": "peer_actions_alpha",
                                "elements": [
                                    {
                                        "type": "button",
                                        "action_id": "select_peer",
                                        "text": "Connect",
                                        "style": "primary",
                                        "value": "alpha"
                                    }
                                ]
                            }
                        ]
                    }
                ]
            }
        """.trimIndent()

        val surface = Surface.fromJson(json)
        assertEquals("sidebar", surface.surfaceId)
        val card = surface.blocks.first()
        assertEquals(BlockType.CARD, card.type)
        assertEquals("Alpha Agent", card.title)
        assertEquals("online", card.badge)
        assertEquals(3, card.blocks.size)

        val collapsible = card.blocks[1]
        assertEquals(BlockType.COLLAPSIBLE, collapsible.type)
        assertEquals("2 Subagents", collapsible.title)
        assertFalse(collapsible.expanded)
        assertEquals(1, collapsible.blocks.size)

        val actions = card.blocks[2]
        assertEquals(BlockType.ACTIONS, actions.type)
        val button = actions.elements.first()
        assertEquals(ElementType.BUTTON, button.type)
        assertEquals("select_peer", button.actionId)
        assertEquals("Connect", button.text)
        assertEquals("primary", button.style)
        assertEquals("alpha", button.value)
    }

    @Test
    fun parsesFormInputsWithDropdownSelectAndToggle() {
        val json = """
            {
                "surface_id": "config_alpha",
                "title": "Agent Configuration",
                "blocks": [
                    {
                        "type": "input",
                        "block_id": "cfg_name",
                        "label": "Agent Name",
                        "element": {
                            "type": "plain_text_input",
                            "action_id": "name_input",
                            "placeholder": "Enter agent name",
                            "initial_value": "alpha",
                            "multiline": false
                        }
                    },
                    {
                        "type": "input",
                        "block_id": "cfg_provider",
                        "label": "Model Provider",
                        "element": {
                            "type": "dropdown_select",
                            "action_id": "provider_select",
                            "initial_option": "google-vertex",
                            "options": [
                                {"text": "Google Vertex", "value": "google-vertex"},
                                {"text": "OpenAI Codex", "value": "openai-codex"}
                            ]
                        }
                    },
                    {
                        "type": "input",
                        "block_id": "cfg_auto_restart",
                        "label": "Auto Restart",
                        "element": {
                            "type": "toggle_switch",
                            "action_id": "auto_restart_toggle",
                            "initial_value": true,
                            "text": "Automatically restart on crash"
                        }
                    },
                    {
                        "type": "input",
                        "block_id": "cfg_attachments",
                        "label": "Upload Attachment",
                        "element": {
                            "type": "attachment_picker",
                            "action_id": "attachment_upload",
                            "upload_endpoint": "/v1/attachments"
                        }
                    }
                ]
            }
        """.trimIndent()

        val surface = Surface.fromJson(json)
        assertEquals(4, surface.blocks.size)

        val textInputBlock = surface.blocks[0]
        assertEquals("cfg_name", textInputBlock.blockId)
        assertEquals(ElementType.PLAIN_TEXT_INPUT, textInputBlock.element?.type)
        assertEquals("name_input", textInputBlock.element?.actionId)
        assertEquals("alpha", textInputBlock.element?.initialValue)

        val selectBlock = surface.blocks[1]
        assertEquals(ElementType.DROPDOWN_SELECT, selectBlock.element?.type)
        assertEquals(2, selectBlock.element?.options?.size)
        assertEquals("Google Vertex", selectBlock.element?.options?.get(0)?.text)
        assertEquals("google-vertex", selectBlock.element?.options?.get(0)?.value)

        val toggleBlock = surface.blocks[2]
        assertEquals(ElementType.TOGGLE_SWITCH, toggleBlock.element?.type)
        assertEquals(true, toggleBlock.element?.initialValue)

        val pickerBlock = surface.blocks[3]
        assertEquals(ElementType.ATTACHMENT_PICKER, pickerBlock.element?.type)
        assertEquals("/v1/attachments", pickerBlock.element?.uploadEndpoint)
    }

    @Test
    fun parsesImageBlockWithAspect() {
        val json = """
            {
                "type": "image",
                "block_id": "img_1",
                "image_url": "https://example.com/robot.png",
                "alt_text": "Robot Avatar",
                "aspect_ratio": "16:9"
            }
        """.trimIndent()

        val block = Block.fromJsonObject(JSONObject(json))
        assertEquals(BlockType.IMAGE, block.type)
        assertEquals("https://example.com/robot.png", block.imageUrl)
        assertEquals("Robot Avatar", block.altText)
        assertEquals("16:9", block.aspectRatio)
    }

    @Test
    fun serializesActionPayloadToStandardWireFormat() {
        val payload = ActionPayload(
            actionId = "send_message",
            blockId = "composer_input",
            agentId = "alpha",
            surfaceId = "conversation_alpha",
            value = "send",
            slotId = "main_slot",
            values = mapOf(
                "composer_input" to mapOf("message_text" to "Hello World"),
                "cfg_switches" to mapOf("sound" to true, "volume" to 80),
            )
        )

        val jsonStr = payload.toJson()
        val obj = JSONObject(jsonStr)

        assertEquals("send_message", obj.getString("action_id"))
        assertEquals("composer_input", obj.getString("block_id"))
        assertEquals("alpha", obj.getString("agent_id"))
        assertEquals("conversation_alpha", obj.getString("surface_id"))
        assertEquals("send", obj.getString("value"))
        assertEquals("main_slot", obj.getString("slot_id"))

        val stateObj = obj.getJSONObject("state")
        val valuesObj = stateObj.getJSONObject("values")
        assertEquals("Hello World", valuesObj.getJSONObject("composer_input").getString("message_text"))
        assertEquals(true, valuesObj.getJSONObject("cfg_switches").getBoolean("sound"))
        assertEquals(80, valuesObj.getJSONObject("cfg_switches").getInt("volume"))
    }

    @Test
    fun parsesActionResponseMutations() {
        // update_slot
        val slotRespJson = """
            {
                "response_action": "update_slot",
                "slot_id": "main_slot",
                "view_url": "/v1/ui/views/agents/beta/conversation"
            }
        """.trimIndent()
        val slotResp = ActionResponse.fromJson(slotRespJson)
        assertEquals(ResponseAction.UPDATE_SLOT, slotResp.responseAction)
        assertEquals("main_slot", slotResp.slotId)
        assertEquals("/v1/ui/views/agents/beta/conversation", slotResp.viewUrl)

        // clear_inputs
        val clearRespJson = """
            {
                "response_action": "clear_inputs",
                "clear_inputs": ["composer_input", "draft_block"]
            }
        """.trimIndent()
        val clearResp = ActionResponse.fromJson(clearRespJson)
        assertEquals(ResponseAction.CLEAR_INPUTS, clearResp.responseAction)
        assertEquals(listOf("composer_input", "draft_block"), clearResp.clearInputs)

        // errors
        val errorsRespJson = """
            {
                "response_action": "errors",
                "errors": {
                    "cfg_name": "Name is required",
                    "cfg_temp": "Temperature must be between 0 and 1"
                }
            }
        """.trimIndent()
        val errorsResp = ActionResponse.fromJson(errorsRespJson)
        assertEquals(ResponseAction.ERRORS, errorsResp.responseAction)
        assertEquals("Name is required", errorsResp.errors["cfg_name"])
        assertEquals("Temperature must be between 0 and 1", errorsResp.errors["cfg_temp"])

        // push
        val pushRespJson = """
            {
                "response_action": "push",
                "surface": {
                    "surface_id": "modal_1",
                    "title": "Modal Settings",
                    "blocks": []
                }
            }
        """.trimIndent()
        val pushResp = ActionResponse.fromJson(pushRespJson)
        assertEquals(ResponseAction.PUSH, pushResp.responseAction)
        assertNotNull(pushResp.surface)
        assertEquals("modal_1", pushResp.surface?.surfaceId)
    }
}
