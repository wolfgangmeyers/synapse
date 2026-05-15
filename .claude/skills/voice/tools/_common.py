#!/usr/bin/env python3
"""Shared helpers for voice skill scripts.

This wrapper re-exports the generate-persona helper module so the voice tools
reuse the established JSONL/chunking conventions instead of forking them.
"""

from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

GENERATE_PERSONA_TOOLS = (
    Path(__file__).resolve().parents[2] / "generate-persona" / "tools"
)

if str(GENERATE_PERSONA_TOOLS) not in sys.path:
    sys.path.insert(0, str(GENERATE_PERSONA_TOOLS))

_SPEC = importlib.util.spec_from_file_location(
    "generate_persona_shared_common",
    GENERATE_PERSONA_TOOLS / "_common.py",
)
if _SPEC is None or _SPEC.loader is None:
    raise RuntimeError("unable to load generate-persona shared _common.py")

_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)

chunk_text = _MODULE.chunk_text
emit_record = _MODULE.emit_record
iter_files = _MODULE.iter_files

__all__ = ["chunk_text", "emit_record", "iter_files"]

