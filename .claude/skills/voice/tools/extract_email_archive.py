#!/usr/bin/env python3
"""Extract authored text from email archives into voice-analysis chunks."""

from __future__ import annotations

import argparse
import mailbox
import sys
from email import policy
from email.message import EmailMessage
from email.parser import BytesParser
from pathlib import Path

from _common import chunk_text, emit_record, iter_files


def normalize_text(value: str) -> str:
    lines = []
    for raw_line in value.splitlines():
        line = raw_line.strip()
        if not line or line.startswith(">"):
            continue
        lines.append(line)
    return "\n".join(lines).strip()


def body_from_message(message: EmailMessage) -> str:
    if message.is_multipart():
        parts = []
        for part in message.walk():
            if part.get_content_type() != "text/plain":
                continue
            if part.get_content_disposition() == "attachment":
                continue
            payload = part.get_content()
            if isinstance(payload, str):
                parts.append(payload)
        return "\n".join(parts)
    payload = message.get_content()
    return payload if isinstance(payload, str) else ""


def address_matches(raw_value: str | None, filters: set[str]) -> bool:
    if not filters:
        return True
    if not raw_value:
        return False
    lowered = raw_value.casefold()
    return any(candidate in lowered for candidate in filters)


def emit_message(message: EmailMessage, path: Path, index: int, max_chars: int) -> int:
    text = normalize_text(body_from_message(message))
    if len(text) < 20 or not any(char.isalpha() for char in text):
        return 0

    emitted = 0
    for chunk_index, chunk in enumerate(chunk_text(text, max_chars=max_chars), start=1):
        emit_record(
            chunk,
            {
                "source_type": "email-archive",
                "source": str(path),
                "date": message.get("Date"),
                "context": {
                    "from": message.get("From"),
                    "subject": message.get("Subject"),
                    "message_index": index,
                    "chunk_index": chunk_index,
                },
            },
        )
        emitted += 1
    return emitted


def extract_mbox(path: Path, sender_filters: set[str], max_chars: int) -> int:
    emitted = 0
    mbox = mailbox.mbox(path)
    for index, message in enumerate(mbox):
        if not address_matches(message.get("From"), sender_filters):
            continue
        emitted += emit_message(message, path, index, max_chars)
    return emitted


def extract_eml(path: Path, sender_filters: set[str], max_chars: int) -> int:
    with path.open("rb") as handle:
        message = BytesParser(policy=policy.default).parse(handle)
    if not address_matches(message.get("From"), sender_filters):
        return 0
    return emit_message(message, path, 0, max_chars)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="Email files or directories")
    parser.add_argument(
        "--author-email",
        action="append",
        default=[],
        help="Only include mail whose From header contains this email address.",
    )
    parser.add_argument(
        "--author-name",
        action="append",
        default=[],
        help="Only include mail whose From header contains this display name.",
    )
    parser.add_argument("--max-chars", type=int, default=4000)
    args = parser.parse_args()

    try:
        files = list(iter_files(args.paths, {".mbox", ".eml"}))
    except FileNotFoundError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    if not files:
        print("Error: no .mbox or .eml files found", file=sys.stderr)
        return 1

    sender_filters = {
        value.casefold()
        for value in [*args.author_email, *args.author_name]
        if value.strip()
    }

    emitted = 0
    for path in files:
        if path.suffix.lower() == ".mbox":
            emitted += extract_mbox(path, sender_filters, args.max_chars)
        else:
            emitted += extract_eml(path, sender_filters, args.max_chars)

    if emitted == 0:
        print("Error: no email messages matched the provided filters", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

