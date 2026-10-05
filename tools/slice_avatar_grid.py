#!/usr/bin/env python3
"""Slice AI-generated avatar grids (like 4x4 robot helmet sheets) into individual avatar PNGs.

Features:
- Handles AI grid misalignment and non-uniform spacing automatically.
- Uses Hough Circle Detection on the glowing ring channel to find each avatar's exact center.
- Determines the exact outer perimeter of each ring so no inner details or ring borders are clipped.
- Applies smooth antialiased transparency outside the ring (no dark background halo).
- Auto-detects the next index in `web/router-ui-react/avatars` (e.g. robot173.png).

Usage:
    uv run --with pillow --with opencv-python --with numpy python3 tools/slice_avatar_grid.py --input "path/to/grid.png"
"""

import argparse
import glob
import os
import re
import sys
from typing import List, Tuple

import cv2
import numpy as np
from PIL import Image


def find_next_robot_index(dest_dir: str, prefix: str = "robot") -> int:
    """Scan dest_dir for prefix<NUM>.png and return max(NUM) + 1."""
    pattern = os.path.join(dest_dir, f"{prefix}*.png")
    highest = 0
    regex = re.compile(rf"^{re.escape(prefix)}(\d+)\.png$")
    for filepath in glob.glob(pattern):
        filename = os.path.basename(filepath)
        match = regex.match(filename)
        if match:
            num = int(match.group(1))
            if num > highest:
                highest = num
    return highest + 1


def detect_avatar_circles(
    img_bgr: np.ndarray,
    rows: int = 4,
    cols: int = 4,
    hsv_lower: Tuple[int, int, int] = (80, 50, 100),
    hsv_upper: Tuple[int, int, int] = (105, 255, 255),
    min_radius_ratio: float = 0.38,
    max_radius_ratio: float = 0.52,
) -> List[Tuple[float, float, float]]:
    """Detect circular frames across a grid using HSV color mask and Hough Circle Transform.

    Returns list of (center_x, center_y, radius) in row-major order.
    """
    hsv = cv2.cvtColor(img_bgr, cv2.COLOR_BGR2HSV)
    ring_mask = cv2.inRange(hsv, np.array(hsv_lower), np.array(hsv_upper))

    img_h, img_w = img_bgr.shape[:2]
    cell_w = img_w // cols
    cell_h = img_h // rows

    min_r = int(min(cell_w, cell_h) * min_radius_ratio)
    max_r = int(min(cell_w, cell_h) * max_radius_ratio)

    circles = []
    for r in range(rows):
        for c in range(cols):
            x0 = c * cell_w
            y0 = r * cell_h
            x1 = (c + 1) * cell_w
            y1 = (r + 1) * cell_h

            cell_mask = ring_mask[y0:y1, x0:x1]
            blurred = cv2.GaussianBlur(cell_mask, (9, 9), 2)
            detected = cv2.HoughCircles(
                blurred,
                cv2.HOUGH_GRADIENT,
                dp=1,
                minDist=int(min(cell_w, cell_h) * 0.4),
                param1=50,
                param2=20,
                minRadius=min_r,
                maxRadius=max_r,
            )

            if detected is not None:
                c_info = detected[0][0]
                gx = x0 + float(c_info[0])
                gy = y0 + float(c_info[1])
                gr = float(c_info[2])
                circles.append((gx, gy, gr))
            else:
                # Fallback to quadrant center if Hough fails on a very unusual cell
                print(f"Warning: Hough Circle missed cell ({r}, {c}); falling back to quadrant center", file=sys.stderr)
                gx = x0 + cell_w / 2.0
                gy = y0 + cell_h / 2.0
                gr = float((min_r + max_r) / 2.0)
                circles.append((gx, gy, gr))

    return circles


def slice_grid(
    input_path: str,
    dest_dir: str = "web/router-ui-react/avatars",
    rows: int = 4,
    cols: int = 4,
    start_index: int = None,
    output_size: int = 292,
    prefix: str = "robot",
    index_digits: int = 3,
    feather: float = 1.5,
) -> List[str]:
    """Slice grid into individual avatars with transparent background outside the ring."""
    if not os.path.exists(input_path):
        raise FileNotFoundError(f"Input image not found: {input_path}")

    os.makedirs(dest_dir, exist_ok=True)

    img_bgr = cv2.imread(input_path)
    if img_bgr is None:
        raise ValueError(f"Could not load image via OpenCV: {input_path}")

    img_h, img_w = img_bgr.shape[:2]
    src_rgba = Image.open(input_path).convert("RGBA")
    src_np = np.array(src_rgba)

    if start_index is None:
        start_index = find_next_robot_index(dest_dir, prefix)

    circles = detect_avatar_circles(img_bgr, rows, cols)
    print(f"Detected {len(circles)} avatar circles in {rows}x{cols} grid (image size: {img_w}x{img_h}).")

    saved_paths = []
    half = output_size / 2.0

    for idx, (cx, cy, r_hough) in enumerate(circles):
        out_num = start_index + idx
        filename = f"{prefix}{out_num:0{index_digits}d}.png"
        out_path = os.path.join(dest_dir, filename)

        # Center integer coordinate
        icx = int(round(cx))
        icy = int(round(cy))

        x0 = icx - output_size // 2
        y0 = icy - output_size // 2
        x1 = x0 + output_size
        y1 = y0 + output_size

        crop = np.zeros((output_size, output_size, 4), dtype=np.uint8)

        pad_l = max(0, -x0)
        pad_t = max(0, -y0)
        pad_r = max(0, x1 - img_w)
        pad_b = max(0, y1 - img_h)

        sx0 = max(0, x0)
        sy0 = max(0, y0)
        sx1 = min(img_w, x1)
        sy1 = min(img_h, y1)

        crop[pad_t:output_size - pad_b, pad_l:output_size - pad_r] = src_np[sy0:sy1, sx0:sx1]

        # Calculate exact distance from detected sub-pixel center in crop space
        center_x = cx - x0
        center_y = cy - y0
        y_coords, x_coords = np.ogrid[:output_size, :output_size]
        dists = np.sqrt((x_coords - center_x) ** 2 + (y_coords - center_y) ** 2)

        # Identify outer edge of the colored ring
        # Cyan ring pixels:
        cyan_in_crop = (
            (crop[:, :, 2] > 130)
            & (crop[:, :, 1] > 100)
            & (crop[:, :, 2] > crop[:, :, 0] + 35)
            & (dists > 120)
            & (dists < 155)
        )

        if np.sum(cyan_in_crop) > 50:
            # 99.2 percentile of ring pixels + margin covers the outer glow boundary
            r_outer = float(np.percentile(dists[cyan_in_crop], 99.2)) + 0.6
        else:
            r_outer = r_hough + 4.0

        # Safety clamp to avoid cutting ring or spilling into neighbors
        r_outer = max(143.5, min(146.5, r_outer))

        # Smooth antialiased falloff outside r_outer
        alpha = np.clip((r_outer + feather - dists) / feather, 0.0, 1.0)
        alpha_u8 = (alpha * 255).astype(np.uint8)
        crop[:, :, 3] = alpha_u8

        # Clean zero-alpha RGB to keep PNGs compact and pristine
        crop[alpha_u8 == 0, :3] = 0

        out_img = Image.fromarray(crop)
        out_img.save(out_path)
        saved_paths.append(out_path)
        print(f"[{idx+1}/{len(circles)}] Wrote {filename} (center=({cx:.1f}, {cy:.1f}), r={r_outer:.1f})")

    return saved_paths


def main():
    parser = argparse.ArgumentParser(description="Slice an AI-generated avatar grid into individual transparent PNGs.")
    parser.add_argument("--input", "-i", required=True, help="Path to input grid image")
    parser.add_argument("--dest", "-d", default="web/router-ui-react/avatars", help="Output directory")
    parser.add_argument("--rows", type=int, default=4, help="Grid rows (default: 4)")
    parser.add_argument("--cols", type=int, default=4, help="Grid columns (default: 4)")
    parser.add_argument("--start-index", "-s", type=int, default=None, help="Starting robot index (default: auto-detect)")
    parser.add_argument("--size", type=int, default=292, help="Output image dimensions in px (default: 292)")
    parser.add_argument("--prefix", default="robot", help="Output filename prefix (default: robot)")

    args = parser.parse_args()

    saved = slice_grid(
        input_path=args.input,
        dest_dir=args.dest,
        rows=args.rows,
        cols=args.cols,
        start_index=args.start_index,
        output_size=args.size,
        prefix=args.prefix,
    )

    first_file = os.path.basename(saved[0])
    last_file = os.path.basename(saved[-1])
    print(f"\nSuccessfully generated {len(saved)} avatar files: {first_file} through {last_file}")


if __name__ == "__main__":
    main()
