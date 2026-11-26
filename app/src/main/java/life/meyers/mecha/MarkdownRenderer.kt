package life.meyers.mecha

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.text.ClickableText
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text

private const val MAX_MARKDOWN_CHARS = 128 * 1024
private const val MAX_MARKDOWN_BLOCKS = 512
private const val MAX_MARKDOWN_NESTING = 12
private const val LINK_TAG = "markdown-link"

internal sealed interface MarkdownBlock {
    data class Paragraph(val inlines: List<MarkdownInline>) : MarkdownBlock
    data class Heading(val level: Int, val inlines: List<MarkdownInline>) : MarkdownBlock
    data class CodeBlock(val text: String) : MarkdownBlock
    data class ListBlock(val ordered: Boolean, val items: List<MarkdownListItem>) : MarkdownBlock
    data class Quote(val blocks: List<MarkdownBlock>) : MarkdownBlock
}

internal data class MarkdownListItem(val blocks: List<MarkdownBlock>)

internal sealed interface MarkdownInline {
    data class Text(val value: String) : MarkdownInline
    data class Emphasis(val children: List<MarkdownInline>) : MarkdownInline
    data class Strong(val children: List<MarkdownInline>) : MarkdownInline
    data class InlineCode(val value: String) : MarkdownInline
    data class Link(val label: List<MarkdownInline>, val destination: String) : MarkdownInline
}

internal fun isAllowedMarkdownLink(value: String): Boolean {
    if (value.isEmpty() || value.trim() != value) return false
    if (value.startsWith("http://") || value.startsWith("https://")) {
        return runCatching {
            val uri = java.net.URI(value)
            (uri.scheme.equals("http", ignoreCase = true) || uri.scheme.equals("https", ignoreCase = true)) &&
                !uri.rawAuthority.isNullOrBlank()
        }.getOrDefault(false)
    }
    if (!value.startsWith("mailto:", ignoreCase = true)) return false
    return value.matches(Regex("^mailto:[^\\s/]+$", RegexOption.IGNORE_CASE))
}

internal fun parseMarkdown(source: String): List<MarkdownBlock> {
    if (source.length > MAX_MARKDOWN_CHARS) return listOf(MarkdownBlock.Paragraph(listOf(MarkdownInline.Text(source))))
    return MarkdownParser(source).parse()
}

private class MarkdownParser(source: String) {
    private val lines = source.split("\n", limit = Int.MAX_VALUE).map { it.removeSuffix("\r") }
    private var blocks = 0

    fun parse(depth: Int = 0): List<MarkdownBlock> {
        if (depth > MAX_MARKDOWN_NESTING) {
            return listOf(MarkdownBlock.Paragraph(listOf(MarkdownInline.Text(lines.joinToString("\n")))))
        }
        val result = mutableListOf<MarkdownBlock>()
        var index = 0
        while (index < lines.size) {
            if (lines[index].isBlank()) {
                index++
                continue
            }
            if (blocks >= MAX_MARKDOWN_BLOCKS) {
                result += MarkdownBlock.Paragraph(listOf(MarkdownInline.Text(lines.subList(index, lines.size).joinToString("\n"))))
                break
            }
            val fence = FENCE.matchEntire(lines[index])
            if (fence != null) {
                val marker = fence.groupValues[1]
                val code = mutableListOf<String>()
                index++
                while (index < lines.size && !isClosingFence(lines[index], marker)) {
                    code += lines[index]
                    index++
                }
                if (index < lines.size) index++
                add(result, MarkdownBlock.CodeBlock(code.joinToString("\n") + if (code.isNotEmpty()) "\n" else ""))
                continue
            }
            val heading = HEADING.matchEntire(lines[index])
            if (heading != null) {
                add(result, MarkdownBlock.Heading(heading.groupValues[1].length, parseInline(heading.groupValues[2])))
                index++
                continue
            }
            if (QUOTE.matches(lines[index])) {
                val quoteLines = mutableListOf<String>()
                while (index < lines.size && (QUOTE.matches(lines[index]) || lines[index].isBlank())) {
                    quoteLines += if (QUOTE.matches(lines[index])) lines[index].replaceFirst(QUOTE_PREFIX, "") else ""
                    index++
                }
                add(result, MarkdownBlock.Quote(MarkdownParser(quoteLines.joinToString("\n")).parse(depth + 1)))
                continue
            }
            val firstList = listMatch(lines[index])
            if (firstList != null) {
                val ordered = firstList.ordered
                val baseIndent = leadingSpaces(lines[index])
                val items = mutableListOf<MarkdownListItem>()
                while (index < lines.size) {
                    val current = listMatch(lines[index]) ?: break
                    if (current.ordered != ordered || leadingSpaces(lines[index]) > baseIndent) break
                    val itemLines = mutableListOf(current.content)
                    index++
                    while (index < lines.size) {
                        if (listMatch(lines[index]) != null && leadingSpaces(lines[index]) <= baseIndent) break
                        if (lines[index].isBlank()) {
                            if (index + 1 < lines.size && listMatch(lines[index + 1]) != null) break
                            itemLines += ""
                            index++
                            continue
                        }
                        if (leadingSpaces(lines[index]) <= baseIndent && startsBlock(lines[index])) break
                        itemLines += lines[index].drop(minOf(leadingSpaces(lines[index]), 2))
                        index++
                    }
                    val itemBlocks = MarkdownParser(itemLines.joinToString("\n")).parse(depth + 1)
                    items += MarkdownListItem(itemBlocks)
                }
                add(result, MarkdownBlock.ListBlock(ordered, items))
                continue
            }
            val paragraph = mutableListOf(lines[index])
            index++
            while (index < lines.size && lines[index].isNotBlank() && !startsBlock(lines[index])) {
                paragraph += lines[index]
                index++
            }
            add(result, MarkdownBlock.Paragraph(parseInline(paragraph.joinToString("\n"))))
        }
        return result
    }

    private fun add(result: MutableList<MarkdownBlock>, block: MarkdownBlock) {
        blocks++
        result += block
    }

    private fun startsBlock(line: String): Boolean =
        FENCE.matches(line) || HEADING.matches(line) || QUOTE.matches(line) || listMatch(line) != null

    private fun isClosingFence(line: String, marker: String): Boolean =
        line.matches(Regex("^ {0,3}${Regex.escape(marker.first().toString())}{${marker.length},}\\s*$"))

    private data class ListMatch(val ordered: Boolean, val content: String)

    private fun listMatch(line: String): ListMatch? {
        UNORDERED.matchEntire(line)?.let { return ListMatch(false, it.groupValues[2]) }
        ORDERED.matchEntire(line)?.let { return ListMatch(true, it.groupValues[2]) }
        return null
    }

    private fun leadingSpaces(line: String): Int = line.takeWhile { it == ' ' }.length

    companion object {
        private val FENCE = Regex("^ {0,3}(`{3,}|~{3,}).*$")
        private val HEADING = Regex("^ {0,3}(#{1,6})[ \\t]+(.*?)(?:[ \\t]+#+[ \\t]*)?$")
        private val QUOTE = Regex("^ {0,3}>[ \\t]?.*$")
        private val QUOTE_PREFIX = Regex("^ {0,3}>[ \\t]?")
        private val UNORDERED = Regex("^ {0,3}([-+*])[ \\t]+(.*)$")
        private val ORDERED = Regex("^ {0,3}(\\d+\\.)[ \\t]+(.*)$")
    }
}

private fun parseInline(source: String): List<MarkdownInline> {
    if (source.isEmpty()) return emptyList()
    val result = mutableListOf<MarkdownInline>()
    val plain = StringBuilder()
    fun flush() {
        if (plain.isNotEmpty()) {
            result += MarkdownInline.Text(plain.toString())
            plain.clear()
        }
    }
    var index = 0
    while (index < source.length) {
        if (source[index] == '\\' && index + 1 < source.length && source[index + 1] in "\\`*_[]()") {
            plain.append(source[index + 1])
            index += 2
            continue
        }
        if (source[index] == '`') {
            val run = source.substring(index).takeWhile { it == '`' }.length
            val close = source.indexOf("`".repeat(run), index + run)
            if (close >= 0 && !source.substring(index + run, close).contains('\n')) {
                flush()
                result += MarkdownInline.InlineCode(source.substring(index + run, close))
                index = close + run
                continue
            }
        }
        val link = parseLinkAt(source, index)
        if (link != null) {
            flush()
            result += link.inline
            index = link.end
            continue
        }
        val strong = parseDelimited(source, index, "**") ?: parseDelimited(source, index, "__")
        if (strong != null) {
            flush()
            result += MarkdownInline.Strong(parseInline(strong.content))
            index = strong.end
            continue
        }
        if (index + 1 < source.length &&
            ((source[index] == '*' && source[index + 1] == '*') || (source[index] == '_' && source[index + 1] == '_'))
        ) {
            plain.append(source, index, index + 2)
            index += 2
            continue
        }
        val emphasis = parseDelimited(source, index, "*") ?: parseDelimited(source, index, "_")
        if (emphasis != null && (source[index] != '_' || index == 0 || !source[index - 1].isLetterOrDigit())) {
            flush()
            result += MarkdownInline.Emphasis(parseInline(emphasis.content))
            index = emphasis.end
            continue
        }
        plain.append(source[index])
        index++
    }
    flush()
    return result
}

private data class Delimited(val content: String, val end: Int)

private fun parseDelimited(source: String, start: Int, marker: String): Delimited? {
    if (!source.startsWith(marker, start)) return null
    val triple = if (marker == "**") source.indexOf("***", start + marker.length) else -1
    if (triple >= 0) {
        val close = triple + 1
        if (close > start + marker.length) return Delimited(source.substring(start + marker.length, close), close + marker.length)
    }
    val close = source.indexOf(marker, start + marker.length)
    if (close <= start + marker.length) return null
    return Delimited(source.substring(start + marker.length, close), close + marker.length)
}

private data class ParsedLink(val inline: MarkdownInline.Link, val end: Int)

private fun parseLinkAt(source: String, start: Int): ParsedLink? {
    if (source[start] != '[' || start + 1 >= source.length) return null
    val labelEnd = source.indexOf(']', start + 1)
    if (labelEnd <= start + 1 || labelEnd + 1 >= source.length || source[labelEnd + 1] != '(') return null
    val destinationEnd = source.indexOf(')', labelEnd + 2)
    if (destinationEnd <= labelEnd + 2) return null
    val destination = source.substring(labelEnd + 2, destinationEnd)
    if (!isAllowedMarkdownLink(destination)) return null
    return ParsedLink(MarkdownInline.Link(parseInline(source.substring(start + 1, labelEnd)), destination), destinationEnd + 1)
}

@Composable
internal fun MarkdownBody(body: String, outgoing: Boolean) {
    val blocks = remember(body) { parseMarkdown(body) }
    val baseColor = if (outgoing) Ink else TextPrimary
    // Compose Text needs a selection container for the native long-press copy toolbar.
    SelectionContainer {
        Column(modifier = Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            blocks.forEach { block -> MarkdownBlockView(block, baseColor) }
        }
    }
}

@Composable
private fun headingStyle(level: Int): TextStyle = when (level.coerceIn(1, 6)) {
    1 -> MaterialTheme.typography.headlineSmall
    2 -> MaterialTheme.typography.titleLarge
    3 -> MaterialTheme.typography.titleMedium
    4 -> MaterialTheme.typography.titleSmall
    else -> MaterialTheme.typography.bodyLarge
}

@Composable
private fun MarkdownBlockView(block: MarkdownBlock, baseColor: Color) {
    when (block) {
        is MarkdownBlock.Paragraph -> MarkdownInlineText(block.inlines, baseColor)
        is MarkdownBlock.Heading -> MarkdownInlineText(
            block.inlines,
            baseColor,
            headingStyle(block.level),
        )
        is MarkdownBlock.CodeBlock -> Text(
            block.text,
            modifier = Modifier.fillMaxWidth().border(BorderStroke(1.dp, Teal), shape = androidx.compose.foundation.shape.RoundedCornerShape(10.dp)).background(Color(0xFF101A24)).padding(14.dp, 12.dp),
            color = Color(0xFFD9FFF7),
            fontFamily = FontFamily.Monospace,
            fontSize = 13.sp,
        )
        is MarkdownBlock.ListBlock -> Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
            block.items.forEachIndexed { index, item ->
                Row(modifier = Modifier.fillMaxWidth()) {
                    Text(if (block.ordered) "${index + 1}." else "•", color = baseColor, modifier = Modifier.padding(end = 8.dp))
                    Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                        item.blocks.forEach { MarkdownBlockView(it, baseColor) }
                    }
                }
            }
        }
        is MarkdownBlock.Quote -> Row(modifier = Modifier.fillMaxWidth()) {
            Box(Modifier.width(3.dp).background(Teal))
            Column(Modifier.padding(start = 10.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                block.blocks.forEach { MarkdownBlockView(it, baseColor) }
            }
        }
    }
}

@Composable
private fun MarkdownInlineText(inlines: List<MarkdownInline>, color: Color, style: TextStyle = MaterialTheme.typography.bodyLarge) {
    val annotated = remember(inlines, color) { buildMarkdownAnnotatedString(inlines, color) }
    val context = LocalContext.current
    val onClick: (Int) -> Unit = { offset ->
        annotated.getStringAnnotations(LINK_TAG, offset, offset).firstOrNull()?.let { annotation ->
            try {
                context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(annotation.item)))
            } catch (_: ActivityNotFoundException) {
                // A valid link can still have no installed handler on the device.
            }
        }
    }
    if (annotated.getStringAnnotations(LINK_TAG, 0, annotated.length).isEmpty()) {
        Text(annotated, style = style.copy(color = color))
    } else {
        ClickableText(annotated, modifier = Modifier.fillMaxWidth(), style = style.copy(color = color), onClick = onClick)
    }
}

private fun buildMarkdownAnnotatedString(inlines: List<MarkdownInline>, color: Color): AnnotatedString = buildAnnotatedString {
    fun appendInlines(items: List<MarkdownInline>, style: SpanStyle = SpanStyle()) {
        items.forEach { inline ->
            when (inline) {
                is MarkdownInline.Text -> withStyle(style) { append(inline.value) }
                is MarkdownInline.Emphasis -> appendInlines(inline.children, style.merge(SpanStyle(fontStyle = androidx.compose.ui.text.font.FontStyle.Italic)))
                is MarkdownInline.Strong -> appendInlines(inline.children, style.merge(SpanStyle(fontWeight = FontWeight.Bold)))
                is MarkdownInline.InlineCode -> withStyle(style.merge(SpanStyle(fontFamily = FontFamily.Monospace, fontSize = 0.92.em, color = Color(0xFFD9FFF7), background = Color(0xFF253C46)))) { append(inline.value) }
                is MarkdownInline.Link -> {
                    pushStringAnnotation(LINK_TAG, inline.destination)
                    appendInlines(inline.label, style.merge(SpanStyle(color = color, textDecoration = TextDecoration.Underline)))
                    pop()
                }
            }
        }
    }
    appendInlines(inlines)
}

