package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PromptTemplatesTest {
    @Test
    fun tokensMatchTheWebHelperInFirstUseOrder() {
        assertEquals(
            listOf("First", "second", "SECOND"),
            parsePromptTemplateTokens("${'$'}{First} ${'$'}{second} ${'$'}{First} ${'$'}{SECOND} ${'$'}{} ${'$'}{incomplete"),
        )
        assertEquals(listOf(" "), parsePromptTemplateTokens("${'$'}{} ${'$'}{ }"))
    }

    @Test
    fun malformedAndUnknownTokensRemainLiteral() {
        val body = "Hello ${'$'}{First}; ${'$'}{second}; ${'$'}{First}; ${'$'}{SECOND}; ${'$'}{}; ${'$'}{incomplete}; ${'$'}{missing}"
        assertEquals(
            "Hello A${'$'}1; B${'$'}${'$'}2; A${'$'}1; ${'$'}& literal; ${'$'}{}; ${'$'}{incomplete}; ${'$'}{missing}",
            renderPromptTemplate(body, mapOf("First" to "A${'$'}1", "second" to "B${'$'}${'$'}2", "SECOND" to "${'$'}& literal")),
        )
        assertEquals("${'$'}{a${'$'}{b} done", renderPromptTemplate("${'$'}{a${'$'}{b} ${'$'}{ok}", mapOf("ok" to "done")))
    }

    @Test
    fun savePublishesSuccessAfterStartingTheListRefresh() {
        val events = mutableListOf<String>()
        val nextSerial = completePromptTemplateSave(
            currentSerial = 4,
            operation = "update",
            refresh = { events += "refresh" },
            publish = { state ->
                events += state.status.name
                assertEquals("Template saved.", state.message)
                assertEquals(5, state.successSerial)
            },
        )

        assertEquals(listOf("refresh", "SUCCESS"), events)
        assertEquals(5, nextSerial)
    }

    @Test
    fun exportHasTheVersionedShapeAndPreservesBodyWhitespace() {
        val templates = listOf(
            PromptTemplate(name = "Review", body = "  Check ${'$'}{File}\n"),
            PromptTemplate(id = "legacy/ID%? # é", name = "With metadata", body = "Body", updatedAt = "2026-08-25T12:34:56.789Z"),
        )
        val payload = promptTemplateExportPayload(templates)
        assertEquals(PROMPT_TEMPLATE_EXPORT_FORMAT, payload.format)
        assertEquals(PROMPT_TEMPLATE_EXPORT_VERSION, payload.version)
        assertEquals(templates, payload.templates)
        assertTrue(payload.toJson().contains("\\n"))
        assertTrue(payload.toJson().contains("legacy/ID%? # é"))
        assertFalse(payload.toJson().contains("created_at"))
    }

    @Test
    fun exportDocumentUsesARealTrailingNewlineAndRoundTripsThroughImporter() {
        val original = listOf(PromptTemplate(name = "Multiline", body = "first\nsecond"))
        val document = promptTemplateExportDocument(original)
        assertTrue(document.endsWith("\n"))
        assertFalse(document.endsWith("\\n"))
        assertEquals(original, parsePromptTemplateImport(document))
    }

    @Test
    fun exportOmitsInvalidTimestampsAndImportRoundTrips() {
        val payload = promptTemplateExportPayload(
            listOf(PromptTemplate(name = "Name", body = "Body", id = "id", updatedAt = "not-a-timestamp")),
        )
        assertEquals(null, payload.templates.single().updatedAt)
        val original = listOf(
            PromptTemplate(id = "legacy/ID%? # é", name = "Review", body = "Check ${'$'}{File}", updatedAt = "2026-08-25T12:34:56.789Z"),
            PromptTemplate(name = "No metadata", body = "  Body  "),
        )
        assertEquals(original, parsePromptTemplateImport(promptTemplateExportJson(original)))
        assertEquals(original, parsePromptTemplateImport(promptTemplateExportPayload(original)))
    }

    @Test
    fun importRejectsMalformedUnknownAndOversizedValues() {
        assertThrowsMessage("Choose a valid prompt template JSON file.") { parsePromptTemplateImport("{") }
        assertThrowsMessage("This is not a valid prompt template export file.") {
            parsePromptTemplateImport("{\"format\":\"other\",\"version\":1,\"templates\":[]}")
        }
        assertThrowsMessage("Template 1 must have a valid name and body.") {
            parsePromptTemplateImport(templateJson("\u0000name", "body"))
        }
        assertThrowsMessage("Template 1 must have a valid name and body.") {
            parsePromptTemplateImport(templateJson("n".repeat(129), "body"))
        }
        assertThrowsMessage("Template 1 must have a valid name and body.") {
            parsePromptTemplateImport(templateJson("name", "b".repeat(65 * 1024)))
        }
        assertThrowsMessage("Template 1 has invalid metadata.") {
            parsePromptTemplateImport(templateJson("name", "body", "\"created_at\":null"))
        }
    }

    @Test
    fun importRejectsDuplicateNamesAndIdsAndBadMetadata() {
        assertThrowsMessage("duplicate template name") {
            parsePromptTemplateImport(importJson("{\"name\":\"same\",\"body\":\"one\"}", "{\"name\":\"same\",\"body\":\"two\"}"))
        }
        assertThrowsMessage("duplicate template id") {
            parsePromptTemplateImport(importJson("{\"id\":\"same\",\"name\":\"one\",\"body\":\"one\"}", "{\"id\":\"same\",\"name\":\"two\",\"body\":\"two\"}"))
        }
        assertThrowsMessage("invalid metadata") {
            parsePromptTemplateImport(importJson("{\"id\":\"\",\"name\":\"name\",\"body\":\"body\"}"))
        }
        assertThrowsMessage("invalid metadata") {
            parsePromptTemplateImport(importJson("{\"name\":\"name\",\"body\":\"body\",\"updated_at\":\"2026-08-25T12:34:56Z\"}"))
        }
        assertThrowsMessage("invalid metadata") {
            parsePromptTemplateImport(importJson("{\"name\":\"name\",\"body\":\"body\",\"extra\":true}"))
        }
    }

    @Test
    fun byteBoundariesAndNulAreValidated() {
        val name128 = "é".repeat(64)
        val body65536 = "é".repeat(32768)
        val valid = parsePromptTemplateImport(templateJson(name128, body65536))
        assertEquals(name128, valid.single().name)
        assertEquals(body65536, valid.single().body)
        assertThrowsMessage("valid name and body") { parsePromptTemplateImport(templateJson("é".repeat(65), "body")) }
        assertThrowsMessage("valid name and body") { parsePromptTemplateImport(templateJson("name", "é".repeat(32769))) }
        assertThrowsMessage("valid name and body") { parsePromptTemplateImport(templateJson("name", "body\u0000")) }
        assertThrowsMessage("invalid metadata") {
            parsePromptTemplateImport(templateJson("name", "body", "\"id\":\"${"é".repeat(513)}\""))
        }
    }

    @Test
    fun conflictsUseOnlyMatchingPortableIdentityAndTimestampInputs() {
        val existing = listOf(
            PromptTemplate(id = "one", name = "One", body = "old", updatedAt = "2026-01-01T00:00:00.000Z"),
            PromptTemplate(id = "two", name = "Two", body = "old"),
        )
        val imported = listOf(
            PromptTemplate(id = "one", name = "Renamed", body = "new", updatedAt = "2026-02-01T00:00:00.000Z"),
            PromptTemplate(id = "two", name = "Ignored", body = "new"),
            PromptTemplate(name = "New", body = "new"),
        )
        assertEquals(listOf(imported[0]), promptTemplateImportConflicts(imported, existing))
        assertEquals(emptyList<PromptTemplate>(), promptTemplateImportConflicts(imported.drop(1), existing))
    }

    @Test
    fun selectionReplacementClampsBoundsAndReturnsCaretAfterInsertedText() {
        assertEquals(
            PromptTemplateEditResult("prefixNEWsuffix", 9),
            replacePromptTemplateSelection("prefixselectedsuffix", "NEW", PromptTemplateSelection(6, 14)),
        )
        assertEquals(
            PromptTemplateEditResult("NEW", 3),
            replacePromptTemplateSelection("body", "NEW", PromptTemplateSelection(-10, 99)),
        )
        assertEquals(
            PromptTemplateEditResult("bodyNEW", 7),
            replacePromptTemplateSelection("body", "NEW", null),
        )
        assertEquals(
            PromptTemplateEditResult("bodNEWy", 6),
            replacePromptTemplateSelection("body", "NEW", PromptTemplateSelection(3, 1)),
        )
    }

    @Test
    fun selectedComposerTextUsesTheSameClampedRangeAsInsertion() {
        assertEquals("selected", promptTemplateSelectedText("prefixselectedsuffix", PromptTemplateSelection(6, 14)))
        assertEquals("body", promptTemplateSelectedText("body", PromptTemplateSelection(-10, 99)))
        assertEquals("", promptTemplateSelectedText("body", PromptTemplateSelection(3, 1)))
        assertEquals("", promptTemplateSelectedText("body", null))
    }

    private fun templateJson(name: String, body: String, extra: String = ""): String =
        importJson("{\"name\":${jsonString(name)},\"body\":${jsonString(body)}${if (extra.isEmpty()) "" else ",$extra"}}")

    private fun importJson(vararg templates: String): String =
        "{\"format\":\"$PROMPT_TEMPLATE_EXPORT_FORMAT\",\"version\":$PROMPT_TEMPLATE_EXPORT_VERSION,\"templates\":[${templates.joinToString(",")}] }"

    private fun jsonString(value: String): String = buildString {
        append('"')
        value.forEach { character ->
            when (character) {
                '"' -> append("\\\"")
                '\\' -> append("\\\\")
                '\b' -> append("\\b")
                '\u000C' -> append("\\f")
                '\n' -> append("\\n")
                '\r' -> append("\\r")
                '\t' -> append("\\t")
                in '\u0000'..'\u001F' -> append("\\u").append(character.code.toString(16).padStart(4, '0'))
                else -> append(character)
            }
        }
        append('"')
    }

    private fun assertThrowsMessage(fragment: String, block: () -> Unit) {
        try {
            block()
            throw AssertionError("Expected an exception containing $fragment")
        } catch (error: IllegalArgumentException) {
            assertTrue(error.message.orEmpty().contains(fragment))
        }
    }
}
