package life.meyers.mecha

import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue

/** Keeps the editor selection at the corresponding position when stored text is normalized. */
internal fun normalizeComposerValue(value: TextFieldValue): TextFieldValue {
    val normalizedText = normalizeEscapedNewlines(value.text)
    val start = if (normalizedText == value.text) {
        value.selection.start.coerceIn(0, normalizedText.length)
    } else {
        normalizedComposerOffset(value.text, value.selection.start)
    }
    val end = if (normalizedText == value.text) {
        value.selection.end.coerceIn(0, normalizedText.length)
    } else {
        normalizedComposerOffset(value.text, value.selection.end)
    }
    if (normalizedText == value.text && start == value.selection.start && end == value.selection.end) return value
    return TextFieldValue(normalizedText, TextRange(start, end))
}

/** Restored drafts start with the caret after their normalized text. */
internal fun restoredComposerValue(draft: String): TextFieldValue {
    val normalized = normalizeEscapedNewlines(draft)
    return TextFieldValue(normalized, TextRange(normalized.length))
}

private fun normalizedComposerOffset(source: String, offset: Int): Int {
    val clampedOffset = offset.coerceIn(0, source.length)
    var sourceIndex = 0
    var normalizedIndex = 0
    while (sourceIndex < clampedOffset) {
        val escapedNewlineLength = when {
            source.startsWith("\\r\\n", sourceIndex) -> 4
            source.startsWith("\\n", sourceIndex) -> 2
            source.startsWith("\\r", sourceIndex) -> 2
            else -> 0
        }
        if (escapedNewlineLength == 0) {
            sourceIndex++
            normalizedIndex++
        } else if (sourceIndex + escapedNewlineLength <= clampedOffset) {
            sourceIndex += escapedNewlineLength
            normalizedIndex++
        } else {
            // A selection inside an escape sequence belongs at the normalized newline boundary.
            break
        }
    }
    return normalizedIndex
}
