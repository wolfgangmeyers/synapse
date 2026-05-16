#!/usr/bin/env python3
"""Poll Purelymail IMAP and deliver unread mail into the supervisor mailbox."""

from contextlib import contextmanager
from datetime import datetime, timezone
from email import policy
from email.header import decode_header, make_header
from email.parser import BytesParser
from email.utils import parseaddr
from html.parser import HTMLParser
import fcntl
import imaplib
import json
import os
import sys


MAIL_CONFIG = os.environ.get(
    "MECHA_MAIL_CONFIG",
    os.path.expanduser("~/.mecha-developer/mail.json"),
)
MAILBOX_DIR = os.environ.get(
    "MECHA_MAILBOX_DIR_OVERRIDE",
    os.path.expanduser("~/.mecha-developer/mailboxes"),
)
LOG_FILE = os.environ.get(
    "MECHA_PURELYMAIL_POLL_LOG",
    os.path.expanduser("~/.mecha-developer/purelymail-poll.log"),
)
LOCK_FILE = os.environ.get(
    "MECHA_PURELYMAIL_POLL_LOCK",
    os.path.expanduser("~/.mecha-developer/.purelymail-poll.lock"),
)


class PollLockUnavailable(Exception):
    """Raised when another poller process already holds the lock."""


class TextHTMLParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []

    def handle_data(self, data):
        text = data.strip()
        if text:
            self.parts.append(text)

    def text(self):
        return "\n".join(self.parts)


def log(message):
    os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    with open(LOG_FILE, "a", encoding="utf-8") as f:
        f.write(f"{ts} {message}\n")


@contextmanager
def poll_lock():
    os.makedirs(os.path.dirname(LOCK_FILE), exist_ok=True)
    lock = open(LOCK_FILE, "a", encoding="utf-8")
    try:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise PollLockUnavailable
        yield
    finally:
        lock.close()


def load_config():
    with open(MAIL_CONFIG, "r", encoding="utf-8") as f:
        return json.load(f)


def decode_subject(value):
    if not value:
        return ""
    try:
        return str(make_header(decode_header(value)))
    except Exception:
        return value


def sender_address(from_header):
    _, address = parseaddr(from_header or "")
    return address.lower() if address else "unknown"


def sender_alias(from_header):
    address = sender_address(from_header)
    local, sep, domain = address.partition("@")
    if sep and domain == "agents.meyers.life" and local:
        return local
    if address == "wolfgangmeyers@gmail.com":
        return "wolfgang"
    return "unknown"


def html_to_text(html):
    parser = TextHTMLParser()
    parser.feed(html or "")
    return parser.text()


def message_body(message):
    plain = None
    html = None

    if message.is_multipart():
        for part in message.walk():
            if part.get_content_maintype() == "multipart":
                continue
            disposition = (part.get_content_disposition() or "").lower()
            if disposition == "attachment":
                continue
            content_type = part.get_content_type()
            if content_type == "text/plain" and plain is None:
                plain = part.get_content()
            elif content_type == "text/html" and html is None:
                html = part.get_content()
    else:
        content_type = message.get_content_type()
        if content_type == "text/plain":
            plain = message.get_content()
        elif content_type == "text/html":
            html = message.get_content()
        else:
            try:
                plain = message.get_content()
            except Exception:
                plain = ""

    if plain is not None:
        return str(plain).strip()
    if html is not None:
        return html_to_text(str(html)).strip()
    return ""


def mailbox_message(raw_message, msg_id):
    message = BytesParser(policy=policy.default).parsebytes(raw_message)
    from_header = message.get("From", "")
    return {
        "id": msg_id,
        "from": sender_address(from_header),
        "from_alias": sender_alias(from_header),
        "to": "supervisor",
        "body": message_body(message),
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "read": False,
        "subject": decode_subject(message.get("Subject", "")),
    }


def write_mailbox_message(message):
    mailbox_path = os.path.join(MAILBOX_DIR, "supervisor")
    os.makedirs(mailbox_path, exist_ok=True)
    msg_file = os.path.join(mailbox_path, f"{message['id']}.json")
    if os.path.exists(msg_file):
        return msg_file
    tmp_file = f"{msg_file}.tmp"
    with open(tmp_file, "w", encoding="utf-8") as f:
        json.dump(message, f, indent=2)
        f.write("\n")
    os.replace(tmp_file, msg_file)
    return msg_file


def fetch_raw_message(imap, message_id):
    typ, data = imap.fetch(message_id, "(BODY.PEEK[])")
    if typ != "OK" or not data:
        raise RuntimeError(f"fetch failed for message {message_id!r}: {typ} {data!r}")
    for item in data:
        if isinstance(item, tuple) and item[1]:
            return item[1]
    raise RuntimeError(f"fetch returned no message bytes for {message_id!r}")


def message_uid(imap, message_id):
    typ, data = imap.fetch(message_id, "(UID)")
    if typ != "OK" or not data:
        raise RuntimeError(f"UID fetch failed for message {message_id!r}: {typ} {data!r}")
    for item in data:
        raw = item[0] if isinstance(item, tuple) else item
        if not raw:
            continue
        text = raw.decode("ascii", errors="ignore")
        marker = "UID "
        if marker in text:
            return text.split(marker, 1)[1].split(")", 1)[0].strip()
    raise RuntimeError(f"UID fetch returned no UID for message {message_id!r}")


def mailbox_id(config, uid):
    local = config["address"].split("@", 1)[0]
    return f"purelymail-{local}-{uid}"


def deliver_unseen(imap, config):
    typ, data = imap.search(None, "UNSEEN")
    if typ != "OK":
        raise RuntimeError(f"UNSEEN search failed: {typ} {data!r}")

    delivered = 0
    message_ids = data[0].split() if data and data[0] else []
    for message_id in message_ids:
        try:
            uid = message_uid(imap, message_id)
            raw = fetch_raw_message(imap, message_id)
            mailbox_msg = mailbox_message(raw, mailbox_id(config, uid))
            msg_file = write_mailbox_message(mailbox_msg)
            typ, _ = imap.store(message_id, "+FLAGS", "\\Seen")
            if typ != "OK":
                log(f"ERROR mark-seen failed id={message_id.decode()} uid={uid} file={msg_file}")
                continue
        except Exception as e:
            log(f"ERROR delivery failed id={message_id.decode()}: {type(e).__name__}: {e}")
            continue
        delivered += 1
        log(
            "delivered "
            f"imap_id={message_id.decode()} from={mailbox_msg['from']} "
            f"subject={mailbox_msg['subject']!r} file={msg_file}"
        )
    if not message_ids:
        log("no unseen messages")
    return delivered


def run():
    config = load_config()
    with poll_lock():
        with imaplib.IMAP4_SSL(
            config["imap_host"],
            int(config.get("imap_port", 993)),
            timeout=30,
        ) as imap:
            imap.login(config["address"], config["password"])
            imap.select("INBOX")
            return deliver_unseen(imap, config)


def main():
    try:
        count = run()
        print(f"Delivered {count} message(s).")
    except PollLockUnavailable:
        log("another poller run is active; skipping")
        print("Another poller run is active; skipping.")
    except Exception as e:
        log(f"ERROR {type(e).__name__}: {e}")
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
