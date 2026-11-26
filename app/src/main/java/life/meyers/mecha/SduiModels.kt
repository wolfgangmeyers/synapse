package life.meyers.mecha

import org.json.JSONArray
import org.json.JSONObject

/**
 * Generic Block Kit primitive block types.
 */
object BlockType {
    const val SECTION = "section"
    const val CARD = "card"
    const val COLLAPSIBLE = "collapsible"
    const val ACTIONS = "actions"
    const val INPUT = "input"
    const val IMAGE = "image"
    const val REMOTE_SLOT = "remote_slot"
    const val DIVIDER = "divider"
}

/**
 * Generic Block Kit element types.
 */
object ElementType {
    const val PLAIN_TEXT = "plain_text"
    const val MRKDWN = "mrkdwn"
    const val BUTTON = "button"
    const val PLAIN_TEXT_INPUT = "plain_text_input"
    const val TOGGLE_SWITCH = "toggle_switch"
    const val DROPDOWN_SELECT = "dropdown_select"
    const val ATTACHMENT_PICKER = "attachment_picker"
}

/**
 * Server action response mutation directives.
 */
object ResponseAction {
    const val UPDATE = "update"
    const val UPDATE_SLOT = "update_slot"
    const val PUSH = "push"
    const val CLEAR_INPUTS = "clear_inputs"
    const val ERRORS = "errors"
}

/**
 * Surface represents a top-level or modal screen composed of Block Kit blocks.
 */
data class Surface(
    val surfaceId: String,
    val title: String? = null,
    val blocks: List<Block> = emptyList(),
) {
    companion object {
        fun fromJson(jsonStr: String): Surface = fromJsonObject(JSONObject(jsonStr))

        fun fromJsonObject(obj: JSONObject): Surface {
            val surfaceId = obj.optString("surface_id", "surface")
            val title = if (obj.has("title") && !obj.isNull("title")) obj.getString("title") else null
            val blocksArray = obj.optJSONArray("blocks") ?: JSONArray()
            val blocks = mutableListOf<Block>()
            for (i in 0 until blocksArray.length()) {
                val blockObj = blocksArray.optJSONObject(i) ?: continue
                blocks.add(Block.fromJsonObject(blockObj))
            }
            return Surface(surfaceId = surfaceId, title = title, blocks = blocks)
        }
    }
}

/**
 * Field represents a key-value text pair inside a section block.
 */
data class Field(
    val title: String,
    val value: String,
) {
    companion object {
        fun fromJsonObject(obj: JSONObject): Field = Field(
            title = obj.optString("title", ""),
            value = obj.optString("value", ""),
        )
    }
}

/**
 * SelectOption represents an item in a dropdown select element.
 */
data class SelectOption(
    val text: String,
    val value: String,
) {
    companion object {
        fun fromJsonObject(obj: JSONObject): SelectOption = SelectOption(
            text = obj.optString("text", ""),
            value = obj.optString("value", ""),
        )
    }
}

/**
 * Element represents an interactive or display widget inside a block or accessory slot.
 */
data class Element(
    val type: String,
    val actionId: String = "",
    val placeholder: String? = null,
    val initialValue: Any? = null,
    val multiline: Boolean = false,
    val text: String? = null,
    val style: String? = null,
    val value: String? = null,
    val options: List<SelectOption> = emptyList(),
    val initialOption: String? = null,
    val uploadEndpoint: String? = null,
) {
    companion object {
        fun fromJsonObject(obj: JSONObject): Element {
            val type = obj.optString("type", ElementType.PLAIN_TEXT)
            val actionId = obj.optString("action_id", "")
            val placeholder = if (obj.has("placeholder") && !obj.isNull("placeholder")) {
                val pObj = obj.optJSONObject("placeholder")
                if (pObj != null) pObj.optString("text", "")
                else obj.optString("placeholder", "")
            } else null
            val initialValue = if (obj.has("initial_value") && !obj.isNull("initial_value")) obj.get("initial_value") else null
            val multiline = obj.optBoolean("multiline", false)
            val text = if (obj.has("text") && !obj.isNull("text")) {
                val tObj = obj.optJSONObject("text")
                if (tObj != null) tObj.optString("text", "")
                else obj.optString("text", "")
            } else null
            val style = if (obj.has("style") && !obj.isNull("style")) obj.getString("style") else null
            val value = if (obj.has("value") && !obj.isNull("value")) obj.getString("value") else null
            val initialOption = if (obj.has("initial_option") && !obj.isNull("initial_option")) obj.getString("initial_option") else null
            val uploadEndpoint = if (obj.has("upload_endpoint") && !obj.isNull("upload_endpoint")) obj.getString("upload_endpoint") else null

            val optionsList = mutableListOf<SelectOption>()
            val optionsArr = obj.optJSONArray("options")
            if (optionsArr != null) {
                for (i in 0 until optionsArr.length()) {
                    val optObj = optionsArr.optJSONObject(i) ?: continue
                    optionsList.add(SelectOption.fromJsonObject(optObj))
                }
            }

            return Element(
                type = type,
                actionId = actionId,
                placeholder = placeholder,
                initialValue = initialValue,
                multiline = multiline,
                text = text,
                style = style,
                value = value,
                options = optionsList,
                initialOption = initialOption,
                uploadEndpoint = uploadEndpoint,
            )
        }
    }
}

/**
 * Block represents a generic Block Kit layout primitive node.
 */
data class Block(
    val type: String,
    val blockId: String? = null,
    val text: String? = null,
    val fields: List<Field> = emptyList(),
    val accessory: Element? = null,
    val title: String? = null,
    val subtitle: String? = null,
    val badge: String? = null,
    val expanded: Boolean = false,
    val style: String? = null,
    val blocks: List<Block> = emptyList(),
    val elements: List<Element> = emptyList(),
    val label: String? = null,
    val element: Element? = null,
    val hint: String? = null,
    val optional: Boolean = false,
    val imageUrl: String? = null,
    val altText: String? = null,
    val aspectRatio: String? = null,
    val thumbnailUrl: String? = null,
    val slotId: String? = null,
    val viewUrl: String? = null,
) {
    companion object {
        fun fromJsonObject(obj: JSONObject): Block {
            val type = obj.optString("type", BlockType.SECTION)
            val blockId = if (obj.has("block_id") && !obj.isNull("block_id")) obj.getString("block_id") else null
            val text = if (obj.has("text") && !obj.isNull("text")) {
                val tObj = obj.optJSONObject("text")
                if (tObj != null) tObj.optString("text", "")
                else obj.optString("text", "")
            } else null
            val title = if (obj.has("title") && !obj.isNull("title")) {
                val tObj = obj.optJSONObject("title")
                if (tObj != null) tObj.optString("text", "")
                else obj.optString("title", "")
            } else null
            val subtitle = if (obj.has("subtitle") && !obj.isNull("subtitle")) obj.getString("subtitle") else null
            val badge = if (obj.has("badge") && !obj.isNull("badge")) {
                obj.getString("badge")
            } else if (obj.has("status_badge") && !obj.isNull("status_badge")) {
                obj.getString("status_badge")
            } else null
            val expanded = if (obj.has("expanded")) {
                obj.optBoolean("expanded", false)
            } else {
                obj.optBoolean("default_expanded", false)
            }
            val style = if (obj.has("style") && !obj.isNull("style")) obj.getString("style") else null
            val label = if (obj.has("label") && !obj.isNull("label")) {
                val lObj = obj.optJSONObject("label")
                if (lObj != null) lObj.optString("text", "")
                else obj.optString("label", "")
            } else null
            val hint = if (obj.has("hint") && !obj.isNull("hint")) obj.getString("hint") else null
            val optional = obj.optBoolean("optional", false)
            val imageUrl = if (obj.has("image_url") && !obj.isNull("image_url")) obj.getString("image_url") else null
            val altText = if (obj.has("alt_text") && !obj.isNull("alt_text")) obj.getString("alt_text") else null
            val aspectRatio = if (obj.has("aspect_ratio") && !obj.isNull("aspect_ratio")) obj.getString("aspect_ratio") else null
            val thumbnailUrl = if (obj.has("thumbnail_url") && !obj.isNull("thumbnail_url")) obj.getString("thumbnail_url") else null
            val slotId = if (obj.has("slot_id") && !obj.isNull("slot_id")) obj.getString("slot_id") else null
            val viewUrl = if (obj.has("view_url") && !obj.isNull("view_url")) {
                obj.getString("view_url")
            } else if (obj.has("endpoint") && !obj.isNull("endpoint")) {
                obj.getString("endpoint")
            } else null

            val fields = mutableListOf<Field>()
            val fieldsArr = obj.optJSONArray("fields")
            if (fieldsArr != null) {
                for (i in 0 until fieldsArr.length()) {
                    val fieldObj = fieldsArr.optJSONObject(i) ?: continue
                    fields.add(Field.fromJsonObject(fieldObj))
                }
            }

            val accessory = if (obj.has("accessory") && !obj.isNull("accessory")) {
                Element.fromJsonObject(obj.getJSONObject("accessory"))
            } else null

            val childBlocks = mutableListOf<Block>()
            val blocksArr = obj.optJSONArray("blocks")
            if (blocksArr != null) {
                for (i in 0 until blocksArr.length()) {
                    val childObj = blocksArr.optJSONObject(i) ?: continue
                    childBlocks.add(fromJsonObject(childObj))
                }
            }

            val elementsList = mutableListOf<Element>()
            val elementsArr = obj.optJSONArray("elements")
            if (elementsArr != null) {
                for (i in 0 until elementsArr.length()) {
                    val elemObj = elementsArr.optJSONObject(i) ?: continue
                    elementsList.add(Element.fromJsonObject(elemObj))
                }
            }

            val childElement = if (obj.has("element") && !obj.isNull("element")) {
                Element.fromJsonObject(obj.getJSONObject("element"))
            } else null

            return Block(
                type = type,
                blockId = blockId,
                text = text,
                fields = fields,
                accessory = accessory,
                title = title,
                subtitle = subtitle,
                badge = badge,
                expanded = expanded,
                style = style,
                blocks = childBlocks,
                elements = elementsList,
                label = label,
                element = childElement,
                hint = hint,
                optional = optional,
                imageUrl = imageUrl,
                altText = altText,
                aspectRatio = aspectRatio,
                thumbnailUrl = thumbnailUrl,
                slotId = slotId,
                viewUrl = viewUrl,
            )
        }
    }
}

/**
 * ActionPayload encapsulates interaction dispatches sent to /v1/ui/action.
 */
data class ActionPayload(
    val actionId: String,
    val blockId: String? = null,
    val agentId: String? = null,
    val surfaceId: String? = null,
    val value: String? = null,
    val slotId: String? = null,
    val values: Map<String, Map<String, Any>> = emptyMap(),
) {
    fun toJson(): String {
        val root = JSONObject()
        root.put("action_id", actionId)
        blockId?.let { root.put("block_id", it) }
        agentId?.let { root.put("agent_id", it) }
        surfaceId?.let { root.put("surface_id", it) }
        value?.let { root.put("value", it) }
        slotId?.let { root.put("slot_id", it) }

        if (values.isNotEmpty()) {
            val stateObj = JSONObject()
            val valuesObj = JSONObject()
            values.forEach { (bId, actions) ->
                val actionObj = JSONObject()
                actions.forEach { (aId, aVal) ->
                    actionObj.put(aId, aVal)
                }
                valuesObj.put(bId, actionObj)
            }
            stateObj.put("values", valuesObj)
            root.put("state", stateObj)
        }
        return root.toString()
    }
}

/**
 * ActionResponse represents the server's mutation reply from /v1/ui/action.
 */
data class ActionResponse(
    val responseAction: String,
    val surface: Surface? = null,
    val slotId: String? = null,
    val viewUrl: String? = null,
    val clearInputs: List<String> = emptyList(),
    val errors: Map<String, String> = emptyMap(),
) {
    companion object {
        fun fromJson(jsonStr: String): ActionResponse = fromJsonObject(JSONObject(jsonStr))

        fun fromJsonObject(obj: JSONObject): ActionResponse {
            val responseAction = obj.optString("response_action", ResponseAction.UPDATE)
            val surface = if (obj.has("surface") && !obj.isNull("surface")) {
                Surface.fromJsonObject(obj.getJSONObject("surface"))
            } else null
            val slotId = if (obj.has("slot_id") && !obj.isNull("slot_id")) obj.getString("slot_id") else null
            val viewUrl = if (obj.has("view_url") && !obj.isNull("view_url")) obj.getString("view_url") else null

            val clearInputs = mutableListOf<String>()
            val clearArr = obj.optJSONArray("clear_inputs")
            if (clearArr != null) {
                for (i in 0 until clearArr.length()) {
                    clearInputs.add(clearArr.optString(i, ""))
                }
            }

            val errors = mutableMapOf<String, String>()
            val errorsObj = obj.optJSONObject("errors")
            if (errorsObj != null) {
                val keys = errorsObj.keys()
                while (keys.hasNext()) {
                    val k = keys.next()
                    errors[k] = errorsObj.optString(k, "")
                }
            }

            return ActionResponse(
                responseAction = responseAction,
                surface = surface,
                slotId = slotId,
                viewUrl = viewUrl,
                clearInputs = clearInputs,
                errors = errors,
            )
        }
    }
}
