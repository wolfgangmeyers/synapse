#!/usr/bin/env python3
"""
Extracts alien symbols from the input grid image, cleans them, applies square padding,
and saves them as transparent PNG assets.

Supports running with standard Python 3 (PIL + NumPy) or OpenCV if available.
"""

import argparse
import os
import sys

# Ensure library path for numpy if running on Debian/Ubuntu systems with separated blas/lapack
ld_paths = ["/usr/lib/x86_64-linux-gnu/blas", "/usr/lib/x86_64-linux-gnu/lapack", "/usr/lib/x86_64-linux-gnu"]
curr_ld = os.environ.get("LD_LIBRARY_PATH", "")
needed_ld = ":".join(ld_paths + ([curr_ld] if curr_ld else []))
if not curr_ld or "/blas" not in curr_ld:
    os.environ["LD_LIBRARY_PATH"] = needed_ld
    os.execv(sys.executable, [sys.executable] + sys.argv)

import numpy as np
from PIL import Image

try:
    import cv2
    HAS_OPENCV = True
except ImportError:
    HAS_OPENCV = False

def parse_args():
    parser = argparse.ArgumentParser(description="Extract alien symbols from grid image.")
    parser.add_argument("--input", required=True, help="Path to input image")
    parser.add_argument("--output-dir", required=True, help="Directory to save extracted symbols")
    parser.add_argument("--size", type=int, default=128, help="Output square size in pixels (default: 128)")
    parser.add_argument("--padding-ratio", type=float, default=0.15, help="Padding ratio inside square (default: 0.15)")
    return parser.parse_args()

def extract_symbols(input_path, output_dir, target_size=128, padding_ratio=0.15):
    # Resolve input path: if absolute path doesn't exist, check relative to current working directory
    resolved_input = input_path
    if not os.path.exists(resolved_input):
        basename = os.path.basename(input_path)
        if os.path.exists(basename):
            resolved_input = basename
        elif os.path.exists(os.path.join(os.path.dirname(__file__), "..", basename)):
            resolved_input = os.path.join(os.path.dirname(__file__), "..", basename)
        else:
            # Also check if it's an attachment in router
            alt_local = os.path.join(os.getcwd(), basename)
            if os.path.exists(alt_local):
                resolved_input = alt_local
            else:
                raise FileNotFoundError(f"Input image not found at {input_path} or {basename}")

    os.makedirs(output_dir, exist_ok=True)
    img = Image.open(resolved_input)
    gray = np.array(img.convert('L'))
    arr = np.array(img)

    # 8x8 grid dividing lines in the 1254x1254 image
    x_divs = [33, 180, 328, 478, 627, 776, 925, 1074, 1223]
    y_divs = [94, 223, 352, 482, 610, 739, 868, 997, 1124]

    symbol_count = 0
    extracted_files = []

    # Inside square area available for the symbol
    inner_size = int(target_size * (1.0 - 2 * padding_ratio))

    for r in range(8):
        for c in range(8):
            # Safe margin away from the grid divider double-lines (4-5px wide)
            y0, y1 = y_divs[r] + 8, y_divs[r+1] - 8
            x0, x1 = x_divs[c] + 8, x_divs[c+1] - 8

            cell_gray = gray[y0:y1, x0:x1]
            cell_rgb = arr[y0:y1, x0:x1]

            # Threshold to locate symbol pixels (background is ~10-15)
            thresh = 50
            mask = cell_gray > thresh
            if not mask.any():
                continue

            # Bounding box of the symbol
            ys, xs = np.where(mask)
            min_y, max_y = ys.min(), ys.max()
            min_x, max_x = xs.min(), xs.max()

            # Add a 1-pixel buffer if within cell bounds for smooth anti-aliased edge
            min_y = max(0, min_y - 1)
            max_y = min(cell_gray.shape[0] - 1, max_y + 1)
            min_x = max(0, min_x - 1)
            max_x = min(cell_gray.shape[1] - 1, max_x + 1)

            crop_gray = cell_gray[min_y:max_y+1, min_x:max_x+1]
            crop_rgb = cell_rgb[min_y:max_y+1, min_x:max_x+1]

            # Build transparent RGBA image:
            # The symbol pixels are bright cyan/white on dark teal/black background.
            # Background noise is <= 30.
            # Let alpha transition smoothly from 0 at gray <= 30 to 255 at gray >= 160.
            bg_cutoff = 30.0
            fg_sat = 160.0
            alpha = np.clip((crop_gray.astype(np.float32) - bg_cutoff) / (fg_sat - bg_cutoff), 0.0, 1.0)
            
            # Smooth step curve for crisp yet antialiased edges
            alpha = alpha * alpha * (3 - 2 * alpha)
            alpha_uint8 = (alpha * 255).astype(np.uint8)

            # Clean monochrome white for easy CSS mask / tinting, or preserving pure white shape:
            h, w = crop_gray.shape
            rgba = np.zeros((h, w, 4), dtype=np.uint8)
            rgba[:, :, 0] = 255
            rgba[:, :, 1] = 255
            rgba[:, :, 2] = 255
            rgba[:, :, 3] = alpha_uint8

            glyph_img = Image.fromarray(rgba, mode='RGBA')

            # Scale to fit inside inner_size maintaining aspect ratio
            scale = min(inner_size / w, inner_size / h)
            new_w = max(1, int(round(w * scale)))
            new_h = max(1, int(round(h * scale)))
            glyph_resized = glyph_img.resize((new_w, new_h), Image.Resampling.LANCZOS)

            # Center in target_size x target_size square with transparency
            canvas = Image.new('RGBA', (target_size, target_size), (0, 0, 0, 0))
            offset_x = (target_size - new_w) // 2
            offset_y = (target_size - new_h) // 2
            canvas.paste(glyph_resized, (offset_x, offset_y))

            symbol_count += 1
            filename = f"symbol{symbol_count:03d}.png"
            out_file = os.path.join(output_dir, filename)
            canvas.save(out_file, "PNG")
            extracted_files.append(out_file)

    print(f"Extracted {symbol_count} distinct symbols into {output_dir}")
    return extracted_files

if __name__ == "__main__":
    args = parse_args()
    extract_symbols(args.input, args.output_dir, args.size, args.padding_ratio)
