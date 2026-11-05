---
name: slice-avatar-grid
description: Slice AI-generated avatar grid sheets (like 4x4 robot helmet galleries) into individually centered, transparent PNG avatars for web/router-ui-react/avatars. Handles AI grid jitter, circle center detection, and antialiased circular background removal.
---

# Slice Avatar Grid Skill

This skill automates the extraction and addition of avatars from AI-generated grid sheets (such as 4x4 robot helmet galleries) into `web/router-ui-react/avatars`.

## Why Naive Slicing Fails

AI-generated avatar sheets (from Midjourney, DALL-E, etc.) do not align to a uniform mathematical grid:
1. **Grid Jitter**: Center positions vary row-by-row and column-by-column by 5–25 pixels.
2. **Radius Variations**: The circular borders vary in size across the canvas.
3. **Neighbor Contamination**: Naive bounding boxes cut through neighbor circles or leave dark borders.

## The Tool: `slice_avatar_grid.py`

The script beside this skill provides a specialized CLI tool that:
1. Detects each avatar's circular glowing frame using Hough Circle Transform on color masks.
2. Measures the exact outer boundary of each circular ring.
3. Crops an exact, square canvas (default 292x292) centered on each detected avatar.
4. Applies a smooth antialiased circular mask right at the outer edge of the ring, making everything outside completely transparent while preserving 100% of the interior and ring border.
5. Auto-detects the next available filename (e.g. `robot173.png` through `robot188.png`).

## Usage

```bash
uv run --with pillow --with opencv-python --with numpy python3 .claude/skills/slice-avatar-grid/slice_avatar_grid.py \
  --input "/path/to/avatar_sheet.png"
```

Run this example from the repository root. The original `tools/slice_avatar_grid.py` path remains available as a compatibility entry point.

### Options

* `--input`, `-i`: Path to the input image (PNG or JPG).
* `--dest`, `-d`: Destination directory (default: `web/router-ui-react/avatars`, relative to the current working directory).
* `--rows`: Number of grid rows (default: `4`).
* `--cols`: Number of grid columns (default: `4`).
* `--start-index`, `-s`: Starting robot number (default: auto-detected next number in `--dest`).
* `--size`: Output square image dimension in pixels (default: `292`).
* `--prefix`: Filename prefix (default: `robot`).

## Post-Slice Verification

1. **Visual Inspection**: View each generated PNG (e.g. via the `read` tool or image viewer) to verify:
   - The circular ring is complete and intact all the way around.
   - The image is centered.
   - The area outside the ring is transparent with no dark background or halo.
2. **Unit Tests**: Run Vitest in `web/router-ui-react` to ensure avatars are registered:
   ```bash
   npm --prefix web/router-ui-react test tests/unit/agent-avatar.test.ts
   ```
