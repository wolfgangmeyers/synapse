package life.meyers.mecha

import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.Locale

internal const val PROMPT_TEMPLATE_EXPORT_FORMAT = "mecha-prompt-templates"
internal const val PROMPT_TEMPLATE_EXPORT_VERSION = 1
internal const val MAX_PROMPT_TEMPLATES = 100
internal const val MAX_PROMPT_TEMPLATE_NAME_BYTES = 128
internal const val MAX_PROMPT_TEMPLATE_BODY_BYTES = 64 * 1024
internal const val MAX_PROMPT_TEMPLATE_ID_BYTES = 1024

internal data class PromptTemplate(
    val name: String,
    val body: String,
    val id: String? = null,
    val updatedAt: String? = null,
) {
    val updated_at: String? get() = updatedAt
}

internal data class PromptTemplateExportPayload(
    val format: String,
    val version: Int,
    val templates: List<PromptTemplate>,
) {
    fun toJson(): String = buildString {
        append("{\"format\":").append(jsonString(format))
        append(",\"version\":").append(version)
        append(",\"templates\":[")
        templates.forEachIndexed { index, template ->
            if (index > 0) append(',')
            append(exportedTemplateJson(template))
        }
        append("]}")
    }
}

internal data class PromptTemplateSelection(val start: Int, val end: Int)

internal data class PromptTemplateEditResult(val text: String, val caret: Int) {
    val value: String get() = text
    val selectionStart: Int get() = caret
    val selectionEnd: Int get() = caret
}

private val promptToken = Regex("\\$\\{([^{}]+)\\}")
private val canonicalTimestamp = Regex("\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z")
private val timestampFormatter = DateTimeFormatter.ofPattern("uuuu-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.ROOT)
    .withZone(ZoneOffset.UTC)
private val promptTemplateImportKeys = setOf("name", "body", "id", "updated_at")

/** Builds the same portable payload as web/router-ui-react prompt-template domain. */
internal fun promptTemplateExportPayload(templates: List<PromptTemplate>?): PromptTemplateExportPayload =
    PromptTemplateExportPayload(PROMPT_TEMPLATE_EXPORT_FORMAT, PROMPT_TEMPLATE_EXPORT_VERSION, templates.orEmpty().map { template ->
        template.copy(updatedAt = template.updatedAt?.takeIf(::isCanonicalPromptTemplateTimestamp))
    })

internal fun promptTemplateExportJson(templates: List<PromptTemplate>?): String =
    promptTemplateExportPayload(templates).toJson()

/** Adds the document's final line ending without changing JSON string contents. */
internal fun promptTemplateExportDocument(templates: List<PromptTemplate>?): String =
    promptTemplateExportJson(templates) + "\n"

/** Parses and validates a complete export before any caller can issue import requests. */
internal fun parsePromptTemplateImport(value: String): List<PromptTemplate> {
    val parsed = try {
        JsonParser(value).parse()
    } catch (_: Exception) {
        throw IllegalArgumentException("Choose a valid prompt template JSON file.")
    }
    val root = parsed as? JsonObject
        ?: throw IllegalArgumentException("This is not a valid prompt template export file.")
    if (root.string("format") != PROMPT_TEMPLATE_EXPORT_FORMAT
        || root.number("version")?.let { it.isFinite() && it == PROMPT_TEMPLATE_EXPORT_VERSION.toDouble() } != true) {
        throw IllegalArgumentException("This is not a valid prompt template export file.")
    }
    val templates = root.value("templates") as? JsonArray
        ?: throw IllegalArgumentException("This is not a valid prompt template export file.")
    if (templates.values.size > MAX_PROMPT_TEMPLATES) {
        throw IllegalArgumentException("This is not a valid prompt template export file.")
    }

    val names = mutableSetOf<String>()
    val ids = mutableSetOf<String>()
    return buildList(templates.values.size) {
        templates.values.forEachIndexed { index, rawTemplate ->
            val template = rawTemplate as? JsonObject
                ?: throw IllegalArgumentException("Template ${index + 1} has invalid metadata.")
            if (template.fields.keys.any { it !in promptTemplateImportKeys }) {
                throw IllegalArgumentException("Template ${index + 1} has invalid metadata.")
            }
            val name = template.string("name")
            val body = template.string("body")
            if (name == null || body == null || name.contains('\u0000') || body.contains('\u0000')
                || jsTrim(name).isEmpty() || jsTrim(body).isEmpty()
                || utf8Length(name) > MAX_PROMPT_TEMPLATE_NAME_BYTES
                || utf8Length(body) > MAX_PROMPT_TEMPLATE_BODY_BYTES) {
                throw IllegalArgumentException("Template ${index + 1} must have a valid name and body.")
            }

            val id = if (template.fields.containsKey("id")) {
                val candidate = template.string("id")
                if (candidate == null || candidate.isEmpty() || candidate.contains('\u0000')
                    || utf8Length(candidate) > MAX_PROMPT_TEMPLATE_ID_BYTES) {
                    throw IllegalArgumentException("Template ${index + 1} has invalid metadata.")
                }
                if (!ids.add(candidate)) {
                    throw IllegalArgumentException("The import contains duplicate template id \"$candidate\".")
                }
                candidate
            } else null

            val updatedAt = if (template.fields.containsKey("updated_at")) {
                val candidate = template.string("updated_at")
                if (candidate == null || !isCanonicalPromptTemplateTimestamp(candidate)) {
                    throw IllegalArgumentException("Template ${index + 1} has invalid metadata.")
                }
                candidate
            } else null

            if (!names.add(name)) {
                throw IllegalArgumentException("The import contains duplicate template name \"$name\".")
            }
            add(PromptTemplate(name = name, body = body, id = id, updatedAt = updatedAt))
        }
    }
}

internal fun parsePromptTemplateImport(payload: PromptTemplateExportPayload): List<PromptTemplate> =
    parsePromptTemplateImport(payload.toJson())

/** Matches the Web UI's overwrite-confirmation inputs: identity and supplied version only. */
internal fun promptTemplateImportConflicts(
    templates: List<PromptTemplate>,
    existing: List<PromptTemplate>,
): List<PromptTemplate> = templates.filter { incoming ->
    incoming.id != null && incoming.updatedAt != null && existing.any { it.id == incoming.id }
}

/** Returns distinct, non-empty token names in their first-use order. */
internal fun parsePromptTemplateTokens(body: String?): List<String> {
    val names = LinkedHashSet<String>()
    promptToken.findAll(body.orEmpty()).forEach { names.add(it.groupValues[1]) }
    return names.toList()
}

/** Replaces complete known tokens and leaves unknown or malformed text untouched. */
internal fun renderPromptTemplate(body: String?, values: Map<String, Any?> = emptyMap()): String =
    promptToken.replace(body.orEmpty()) { match ->
        val name = match.groupValues[1]
        if (values.containsKey(name)) jsString(values[name]) else match.value
    }

/** Returns the composer text represented by a captured selection. */
internal fun promptTemplateSelectedText(
    draft: String,
    selection: PromptTemplateSelection?,
): String {
    if (selection == null) return ""
    val start = selection.start.coerceIn(0, draft.length)
    val end = selection.end.coerceIn(start, draft.length)
    return draft.substring(start, end)
}

/** Replaces the captured selection, clamping it exactly as the Web UI does, and returns the new caret. */
internal fun replacePromptTemplateSelection(
    draft: String,
    replacement: String,
    selection: PromptTemplateSelection?,
): PromptTemplateEditResult {
    val start = (selection?.start ?: draft.length).coerceIn(0, draft.length)
    val end = (selection?.end ?: start).coerceIn(start, draft.length)
    val text = draft.substring(0, start) + replacement + draft.substring(end)
    return PromptTemplateEditResult(text, start + replacement.length)
}

internal fun replacePromptTemplateSelection(
    draft: String,
    selectionStart: Int?,
    selectionEnd: Int?,
    replacement: String,
): PromptTemplateEditResult = replacePromptTemplateSelection(
    draft,
    replacement,
    if (selectionStart == null && selectionEnd == null) null else PromptTemplateSelection(
        selectionStart ?: draft.length,
        selectionEnd ?: (selectionStart ?: draft.length),
    ),
)

private fun isCanonicalPromptTemplateTimestamp(value: String): Boolean {
    if (!canonicalTimestamp.matches(value)) return false
    return try {
        timestampFormatter.format(Instant.parse(value)) == value
    } catch (_: Exception) {
        false
    }
}

// TextEncoder replaces each unpaired UTF-16 surrogate with U+FFFD before counting bytes.
private fun utf8Length(value: String): Int {
    var bytes = 0
    var index = 0
    while (index < value.length) {
        val first = value[index].code
        when {
            first in 0xD800..0xDBFF && index + 1 < value.length && value[index + 1].code in 0xDC00..0xDFFF -> {
                bytes += 4
                index += 2
            }
            first in 0xD800..0xDFFF -> {
                bytes += 3
                index++
            }
            first <= 0x7F -> { bytes++; index++ }
            first <= 0x7FF -> { bytes += 2; index++ }
            else -> { bytes += 3; index++ }
        }
    }
    return bytes
}

private fun jsTrim(value: String): String {
    var start = 0
    var end = value.length
    while (start < end && isEcmaWhitespace(value[start].code)) start++
    while (end > start && isEcmaWhitespace(value[end - 1].code)) end--
    return value.substring(start, end)
}

private fun isEcmaWhitespace(code: Int): Boolean = code == 0x0009 || code == 0x000A || code == 0x000B
    || code == 0x000C || code == 0x000D || code == 0x0020 || code == 0x00A0 || code == 0x1680
    || code in 0x2000..0x200A || code == 0x2028 || code == 0x2029 || code == 0x202F
    || code == 0x205F || code == 0x3000 || code == 0xFEFF

private fun jsString(value: Any?): String = when (value) {
    null -> ""
    is String -> value
    is Boolean -> value.toString()
    is List<*> -> value.joinToString(",") { jsString(it) }
    else -> value.toString()
}

private fun exportedTemplateJson(template: PromptTemplate): String = buildString {
    append("{\"name\":").append(jsonString(template.name))
    append(",\"body\":").append(jsonString(template.body))
    if (template.id != null) append(",\"id\":").append(jsonString(template.id))
    if (template.updatedAt != null && isCanonicalPromptTemplateTimestamp(template.updatedAt)) {
        append(",\"updated_at\":").append(jsonString(template.updatedAt))
    }
    append('}')
}

private fun jsonString(value: String): String = buildString {
    append('"')
    var index = 0
    while (index < value.length) {
        val character = value[index]
        when {
            character == '"' -> append("\\\"")
            character == '\\' -> append("\\\\")
            character == '\b' -> append("\\b")
            character == '\u000C' -> append("\\f")
            character == '\n' -> append("\\n")
            character == '\r' -> append("\\r")
            character == '\t' -> append("\\t")
            character.code in 0xD800..0xDBFF && index + 1 < value.length && value[index + 1].code in 0xDC00..0xDFFF -> {
                append(character).append(value[index + 1])
                index++
            }
            character.code in 0xD800..0xDFFF -> append("\\u").append(character.code.toString(16).padStart(4, '0'))
            character.code in 0x0000..0x001F -> append("\\u").append(character.code.toString(16).padStart(4, '0'))
            else -> append(character)
        }
        index++
    }
    append('"')
}

private sealed interface JsonValue
private data class JsonObject(val fields: LinkedHashMap<String, JsonValue>) : JsonValue {
    fun value(key: String): JsonValue? = fields[key]
    fun string(key: String): String? = (value(key) as? JsonString)?.value
    fun number(key: String): Double? = (value(key) as? JsonNumber)?.value
}
private data class JsonArray(val values: List<JsonValue>) : JsonValue
private data class JsonString(val value: String) : JsonValue
private data class JsonNumber(val value: Double) : JsonValue
private data object JsonNull : JsonValue
private data class JsonBoolean(val value: Boolean) : JsonValue

private class JsonParser(private val source: String) {
    private var index = 0

    fun parse(): JsonValue {
        skipWhitespace()
        val value = parseValue()
        skipWhitespace()
        if (index != source.length) error("trailing JSON")
        return value
    }

    private fun parseValue(): JsonValue {
        if (index >= source.length) error("missing value")
        return when (source[index]) {
            '{' -> parseObject()
            '[' -> parseArray()
            '"' -> JsonString(parseString())
            't' -> parseLiteral("true", JsonBoolean(true))
            'f' -> parseLiteral("false", JsonBoolean(false))
            'n' -> parseLiteral("null", JsonNull)
            '-', in '0'..'9' -> parseNumber()
            else -> error("invalid value")
        }
    }

    private fun parseObject(): JsonObject {
        index++
        skipWhitespace()
        val fields = LinkedHashMap<String, JsonValue>()
        if (consume('}')) return JsonObject(fields)
        while (true) {
            if (index >= source.length || source[index] != '"') error("object key")
            val key = parseString()
            skipWhitespace()
            requireChar(':')
            skipWhitespace()
            fields[key] = parseValue()
            skipWhitespace()
            if (consume('}')) return JsonObject(fields)
            requireChar(',')
            skipWhitespace()
        }
    }

    private fun parseArray(): JsonArray {
        index++
        skipWhitespace()
        val values = mutableListOf<JsonValue>()
        if (consume(']')) return JsonArray(values)
        while (true) {
            values += parseValue()
            skipWhitespace()
            if (consume(']')) return JsonArray(values)
            requireChar(',')
            skipWhitespace()
        }
    }

    private fun parseString(): String {
        requireChar('"')
        val result = StringBuilder()
        while (index < source.length) {
            val character = source[index++]
            when {
                character == '"' -> return result.toString()
                character == '\\' -> {
                    if (index >= source.length) error("string escape")
                    when (val escaped = source[index++]) {
                        '"', '\\', '/' -> result.append(escaped)
                        'b' -> result.append('\b')
                        'f' -> result.append('\u000C')
                        'n' -> result.append('\n')
                        'r' -> result.append('\r')
                        't' -> result.append('\t')
                        'u' -> result.append(parseUnicodeEscape())
                        else -> error("string escape")
                    }
                }
                character.code < 0x20 -> error("control in string")
                else -> result.append(character)
            }
        }
        error("unterminated string")
    }

    private fun parseUnicodeEscape(): Char {
        if (index + 4 > source.length) error("unicode escape")
        val digits = source.substring(index, index + 4)
        if (!digits.all { it in '0'..'9' || it.lowercaseChar() in 'a'..'f' }) error("unicode escape")
        index += 4
        return digits.toInt(16).toChar()
    }

    private fun parseNumber(): JsonNumber {
        val start = index
        if (consume('-')) { /* sign */ }
        if (consume('0').not()) {
            if (index >= source.length || source[index] !in '1'..'9') error("number")
            while (index < source.length && source[index].isDigit()) index++
        }
        if (consume('.')) {
            if (index >= source.length || !source[index].isDigit()) error("number fraction")
            while (index < source.length && source[index].isDigit()) index++
        }
        if (index < source.length && (source[index] == 'e' || source[index] == 'E')) {
            index++
            if (index < source.length && (source[index] == '+' || source[index] == '-')) index++
            if (index >= source.length || !source[index].isDigit()) error("number exponent")
            while (index < source.length && source[index].isDigit()) index++
        }
        return JsonNumber(source.substring(start, index).toDouble())
    }

    private fun <T : JsonValue> parseLiteral(text: String, value: T): T {
        if (!source.startsWith(text, index)) error("literal")
        index += text.length
        return value
    }

    private fun skipWhitespace() {
        while (index < source.length && source[index] in " \t\n\r") index++
    }

    private fun consume(character: Char): Boolean = if (index < source.length && source[index] == character) {
        index++
        true
    } else false

    private fun requireChar(character: Char) {
        if (!consume(character)) error("expected $character")
    }
}
