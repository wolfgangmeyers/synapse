package life.meyers.mecha

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.AttachFile
import androidx.compose.material.icons.filled.Check
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import coil.compose.AsyncImage
import kotlinx.coroutines.launch

/**
 * Controller for displaying full-screen image lightbox dialogs.
 */
class LightboxController {
    var activeImageUrl by mutableStateOf<String?>(null)

    fun show(url: String) {
        activeImageUrl = url
    }

    fun dismiss() {
        activeImageUrl = null
    }
}

val LocalLightboxController = staticCompositionLocalOf { LightboxController() }

/**
 * Renders any generic Block Kit primitive block.
 */
@Composable
fun BlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    when (block.type) {
        BlockType.SECTION -> SectionBlockRenderer(block = block, modifier = modifier)
        BlockType.CARD -> CardBlockRenderer(block = block, modifier = modifier)
        BlockType.COLLAPSIBLE -> CollapsibleBlockRenderer(block = block, modifier = modifier)
        BlockType.ACTIONS -> ActionsBlockRenderer(block = block, modifier = modifier)
        BlockType.INPUT -> InputBlockRenderer(block = block, modifier = modifier)
        BlockType.IMAGE -> ImageBlockRenderer(block = block, modifier = modifier)
        BlockType.REMOTE_SLOT -> {
            val slotId = block.slotId ?: block.blockId ?: "slot"
            val viewUrl = block.viewUrl ?: ""
            RemoteSlotRenderer(slotId = slotId, viewUrl = viewUrl, modifier = modifier)
        }
        BlockType.DIVIDER -> DividerBlockRenderer(block = block, modifier = modifier)
        else -> {
            // Graceful fallback for unknown block types
            Text(
                text = "<!-- Unknown block type: ${block.type} -->",
                color = TextMuted,
                fontSize = 11.sp,
                modifier = modifier.padding(4.dp),
            )
        }
    }
}

@Composable
private fun SectionBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        if (!block.text.isNullOrBlank()) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Box(modifier = Modifier.weight(1f)) {
                    MarkdownBody(body = block.text, outgoing = false)
                }
                if (block.accessory != null) {
                    Spacer(modifier = Modifier.width(8.dp))
                    ElementRenderer(element = block.accessory, blockId = block.blockId)
                }
            }
        }

        if (block.fields.isNotEmpty()) {
            Column(
                modifier = Modifier.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                block.fields.chunked(2).forEach { rowFields ->
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        rowFields.forEach { field ->
                            Column(modifier = Modifier.weight(1f)) {
                                Text(
                                    text = field.title.uppercase(),
                                    color = TextMuted,
                                    fontSize = 11.sp,
                                    fontWeight = FontWeight.SemiBold,
                                    fontFamily = FontFamily.Monospace,
                                )
                                Text(
                                    text = field.value,
                                    color = TextPrimary,
                                    fontSize = 13.sp,
                                )
                            }
                        }
                        if (rowFields.size == 1) {
                            Spacer(modifier = Modifier.weight(1f))
                        }
                    }
                }
            }
        }

        if (block.text.isNullOrBlank() && block.accessory != null) {
            ElementRenderer(element = block.accessory, blockId = block.blockId)
        }
    }
}

@Composable
private fun CardBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    val borderColor = if (block.style == "primary" || block.style == "active") Teal else BorderSubtle

    Card(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(12.dp),
        colors = CardDefaults.cardColors(containerColor = SurfaceDark),
        border = BorderStroke(1.dp, borderColor),
    ) {
        Column(
            modifier = Modifier.padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            if (!block.title.isNullOrBlank() || !block.badge.isNullOrBlank()) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(modifier = Modifier.weight(1f, fill = false)) {
                        if (!block.title.isNullOrBlank()) {
                            Text(
                                text = block.title,
                                color = TextPrimary,
                                fontSize = 15.sp,
                                fontWeight = FontWeight.Bold,
                            )
                        }
                        if (!block.subtitle.isNullOrBlank()) {
                            Text(
                                text = block.subtitle,
                                color = TextMuted,
                                fontSize = 12.sp,
                            )
                        }
                    }
                    if (!block.badge.isNullOrBlank()) {
                        Spacer(modifier = Modifier.width(8.dp))
                        StatusBadge(badge = block.badge)
                    }
                }
            }

            block.blocks.forEach { childBlock ->
                BlockRenderer(block = childBlock)
            }
        }
    }
}

@Composable
private fun CollapsibleBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    var expanded by remember(block.blockId) { mutableStateOf(block.expanded) }

    Card(
        modifier = modifier.fillMaxWidth(),
        shape = RoundedCornerShape(10.dp),
        colors = CardDefaults.cardColors(containerColor = SurfaceDark),
        border = BorderStroke(1.dp, BorderSubtle),
    ) {
        Column(modifier = Modifier.fillMaxWidth()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { expanded = !expanded }
                    .padding(horizontal = 12.dp, vertical = 10.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Row(
                    modifier = Modifier.weight(1f),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    Text(
                        text = block.title ?: "Details",
                        color = TextPrimary,
                        fontSize = 13.sp,
                        fontWeight = FontWeight.Medium,
                    )
                    if (!block.badge.isNullOrBlank()) {
                        StatusBadge(badge = block.badge)
                    }
                }
                Icon(
                    imageVector = if (expanded) Icons.Default.KeyboardArrowUp else Icons.Default.KeyboardArrowDown,
                    contentDescription = if (expanded) "Collapse" else "Expand",
                    tint = TextMuted,
                    modifier = Modifier.size(20.dp),
                )
            }

            AnimatedVisibility(
                visible = expanded,
                enter = fadeIn() + expandVertically(),
                exit = fadeOut() + shrinkVertically(),
            ) {
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 12.dp, vertical = 8.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    HorizontalDivider(color = BorderSubtle, thickness = 1.dp)
                    block.blocks.forEach { childBlock ->
                        BlockRenderer(block = childBlock)
                    }
                }
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun ActionsBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    FlowRow(
        modifier = modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        block.elements.forEach { element ->
            ElementRenderer(element = element, blockId = block.blockId)
        }
    }
}

@Composable
private fun InputBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    val state = LocalSduiState.current
    val blockId = block.blockId ?: ""
    val error = state.getError(blockId)

    Column(
        modifier = modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        if (!block.label.isNullOrBlank()) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                Text(
                    text = block.label,
                    color = TextPrimary,
                    fontSize = 13.sp,
                    fontWeight = FontWeight.Medium,
                )
                if (block.optional) {
                    Text(
                        text = "Optional",
                        color = TextMuted,
                        fontSize = 11.sp,
                    )
                }
            }
        }

        if (!block.hint.isNullOrBlank()) {
            Text(
                text = block.hint,
                color = TextMuted,
                fontSize = 11.sp,
            )
        }

        if (block.element != null) {
            ElementRenderer(element = block.element, blockId = block.blockId)
        }

        if (!error.isNullOrBlank()) {
            Text(
                text = error,
                color = Coral,
                fontSize = 12.sp,
                modifier = Modifier.padding(top = 2.dp),
            )
        }
    }
}

@Composable
private fun ImageBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    val lightbox = LocalLightboxController.current
    val url = block.imageUrl ?: block.thumbnailUrl ?: return

    val aspect = when (block.aspectRatio) {
        "16:9" -> 16f / 9f
        "4:3" -> 4f / 3f
        "1:1" -> 1f
        else -> null
    }

    val imgModifier = modifier
        .fillMaxWidth()
        .then(if (aspect != null) Modifier.aspectRatio(aspect) else Modifier.heightIn(max = 260.dp))
        .clip(RoundedCornerShape(8.dp))
        .border(1.dp, BorderSubtle, RoundedCornerShape(8.dp))
        .clickable { lightbox.show(url) }

    AsyncImage(
        model = url,
        contentDescription = block.altText ?: "Image",
        contentScale = ContentScale.Fit,
        modifier = imgModifier,
    )
}

@Composable
private fun DividerBlockRenderer(
    block: Block,
    modifier: Modifier = Modifier,
) {
    if (block.style == "glow") {
        Box(
            modifier = modifier
                .fillMaxWidth()
                .height(1.dp)
                .background(Teal.copy(alpha = 0.5f))
        )
    } else {
        HorizontalDivider(
            color = BorderSubtle,
            thickness = 1.dp,
            modifier = modifier.padding(vertical = 4.dp),
        )
    }
}

/**
 * Renders any generic Block Kit element.
 */
@Composable
fun ElementRenderer(
    element: Element,
    blockId: String? = null,
    modifier: Modifier = Modifier,
) {
    val dispatcher = LocalActionDispatcher.current
    val state = LocalSduiState.current
    val effectiveBlockId = blockId ?: ""

    when (element.type) {
        ElementType.PLAIN_TEXT -> {
            Text(
                text = element.text ?: "",
                color = TextPrimary,
                fontSize = 14.sp,
                modifier = modifier,
            )
        }
        ElementType.MRKDWN -> {
            MarkdownBody(body = element.text ?: "", outgoing = false)
        }
        ElementType.BUTTON -> {
            val isPrimary = element.style == "primary"
            val isDanger = element.style == "danger"

            if (isPrimary) {
                Button(
                    onClick = {
                        dispatcher.dispatch(
                            actionId = element.actionId,
                            blockId = blockId,
                            value = element.value,
                        )
                    },
                    shape = RoundedCornerShape(8.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Teal,
                        contentColor = Ink,
                    ),
                    modifier = modifier,
                ) {
                    Text(
                        text = element.text ?: "Submit",
                        fontWeight = FontWeight.Bold,
                        fontSize = 13.sp,
                    )
                }
            } else if (isDanger) {
                Button(
                    onClick = {
                        dispatcher.dispatch(
                            actionId = element.actionId,
                            blockId = blockId,
                            value = element.value,
                        )
                    },
                    shape = RoundedCornerShape(8.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = Coral,
                        contentColor = Color.White,
                    ),
                    modifier = modifier,
                ) {
                    Text(
                        text = element.text ?: "Delete",
                        fontWeight = FontWeight.Bold,
                        fontSize = 13.sp,
                    )
                }
            } else {
                OutlinedButton(
                    onClick = {
                        dispatcher.dispatch(
                            actionId = element.actionId,
                            blockId = blockId,
                            value = element.value,
                        )
                    },
                    shape = RoundedCornerShape(8.dp),
                    border = BorderStroke(1.dp, BorderGlow),
                    colors = ButtonDefaults.outlinedButtonColors(
                        contentColor = TextPrimary,
                    ),
                    modifier = modifier,
                ) {
                    Text(
                        text = element.text ?: "Action",
                        fontSize = 13.sp,
                    )
                }
            }
        }
        ElementType.PLAIN_TEXT_INPUT -> {
            val currentVal = state.getValue(effectiveBlockId, element.actionId) as? String
                ?: element.initialValue?.toString()
                ?: ""

            OutlinedTextField(
                value = currentVal,
                onValueChange = { state.setValue(effectiveBlockId, element.actionId, it) },
                placeholder = {
                    if (!element.placeholder.isNullOrBlank()) {
                        Text(element.placeholder, color = TextMuted, fontSize = 13.sp)
                    }
                },
                singleLine = !element.multiline,
                maxLines = if (element.multiline) 6 else 1,
                shape = RoundedCornerShape(8.dp),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = TextPrimary,
                    unfocusedTextColor = TextPrimary,
                    focusedContainerColor = SurfaceInput,
                    unfocusedContainerColor = SurfaceInput,
                    focusedBorderColor = Teal,
                    unfocusedBorderColor = BorderSubtle,
                ),
                modifier = modifier.fillMaxWidth(),
            )
        }
        ElementType.TOGGLE_SWITCH -> {
            val currentVal = state.getValue(effectiveBlockId, element.actionId) as? Boolean
                ?: (element.initialValue as? Boolean)
                ?: false

            Row(
                modifier = modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                if (!element.text.isNullOrBlank()) {
                    Text(
                        text = element.text,
                        color = TextPrimary,
                        fontSize = 13.sp,
                        modifier = Modifier.weight(1f),
                    )
                }
                Switch(
                    checked = currentVal,
                    onCheckedChange = { state.setValue(effectiveBlockId, element.actionId, it) },
                    colors = SwitchDefaults.colors(
                        checkedThumbColor = Teal,
                        checkedTrackColor = Color(0xFF1B3D3B),
                        uncheckedThumbColor = TextMuted,
                        uncheckedTrackColor = SurfaceDark,
                    ),
                )
            }
        }
        ElementType.DROPDOWN_SELECT -> {
            val selectedVal = state.getValue(effectiveBlockId, element.actionId) as? String
                ?: element.initialOption
                ?: element.options.firstOrNull()?.value
                ?: ""

            val selectedOptionText = element.options.find { it.value == selectedVal }?.text ?: selectedVal
            var dropdownOpen by remember { mutableStateOf(false) }

            Box(modifier = modifier.fillMaxWidth()) {
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .background(SurfaceInput)
                        .border(1.dp, BorderSubtle, RoundedCornerShape(8.dp))
                        .clickable { dropdownOpen = true }
                        .padding(horizontal = 12.dp, vertical = 12.dp)
                ) {
                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = Arrangement.SpaceBetween,
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(
                            text = if (selectedOptionText.isNotBlank()) selectedOptionText else (element.placeholder ?: "Select option..."),
                            color = if (selectedOptionText.isNotBlank()) TextPrimary else TextMuted,
                            fontSize = 13.sp,
                        )
                        Icon(
                            imageVector = Icons.Default.KeyboardArrowDown,
                            contentDescription = "Expand",
                            tint = TextMuted,
                            modifier = Modifier.size(18.dp),
                        )
                    }
                }

                DropdownMenu(
                    expanded = dropdownOpen,
                    onDismissRequest = { dropdownOpen = false },
                    modifier = Modifier.background(SurfaceDark).border(1.dp, BorderSubtle),
                ) {
                    element.options.forEach { opt ->
                        DropdownMenuItem(
                            text = {
                                Text(
                                    text = opt.text,
                                    color = if (opt.value == selectedVal) Teal else TextPrimary,
                                    fontSize = 13.sp,
                                )
                            },
                            onClick = {
                                state.setValue(effectiveBlockId, element.actionId, opt.value)
                                dropdownOpen = false
                            },
                        )
                    }
                }
            }
        }
        ElementType.ATTACHMENT_PICKER -> {
            val context = LocalContext.current
            val scope = rememberCoroutineScope()
            var isUploading by remember { mutableStateOf(false) }
            var uploadError by remember { mutableStateOf<String?>(null) }
            val currentMap = state.getValue(effectiveBlockId, element.actionId) as? Map<*, *>

            val launcher = rememberLauncherForActivityResult(
                contract = ActivityResultContracts.GetContent()
            ) { uri: Uri? ->
                if (uri != null) {
                    scope.launch {
                        isUploading = true
                        uploadError = null
                        try {
                            val bytes = context.contentResolver.openInputStream(uri)?.use { it.readBytes() }
                                ?: throw IllegalStateException("Could not read attachment")
                            val endpoint = element.uploadEndpoint ?: "/v1/attachments"
                            val (attId, thumbUrl) = dispatcher.uploadAttachment(
                                endpoint = endpoint,
                                bytes = bytes,
                                filename = "attachment.png",
                                mimeType = context.contentResolver.getType(uri) ?: "image/png",
                            )
                            state.setValue(
                                effectiveBlockId,
                                element.actionId,
                                mapOf("attachment_id" to attId, "thumbnail_url" to thumbUrl)
                            )
                        } catch (e: Exception) {
                            uploadError = e.message ?: "Upload failed"
                        } finally {
                            isUploading = false
                        }
                    }
                }
            }

            Column(
                modifier = modifier.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy(4.dp),
            ) {
                Row(
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    OutlinedButton(
                        onClick = { launcher.launch("*/*") },
                        enabled = !isUploading,
                        shape = RoundedCornerShape(8.dp),
                        border = BorderStroke(1.dp, BorderGlow),
                        colors = ButtonDefaults.outlinedButtonColors(contentColor = TextPrimary),
                    ) {
                        Icon(
                            imageVector = Icons.Default.AttachFile,
                            contentDescription = "Attach",
                            modifier = Modifier.size(16.dp),
                            tint = Teal,
                        )
                        Spacer(modifier = Modifier.width(6.dp))
                        Text(element.text ?: "Attach File", fontSize = 13.sp)
                    }

                    if (isUploading) {
                        CircularProgressIndicator(
                            color = Teal,
                            strokeWidth = 2.dp,
                            modifier = Modifier.size(18.dp),
                        )
                    }

                    if (currentMap != null) {
                        val attId = currentMap["attachment_id"]?.toString() ?: ""
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier
                                .background(Color(0xFF16252C), RoundedCornerShape(6.dp))
                                .border(1.dp, Teal.copy(alpha = 0.5f), RoundedCornerShape(6.dp))
                                .padding(horizontal = 8.dp, vertical = 4.dp),
                        ) {
                            Text(
                                text = attId,
                                color = Teal,
                                fontSize = 11.sp,
                                fontFamily = FontFamily.Monospace,
                            )
                            Spacer(modifier = Modifier.width(4.dp))
                            IconButton(
                                onClick = { state.setValue(effectiveBlockId, element.actionId, emptyMap<String, String>()) },
                                modifier = Modifier.size(14.dp),
                            ) {
                                Icon(
                                    imageVector = Icons.Default.Close,
                                    contentDescription = "Remove",
                                    tint = Coral,
                                )
                            }
                        }
                    }
                }

                if (!uploadError.isNullOrBlank()) {
                    Text(
                        text = uploadError ?: "",
                        color = Coral,
                        fontSize = 11.sp,
                    )
                }
            }
        }
    }
}
