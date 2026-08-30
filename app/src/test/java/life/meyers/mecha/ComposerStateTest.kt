package life.meyers.mecha

import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import org.junit.Assert.assertEquals
import org.junit.Test

class ComposerStateTest {
    @Test
    fun rapidTypingAndBackspaceKeepTheLatestCaretSelection() {
        val typed = normalizeComposerValue(TextFieldValue("hello", TextRange(5)))
        val inserted = normalizeComposerValue(TextFieldValue("hello!", TextRange(6)))
        val deleted = normalizeComposerValue(TextFieldValue("hello", TextRange(5)))

        assertEquals(TextRange(5), typed.selection)
        assertEquals(TextRange(6), inserted.selection)
        assertEquals(TextRange(5), deleted.selection)
    }

    @Test
    fun restoredDraftNormalizesNewlinesAndPlacesSelectionAtTheEnd() {
        val restored = restoredComposerValue("first\\nsecond")

        assertEquals("first\nsecond", restored.text)
        assertEquals(TextRange(restored.text.length), restored.selection)
    }

    @Test
    fun newlineNormalizationMapsSelectionWithoutResettingItToTheStart() {
        val value = normalizeComposerValue(TextFieldValue("first\\nsecond", TextRange(7)))

        assertEquals("first\nsecond", value.text)
        assertEquals(TextRange(6), value.selection)
    }

    @Test
    fun selectionInsideEscapedNewlineClampsToTheNormalizedBoundary() {
        val value = normalizeComposerValue(TextFieldValue("first\\nsecond", TextRange(6)))

        assertEquals(TextRange(5), value.selection)
    }
}
