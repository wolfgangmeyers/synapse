package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MarkdownRendererTest {
    @Test
    fun parsesWebSupportedBlocksAndInlineStyles() {
        val blocks = parseMarkdown(
            "# Title\n\nParagraph with *emphasis*, **strong**, `code`, and [docs](https://example.com).\n\n" +
                "- first\n- second\n\n> quoted\n\n```kotlin\n  val answer = 42\n```",
        )

        println("MARKDOWN DEBUG allowed=${isAllowedMarkdownLink("https://example.com")} blocks=$blocks")
        assertEquals(5, blocks.size)
        assertEquals(1, (blocks[0] as MarkdownBlock.Heading).level)
        val paragraph = blocks[1] as MarkdownBlock.Paragraph
        assertTrue(paragraph.inlines.any { it is MarkdownInline.Emphasis })
        assertTrue(paragraph.inlines.any { it is MarkdownInline.Strong })
        assertTrue(paragraph.inlines.any { it is MarkdownInline.InlineCode && it.value == "code" })
        assertTrue(paragraph.inlines.any { it is MarkdownInline.Link && it.destination == "https://example.com" })
        assertEquals(2, (blocks[2] as MarkdownBlock.ListBlock).items.size)
        assertEquals("quoted", textOf((blocks[3] as MarkdownBlock.Quote).blocks))
        assertEquals("  val answer = 42\n", (blocks[4] as MarkdownBlock.CodeBlock).text)
    }

    @Test
    fun preservesWhitespaceInParagraphsAndFencedCode() {
        val blocks = parseMarkdown("  leading  spaces\nline  two\n\n~~~text\n  x\n\n y\n~~~")

        assertEquals("  leading  spaces\nline  two", textOf(listOf(blocks[0])))
        assertEquals("  x\n\n y\n", (blocks[1] as MarkdownBlock.CodeBlock).text)
    }

    @Test
    fun supportsNestedListsAndNestedInlineStyles() {
        val blocks = parseMarkdown("- outer **bold *italic***\n  - inner [safe](mailto:user@example.com)\n\n> > nested")
        println("MARKDOWN DEBUG nested=$blocks")

        val list = blocks[0] as MarkdownBlock.ListBlock
        assertEquals(1, list.items.size)
        assertTrue(list.items[0].blocks.any { it is MarkdownBlock.ListBlock })
        val quote = blocks[1] as MarkdownBlock.Quote
        assertTrue(quote.blocks.single() is MarkdownBlock.Quote)
    }

    @Test
    fun rejectsUnsafeLinksWithoutInterpretingHtml() {
        assertFalse(isAllowedMarkdownLink("javascript:alert(1)"))
        assertFalse(isAllowedMarkdownLink("data:text/html,<script>alert(1)</script>"))
        assertFalse(isAllowedMarkdownLink("file:///etc/passwd"))
        assertFalse(isAllowedMarkdownLink(" https://example.com"))
        assertTrue(isAllowedMarkdownLink("https://example.com/path?q=1"))
        assertTrue(isAllowedMarkdownLink("mailto:user@example.com"))

        val blocks = parseMarkdown("<script>alert(1)</script> [run](javascript:alert(1)) [read](data:text/plain,x)")
        println("MARKDOWN DEBUG unsafe=$blocks")
        val text = textOf(blocks)
        assertTrue(text.contains("<script>alert(1)</script>"))
        assertTrue(text.contains("[run](javascript:alert(1))"))
        assertTrue(text.contains("[read](data:text/plain,x)"))
        assertTrue(blocks.flatMap { block -> links(listOf(block)) }.isEmpty())
    }

    @Test
    fun leavesMalformedMarkdownAsText() {
        val blocks = parseMarkdown("**unfinished *also unfinished\n[missing](https://example.com\n`unterminated")
        println("MARKDOWN DEBUG malformed=$blocks")
        assertEquals(1, blocks.size)
        val text = textOf(blocks)
        assertTrue(text.contains("**unfinished"))
        assertTrue(text.contains("[missing](https://example.com"))
        assertTrue(text.contains("`unterminated"))
    }

    @Test
    fun boundsLargeInputWithoutExpandingMarkdownStructure() {
        val source = "*x*\n".repeat(40_000)
        val blocks = parseMarkdown(source)

        assertEquals(1, blocks.size)
        assertEquals(source, textOf(blocks))
    }

    private fun textOf(blocks: List<MarkdownBlock>): String = buildString {
        blocks.forEach { block ->
            when (block) {
                is MarkdownBlock.Paragraph -> append(inlineText(block.inlines))
                is MarkdownBlock.Heading -> append(inlineText(block.inlines))
                is MarkdownBlock.CodeBlock -> append(block.text)
                is MarkdownBlock.ListBlock -> block.items.forEach { append(textOf(it.blocks)) }
                is MarkdownBlock.Quote -> append(textOf(block.blocks))
            }
        }
    }

    private fun inlineText(inlines: List<MarkdownInline>): String = buildString {
        inlines.forEach { inline ->
            when (inline) {
                is MarkdownInline.Text -> append(inline.value)
                is MarkdownInline.Emphasis -> append(inlineText(inline.children))
                is MarkdownInline.Strong -> append(inlineText(inline.children))
                is MarkdownInline.InlineCode -> append(inline.value)
                is MarkdownInline.Link -> append(inlineText(inline.label))
            }
        }
    }

    private fun links(blocks: List<MarkdownBlock>): List<MarkdownInline.Link> = buildList {
        blocks.forEach { block ->
            when (block) {
                is MarkdownBlock.Paragraph -> addLinks(block.inlines, this)
                is MarkdownBlock.Heading -> addLinks(block.inlines, this)
                is MarkdownBlock.CodeBlock -> Unit
                is MarkdownBlock.ListBlock -> block.items.forEach { addAll(links(it.blocks)) }
                is MarkdownBlock.Quote -> addAll(links(block.blocks))
            }
        }
    }

    private fun addLinks(inlines: List<MarkdownInline>, result: MutableList<MarkdownInline.Link>) {
        inlines.forEach { inline ->
            when (inline) {
                is MarkdownInline.Link -> result += inline
                is MarkdownInline.Emphasis -> addLinks(inline.children, result)
                is MarkdownInline.Strong -> addLinks(inline.children, result)
                else -> Unit
            }
        }
    }
}
