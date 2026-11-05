#!/usr/bin/env python3
"""Compatibility entry point for the slice-avatar-grid skill's script."""

from pathlib import Path

_script = Path(__file__).resolve().parent.parent / ".claude/skills/slice-avatar-grid/slice_avatar_grid.py"
exec(compile(_script.read_bytes(), str(_script), "exec"))
