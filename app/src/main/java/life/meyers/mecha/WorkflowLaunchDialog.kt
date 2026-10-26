package life.meyers.mecha

import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import org.json.JSONArray
import org.json.JSONObject

@Composable
internal fun WorkflowLaunchContent(data: String, status: String, pending: Boolean,
    request: (String, String, JSONObject?, (String) -> Unit) -> Unit, agentPath: String) {
    var selected by remember { mutableStateOf<JSONObject?>(null) }
    var values by remember { mutableStateOf<Map<String, String>>(emptyMap()) }
    var error by remember { mutableStateOf<String?>(null) }
    var entryReady by remember { mutableStateOf(false) }
    Text("Current workflow", style = MaterialTheme.typography.titleSmall)
    Text(runCatching {
        val root = JSONObject(status)
        root.optJSONObject("active_workflow_state")?.let { "${it.optString("name")} (${root.optJSONObject("workflow_variables") ?: JSONObject()})" } ?: "No active workflow"
    }.getOrDefault("Loading workflow status…"))
    TextButton(enabled = !pending, onClick = {
        request("PUT", "$agentPath/workflow-state", JSONObject().put("action", "deactivate")) { }
    }) { Text("Deactivate") }
    val workflow = selected
    if (workflow == null) {
        val workflows = runCatching { JSONArray(data) }.getOrDefault(JSONArray())
        if (workflows.length() == 0) Text("No workflows configured.")
        for (i in 0 until workflows.length()) {
            val item = workflows.getJSONObject(i)
            TextButton(enabled = !pending, onClick = {
                request("GET", "/v1/workflows/${AgentControlClient.segment(item.getString("id"))}", null) { body ->
                    val detail = parseWorkflowDetail(body)
                    selected = detail; values = workflowDefaults(detail); entryReady = false
                    val entry = detail.optString("entry_state_id")
                    if (entry.isNotBlank() && entry != "null") request("GET", "/v1/workflow-states/${AgentControlClient.segment(entry)}", null) { entryBody ->
                        parseWorkflowEntry(entryBody); entryReady = true
                    }
                }
            }) { Text(item.optString("name", item.getString("id"))) }
        }
    } else {
        Text(workflow.optString("name"), style = MaterialTheme.typography.titleMedium)
        val variables = workflow.optJSONArray("variables") ?: JSONArray()
        for (i in 0 until variables.length()) {
            val variable = variables.getJSONObject(i)
            val name = variable.getString("name")
            OutlinedTextField(value = values[name].orEmpty(), onValueChange = { values = values + (name to it) },
                label = { Text(variable.optString("label", name) + if (variable.optBoolean("required")) " *" else "") },
                enabled = !pending, singleLine = variable.optString("type") != "textarea", modifier = Modifier.fillMaxWidth())
        }
        error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        if (!entryReady) Text("Entry state is unavailable or loading.")
        Row {
            TextButton(enabled = !pending, onClick = { selected = null; error = null }) { Text("Back") }
            Button(enabled = !pending && entryReady, onClick = {
                error = workflowValidation(workflow, values)
                if (error == null) request("PUT", "$agentPath/workflow-state", JSONObject().put("action", "activate")
                    .put("workflow_id", workflow.getString("id")).put("variables", JSONObject(values))) { selected = null }
            }) { Text("Activate workflow") }
        }
    }
}
