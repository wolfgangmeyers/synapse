package life.meyers.mecha

import okhttp3.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.net.URLEncoder
import java.util.UUID
import java.util.concurrent.TimeUnit

internal class AgentControlClient(baseUrl: String, client: OkHttpClient = OkHttpClient()) {
    private val base = baseUrl.trimEnd('/')
    private val http = client.newBuilder().callTimeout(15, TimeUnit.SECONDS).build()
    fun request(method: String, path: String, body: JSONObject? = null, callback: (Result<String>) -> Unit): Call {
        require(path.startsWith("/v1/"))
        val builder = Request.Builder().url(base + path)
        if (method != "GET") builder.header("X-Request-ID", UUID.randomUUID().toString())
        val payload = if (method in listOf("GET", "DELETE")) null else (body ?: JSONObject()).toString().toRequestBody("application/json".toMediaType())
        val call = http.newCall(builder.method(method, payload).build())
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: java.io.IOException) { callback(Result.failure(e)) }
            override fun onResponse(call: Call, response: Response) {
                callback(runCatching { response.use {
                    val source = it.body?.source() ?: error("Empty response")
                    if (source.request(1_048_577)) error("Router response is too large")
                    val text = source.readUtf8()
                    check(it.isSuccessful) { "Request failed (${it.code}): ${text.take(300)}" }
                    text
                } })
            }
        })
        return call
    }
    companion object {
        fun segment(value: String): String = URLEncoder.encode(value, "UTF-8").replace("+", "%20")
    }
}

internal fun workflowDefaults(workflow: JSONObject): Map<String, String> = buildMap {
    val variables = workflow.optJSONArray("variables") ?: return@buildMap
    for (i in 0 until variables.length()) {
        val v = variables.getJSONObject(i)
        put(v.getString("name"), v.optString("default_value", ""))
    }
}
internal fun workflowValidation(workflow: JSONObject, values: Map<String, String>): String? {
    if (workflow.optString("entry_state_id").let { it.isBlank() || it == "null" }) return "This workflow has no entry state."
    val variables = workflow.optJSONArray("variables") ?: return null
    for (i in 0 until variables.length()) {
        val v = variables.getJSONObject(i)
        if (v.optBoolean("required") && values[v.getString("name")].isNullOrBlank()) return "Enter ${v.optString("label", v.getString("name"))}."
    }
    return null
}

internal fun parseWorkflowDetail(body: String): JSONObject = JSONObject(body).getJSONObject("workflow")
internal fun parseWorkflowEntry(body: String): String = JSONObject(body).getJSONObject("workflow_state").getString("id")
internal fun parseInlinePermission(body: String): Boolean = JSONObject(body).getJSONObject("agent").getBoolean("allow_inline_tools")

internal fun validateAgentControlResponse(action: String, body: String) {
    if (action == "workflow") { org.json.JSONArray(body); return }
    val root = JSONObject(body)
    when (action) {
        "permission", "manage", "interrupt" -> root.getJSONObject("agent")
        "inline-tools" -> root.getBoolean("enabled")
        "tools" -> root.getJSONArray("tools")
        "monitors" -> root.getJSONArray("monitors")
        "children" -> root.getJSONArray("children")
        "scratchpad" -> root.getJSONArray("entries")
        "goal" -> root.getJSONObject("goal_state")
    }
}
