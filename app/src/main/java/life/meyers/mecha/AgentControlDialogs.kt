package life.meyers.mecha

import android.os.Handler
import android.os.Looper
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import okhttp3.Call
import org.json.JSONArray
import org.json.JSONObject

@Composable
internal fun AgentControlsDialog(baseUrl: String, agentId: String, action: String,
    onDismiss: () -> Unit, onTerminate: () -> Unit = {}) {
    val client = remember(baseUrl, agentId) { AgentControlClient(baseUrl) }
    val agentPath = "/v1/managed-agents/${AgentControlClient.segment(agentId)}"
    val monitorPath = "/v1/agents/${AgentControlClient.segment(agentId)}/monitors"
    val path = when (action) {
        "workflow" -> "/v1/workflows"
        "permission", "manage", "interrupt" -> agentPath
        "monitors" -> monitorPath
        "history" -> "/v1/agents/${AgentControlClient.segment(agentId)}/progress/history"
        "scratchpad" -> "$agentPath/scratchpad?limit=100"
        else -> "$agentPath/$action"
    }
    var data by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var resultText by remember { mutableStateOf<String?>(null) }
    var pending by remember { mutableStateOf(false) }
    var revision by remember { mutableIntStateOf(0) }
    var goal by remember { mutableStateOf("") }
    var alive by remember { mutableStateOf(true) }
    val calls = remember { mutableListOf<Call>() }
    val handler = remember { Handler(Looper.getMainLooper()) }
    DisposableEffect(Unit) { onDispose { alive = false; calls.forEach { it.cancel() }; calls.clear() } }
    val request: (String, String, JSONObject?, (String) -> Unit) -> Unit = { method, url, body, success ->
        if (!pending && alive) {
            pending = true; error = null
            val call = client.request(method, url, body) { response -> handler.post {
                if (alive) {
                    pending = false
                    calls.removeAll { it.isExecuted() }
                    response.fold(onSuccess = { text ->
                        runCatching { success(text) }.onFailure { error = it.message ?: "Invalid router response." }
                        if (method != "GET") revision++
                    }, onFailure = { error = it.message ?: "Request failed." })
                }
            } }
            calls.removeAll { it.isExecuted() && (it.isCanceled()) }
            calls.add(call)
        }
    }
    LaunchedEffect(revision) {
        request("GET", path, null) { body ->
            validateAgentControlResponse(action, body)
            data = body
            if (action == "goal") {
                val root = JSONObject(body)
                goal = (root.optJSONObject("goal_state") ?: root).optString("goal")
            }
            if (action == "workflow") request("GET", "$agentPath/workflow-state", null) { status = it }
        }
    }
    AlertDialog(containerColor = MaterialTheme.colorScheme.surface, titleContentColor = MaterialTheme.colorScheme.onSurface, textContentColor = MaterialTheme.colorScheme.onSurface,
        onDismissRequest = { if (!pending) onDismiss() },
        title = { Text(when (action) { "permission" -> "Inline permission"; "inline-tools" -> "Inline tool selection"; "manage" -> "Manage worker"; else -> action.replaceFirstChar { it.uppercase() } }) },
        text = {
            Column(Modifier.fillMaxWidth().heightIn(max = 440.dp).imePadding().verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (pending) LinearProgressIndicator(Modifier.fillMaxWidth())
                error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
                resultText?.let { Text(it.take(600))
                    TextButton(onClick = { resultText = null }) { Text("Dismiss result") } }
                if (data.isNotBlank()) {
                    when (action) {
                        "workflow" -> WorkflowLaunchContent(data, status, pending, request, agentPath)
                        "goal" -> {
                            OutlinedTextField(goal, { goal = it }, label = { Text("Goal") }, enabled = !pending, modifier = Modifier.fillMaxWidth())
                            Button(enabled = !pending && goal.isNotBlank(), onClick = { request("PUT", "$agentPath/goal", JSONObject().put("action", "set").put("goal", goal)) { } }) { Text("Save goal") }
                            TextButton(enabled = !pending, onClick = { request("PUT", "$agentPath/goal", JSONObject().put("action", "off")) { } }) { Text("Turn goal off") }
                        }
                        "permission" -> {
                            val enabled = parseInlinePermission(data)
                            Text(if (enabled) "Inline tools are allowed." else "Inline tools are restricted.")
                            Button(enabled = !pending, onClick = { request("POST", "$agentPath/allow-inline-tools", JSONObject().put("allow_inline_tools", !enabled)) { } }) { Text(if (enabled) "Restrict inline tools" else "Allow inline tools") }
                        }
                        "inline-tools" -> {
                            val enabled = JSONObject(data).getBoolean("enabled")
                            Text("This selection is separate from inline permission.")
                            Row { Text("Inline tools enabled", Modifier.weight(1f)); Switch(enabled, enabled = !pending, onCheckedChange = { value -> request("PUT", path, JSONObject().put("enabled", value)) { } }) }
                        }
                        "tools" -> {
                            val root = JSONObject(data)
                            val override = root.optBoolean("workflow_override")
                            if (override) Text("Workflow ${root.optString("active_workflow_state")} overrides these settings. Changes apply after it clears.")
                            val tools = root.getJSONArray("tools")
                            val categories = (0 until tools.length()).map { tools.getJSONObject(it).optString("category", "General") }.distinct()
                            for (category in categories) {
                                Text(category, style = MaterialTheme.typography.titleSmall)
                                Row {
                                    TextButton(enabled = !pending, onClick = { request("PUT", path, JSONObject().put("category", category).put("enabled", true)) { } }) { Text("Enable category") }
                                    TextButton(enabled = !pending, onClick = { request("PUT", path, JSONObject().put("category", category).put("enabled", false)) { } }) { Text("Disable category") }
                                }
                                for (i in 0 until tools.length()) {
                                    val tool = tools.getJSONObject(i)
                                    if (tool.optString("category", "General") == category) Row {
                                        Column(Modifier.weight(1f)) { Text(tool.optString("tool_name")); if (override) Text("Effective: ${tool.optBoolean("effective_enabled")}") }
                                        Switch(tool.optBoolean("enabled"), enabled = !pending, onCheckedChange = { value -> request("PUT", path, JSONObject().put("tool_name", tool.getString("tool_name")).put("enabled", value)) { } })
                                    }
                                }
                            }
                            if (tools.length() == 0) Text("No worker tools.")
                        }
                        "monitors" -> MonitorControlsContent(data, pending, monitorPath, request) { resultText = it }
                        "manage" -> LifecycleContent(pending, agentPath, request, onTerminate)
                        "interrupt" -> Button(enabled = !pending, onClick = { request("POST", "$agentPath/interrupt", JSONObject()) { resultText = "Interrupt requested." } }) { Text("Interrupt worker") }
                        "children" -> {
                            val rows = JSONObject(data).getJSONArray("children")
                            if (rows.length() == 0) Text("No children.")
                            for (i in 0 until rows.length()) {
                                val child = rows.getJSONObject(i)
                                Text(child.toString(2))
                                val id = child.optString("child_id").ifBlank { child.optString("agent_id").ifBlank { child.optString("id") } }
                                if (id.isNotBlank()) ChildStopContent(pending, id) { request("POST", "$agentPath/subagents/${AgentControlClient.segment(id)}/stop", JSONObject()) { } }
                            }
                        }
                        else -> {
                            val display = runCatching { JSONObject(data).toString(2) }.getOrDefault(data)
                            Text(display.ifBlank { "No entries." })
                        }
                    }
                }
            }
        },
        confirmButton = { TextButton(enabled = !pending, onClick = onDismiss) { Text("Close") } },
        dismissButton = { TextButton(enabled = !pending, onClick = { revision++ }) { Text("Refresh") } })
}

@Composable
private fun LifecycleContent(pending: Boolean, path: String,
    request: (String, String, JSONObject?, (String) -> Unit) -> Unit, onTerminate: () -> Unit) {
    var confirmation by remember { mutableStateOf<String?>(null) }
    var result by remember { mutableStateOf("") }
    Text(result)
    if (confirmation == null) {
        TextButton(enabled = !pending, onClick = { confirmation = "restart" }) { Text("Restart worker") }
        TextButton(enabled = !pending, onClick = { confirmation = "reset" }) { Text("Reset context") }
        TextButton(enabled = !pending, onClick = onTerminate) { Text("Terminate worker") }
    } else {
        Text(if (confirmation == "reset") "Reset starts a fresh session and clears the worker's current context. Saved history remains available." else "Restart stops and starts this worker. Its saved session is resumed.")
        Button(enabled = !pending, onClick = { val operation = confirmation!!; request("POST", "$path/$operation", JSONObject()) { result = "$operation requested. Refresh to check worker status."; confirmation = null } }) { Text("Confirm ${confirmation}") }
        TextButton(enabled = !pending, onClick = { confirmation = null }) { Text("Cancel") }
    }
}

@Composable
private fun ChildStopContent(pending: Boolean, id: String, stop: () -> Unit) {
    var confirm by remember(id) { mutableStateOf(false) }
    TextButton(enabled = !pending, onClick = { if (confirm) { stop(); confirm = false } else confirm = true }) { Text(if (confirm) "Confirm stop $id" else "Stop child") }
}

@Composable
private fun MonitorControlsContent(data: String, pending: Boolean, path: String,
    request: (String, String, JSONObject?, (String) -> Unit) -> Unit, result: (String) -> Unit) {
    var form by remember { mutableStateOf<JSONObject?>(null) }
    var editing by remember { mutableStateOf<String?>(null) }
    var deleteId by remember { mutableStateOf<String?>(null) }
    var formError by remember { mutableStateOf<String?>(null) }
    val fields = listOf("name", "description", "question_instructions", "question_type", "condition_operator", "condition_threshold", "nudge_template", "interval_seconds", "cooldown_seconds", "max_alerts", "trailing_messages_count", "fallback_mode")
    val current = form
    if (current != null) {
        for (field in fields) OutlinedTextField(current.optString(field), { text -> form = JSONObject(current.toString()).put(field, text) }, label = { Text(field.replace('_', ' ')) }, enabled = !pending, modifier = Modifier.fillMaxWidth())
        Row { Text("Enabled", Modifier.weight(1f)); Switch(current.optBoolean("enabled", true), enabled = !pending, onCheckedChange = { form = JSONObject(current.toString()).put("enabled", it) }) }
        formError?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        Button(enabled = !pending, onClick = {
            val payload = JSONObject()
            try {
                for (field in fields) {
                    val value = current.optString(field)
                    if (field in listOf("interval_seconds", "cooldown_seconds", "max_alerts", "trailing_messages_count")) payload.put(field, value.toInt()) else payload.put(field, value)
                }
                require(payload.getString("name").isNotBlank()) { "Enter a name." }
                require(payload.getString("question_instructions").isNotBlank()) { "Enter evaluation instructions." }
                payload.put("enabled", current.optBoolean("enabled", true))
                request(if (editing == null) "POST" else "PUT", path + (editing?.let { "/${AgentControlClient.segment(it)}" } ?: ""), payload) { form = null; editing = null }
            } catch (e: Exception) { formError = e.message ?: "Check the form." }
        }) { Text("Save monitor") }
        TextButton(enabled = !pending, onClick = { form = null; editing = null }) { Text("Cancel") }
    } else {
        TextButton(enabled = !pending, onClick = {
            editing = null; formError = null
            form = JSONObject().put("enabled", true).put("question_type", "noul").put("condition_operator", "gte").put("condition_threshold", "0.8").put("interval_seconds", 30).put("cooldown_seconds", 120).put("max_alerts", 0).put("trailing_messages_count", 6).put("fallback_mode", "skip")
        }) { Text("Add monitor") }
        val rows = JSONObject(data).getJSONArray("monitors")
        Text("${(0 until rows.length()).count { rows.getJSONObject(it).optBoolean("enabled") }} enabled")
        if (rows.length() == 0) Text("No monitors.")
        for (i in 0 until rows.length()) {
            val monitor = rows.getJSONObject(i)
            val id = monitor.getString("id")
            Text(monitor.optString("name"), style = MaterialTheme.typography.titleSmall)
            Row {
                TextButton(enabled = !pending, onClick = { editing = id; form = JSONObject(monitor.toString()); formError = null }) { Text("Edit") }
                TextButton(enabled = !pending, onClick = { request("POST", "$path/${AgentControlClient.segment(id)}/evaluate", JSONObject()) { result(it) } }) { Text("Evaluate") }
                TextButton(enabled = !pending, onClick = {
                    if (deleteId == id) request("DELETE", "$path/${AgentControlClient.segment(id)}", null) { deleteId = null } else deleteId = id
                }) { Text(if (deleteId == id) "Confirm delete" else "Delete") }
            }
        }
    }
}
