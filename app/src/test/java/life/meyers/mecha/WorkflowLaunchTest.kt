package life.meyers.mecha
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
class WorkflowLaunchTest {
    @Test fun actualRouterEnvelopesAreUnwrapped() {
        assertEquals("flow", parseWorkflowDetail("""{"workflow":{"id":"flow"},"states":[]}""").getString("id"))
        assertEquals("entry", parseWorkflowEntry("""{"workflow_state":{"id":"entry"}}"""))
        assertTrue(parseInlinePermission("""{"agent":{"allow_inline_tools":true}}"""))
    }
    @Test fun defaultsRequiredMultilineAndMissingEntry() {
        val workflow = JSONObject("""{"entry_state_id":"start","variables":[{"name":"task","label":"Task","required":true},{"name":"notes","default_value":"hello\nworld"}]}""")
        val defaults = workflowDefaults(workflow)
        assertEquals("hello\nworld", defaults["notes"])
        assertEquals("Enter Task.", workflowValidation(workflow, defaults))
        assertEquals("Enter Task.", workflowValidation(workflow, defaults + ("task" to "  ")))
        assertNull(workflowValidation(workflow, defaults + ("task" to "do it")))
        assertNotNull(workflowValidation(JSONObject("""{"entry_state_id":null}"""), emptyMap()))
        assertNull(workflowValidation(JSONObject("""{"entry_state_id":"entry"}"""), emptyMap()))
    }
}
