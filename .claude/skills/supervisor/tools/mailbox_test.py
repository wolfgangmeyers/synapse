#!/usr/bin/env python3
"""Mailbox system test suite.

Runs unit tests and a live integration test to verify the async mailbox system
works end-to-end. The live integration test is the acceptance gate — it proves
the hook actually fires and injects messages, not just that the code is correct.

Usage:
  python3 mailbox_test.py              # all tests
  python3 mailbox_test.py --unit-only  # skip live test
  python3 mailbox_test.py --live-only  # skip unit tests

All tests use MECHA_MAILBOX_DIR_OVERRIDE to redirect to temp directories.
No test reads from or writes to ~/.mecha-developer/mailboxes/.

Exit codes:
  0 - all tests passed
  1 - one or more tests failed
"""

import argparse
from email.message import EmailMessage
import fcntl
import importlib.util
import json
import multiprocessing
import os
import smtplib
import sys
import tempfile
import time
import uuid
from datetime import datetime, timezone, timedelta

TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))
HOOK_SCRIPT = os.path.join(TOOLS_DIR, "hooks", "mailbox-deliver.py")
SEND_SCRIPT = os.path.join(TOOLS_DIR, "mailbox_send.py")
CRON_SCRIPT = os.path.join(TOOLS_DIR, "mailbox_cron.py")
POLL_SCRIPT = os.path.join(TOOLS_DIR, "purelymail_poll.py")

PASS = "✓"
FAIL = "✗"
SKIP = "⊘"
SMTP_REPLY_BLOCK_HEADER = "*** MANDATORY — IF YOU REPLY, USE SMTP ***"
SMTP_REPLY_BLOCK_COMMAND = (
    'python3 .claude/skills/supervisor/tools/mailbox_send.py --to <sender-email-above> '
    '--from <self> --subject "..." --body "..."'
)
OLD_REPLY_TIP_PREFIX = "Reply tip:"


# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------


class TestRunner:
    def __init__(self):
        self.results = []

    def test(self, name, fn):
        try:
            fn()
            self.results.append((PASS, name, None))
            print(f"  {PASS} {name}")
        except AssertionError as e:
            self.results.append((FAIL, name, str(e)))
            print(f"  {FAIL} {name}")
            print(f"       AssertionError: {e}")
        except Exception as e:
            self.results.append((FAIL, name, f"{type(e).__name__}: {e}"))
            print(f"  {FAIL} {name}")
            print(f"       {type(e).__name__}: {e}")

    def skip(self, name, reason):
        self.results.append((SKIP, name, reason))
        print(f"  {SKIP} {name}: {reason}")

    def summary(self):
        total = len(self.results)
        passed = sum(1 for r in self.results if r[0] == PASS)
        failed = sum(1 for r in self.results if r[0] == FAIL)
        skipped = sum(1 for r in self.results if r[0] == SKIP)
        print()
        print("=" * 60)
        print(
            f"Results: {passed}/{total} passed"
            + (f", {failed} failed" if failed else "")
            + (f", {skipped} skipped" if skipped else "")
        )
        if failed:
            print("\nFailed tests:")
            for status, name, detail in self.results:
                if status == FAIL:
                    print(f"  {FAIL} {name}")
                    if detail:
                        print(f"       {detail}")
        return failed == 0


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

import subprocess


def run_script(script, args=None, env_overrides=None, stdin_data=None):
    """Run a Python script as a subprocess and return (stdout, stderr, returncode)."""
    env = os.environ.copy()
    if env_overrides:
        for k, v in env_overrides.items():
            if v is None:
                env.pop(k, None)
            else:
                env[k] = v

    cmd = [sys.executable, script] + (args or [])
    result = subprocess.run(
        cmd,
        input=json.dumps(stdin_data) if stdin_data is not None else None,
        capture_output=True,
        text=True,
        env=env,
    )
    return result.stdout, result.stderr, result.returncode


def load_mailbox_send_module():
    spec = importlib.util.spec_from_file_location("mailbox_send_under_test", SEND_SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load_purelymail_poll_module():
    spec = importlib.util.spec_from_file_location("purelymail_poll_under_test", POLL_SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeSMTP:
    calls = []
    fail_login = False

    def __init__(self, host, port, timeout=None, context=None):
        self.host = host
        self.port = port
        self.timeout = timeout
        self.context = context
        self.login_args = None
        self.sent_message = None
        FakeSMTP.calls.append(self)

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def login(self, address, password):
        if FakeSMTP.fail_login:
            raise smtplib.SMTPException("fake login failed")
        self.login_args = (address, password)

    def send_message(self, message):
        self.sent_message = message


class FakeIMAP:
    def __init__(self, messages):
        self.messages = messages
        self.stored = []
        self.store_fails = False

    def search(self, charset, criterion):
        assert charset is None
        assert criterion == "UNSEEN"
        return "OK", [b" ".join(self.messages.keys())]

    def fetch(self, message_id, query):
        if query == "(UID)":
            return "OK", [f"{message_id.decode()} (UID {message_id.decode()})".encode()]
        assert query == "(BODY.PEEK[])"
        return "OK", [(b"BODY[]", self.messages[message_id])]

    def store(self, message_id, command, flags):
        self.stored.append((message_id, command, flags))
        if self.store_fails:
            return "NO", [b"store failed"]
        return "OK", [b""]


def make_message(
    from_agent="supervisor",
    to_agent="worker-1",
    body="Test message body",
    subject=None,
    age_seconds=0,
    read=False,
    from_alias=None,
):
    """Create a test message dict."""
    ts = datetime.now(timezone.utc) - timedelta(seconds=age_seconds)
    msg = {
        "id": str(uuid.uuid4()),
        "from": from_agent,
        "to": to_agent,
        "body": body,
        "timestamp": ts.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "read": read,
    }
    if subject is not None:
        msg["subject"] = subject
    if from_alias is not None:
        msg["from_alias"] = from_alias
    return msg


def seed_mailbox(mailbox_dir, agent_name, messages):
    """Write message dicts into a mailbox directory. Returns list of file paths."""
    agent_mailbox = os.path.join(mailbox_dir, agent_name)
    os.makedirs(agent_mailbox, exist_ok=True)
    paths = []
    for msg in messages:
        fpath = os.path.join(agent_mailbox, f"{msg['id']}.json")
        with open(fpath, "w", encoding="utf-8") as f:
            json.dump(msg, f, indent=2)
        paths.append(fpath)
    return paths


def hook_stdin(tool_name="Read"):
    return {"tool_name": tool_name, "tool_input": {}, "session_id": "test-session"}


def cron_stress_worker(start_event, cron_script, mailbox_dir, lock_file, log_file, deliveries_file):
    """Run mailbox_cron in a child process with tmux calls replaced by a file append."""
    spec = importlib.util.spec_from_file_location(f"mailbox_cron_stress_{os.getpid()}", cron_script)
    cron = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(cron)

    cron.MAILBOX_DIR = mailbox_dir
    cron.LOCK_FILE = lock_file
    cron.LOG_FILE = log_file
    cron.MESSAGE_STALE_THRESHOLD = 0
    cron.window_exists = lambda _agent_name: True

    def record_delivery(_agent_name, text):
        with open(deliveries_file, "a", encoding="utf-8") as f:
            fcntl.flock(f.fileno(), fcntl.LOCK_EX)
            try:
                f.write(text)
                f.write("\n---DELIVERY---\n")
                f.flush()
                os.fsync(f.fileno())
            finally:
                fcntl.flock(f.fileno(), fcntl.LOCK_UN)
        # Keep the winning process inside run() long enough for contenders to hit the cron lock.
        time.sleep(0.2)

    cron.send_text_and_enter = record_delivery
    if not start_event.wait(5):
        return
    cron.run()


# ---------------------------------------------------------------------------
# Unit tests
# ---------------------------------------------------------------------------


def run_unit_tests(runner):
    print("\n[Unit Tests — mailbox_send.py]")

    def test_send_creates_valid_schema():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            stdout, stderr, rc = run_script(
                SEND_SCRIPT,
                args=[
                    "--to", "worker-1",
                    "--from", "supervisor",
                    "--body", "Hello worker",
                    "--subject", "Task update",
                ],
                env_overrides={"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
            )
            assert rc == 0, f"mailbox_send.py exited {rc}: {stderr}"

            agent_dir = os.path.join(mailbox_dir, "worker-1")
            assert os.path.isdir(agent_dir), "Mailbox directory not created"
            files = [f for f in os.listdir(agent_dir) if f.endswith(".json")]
            assert len(files) == 1, f"Expected 1 message file, got {len(files)}"

            with open(os.path.join(agent_dir, files[0]), encoding="utf-8") as f:
                msg = json.load(f)

            assert msg["from"] == "supervisor"
            assert msg["to"] == "worker-1"
            assert msg["body"] == "Hello worker"
            assert msg["subject"] == "Task update"
            assert msg["read"] is False
            assert "id" in msg, "Missing id field"
            assert "timestamp" in msg, "Missing timestamp field"
            # UUID filename matches id
            assert files[0] == f"{msg['id']}.json", "Filename does not match message id"

    runner.test("mailbox_send creates valid message JSON with correct schema", test_send_creates_valid_schema)

    def test_send_no_subject_omits_key():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            run_script(
                SEND_SCRIPT,
                args=["--to", "worker-1", "--from", "supervisor", "--body", "No subject"],
                env_overrides={"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
            )
            agent_dir = os.path.join(mailbox_dir, "worker-1")
            files = [f for f in os.listdir(agent_dir) if f.endswith(".json")]
            with open(os.path.join(agent_dir, files[0]), encoding="utf-8") as f:
                msg = json.load(f)
            assert "subject" not in msg, "subject key should be absent when not provided"

    runner.test("mailbox_send without --subject omits subject key entirely", test_send_no_subject_omits_key)

    def test_send_creates_mailbox_dir():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            assert not os.path.exists(mailbox_dir), "Mailbox dir should not exist yet"
            stdout, stderr, rc = run_script(
                SEND_SCRIPT,
                args=["--to", "worker-99", "--from", "supervisor", "--body", "Test"],
                env_overrides={"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
            )
            assert rc == 0
            assert os.path.isdir(os.path.join(mailbox_dir, "worker-99")), "Mailbox dir should be created"

    runner.test("mailbox_send creates mailbox directory on first use", test_send_creates_mailbox_dir)

    def test_send_rejects_invalid_agent_names():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            for bad_name in ["../etc", "foo/bar", ".hidden", ""]:
                stdout, stderr, rc = run_script(
                    SEND_SCRIPT,
                    args=["--to", bad_name or "worker-1", "--from", "supervisor", "--body", "test"],
                    env_overrides={"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
                )
                if bad_name == "":
                    # Empty string — argparse will reject --to with no value
                    continue
                assert rc != 0, (
                    f"mailbox_send.py should reject invalid agent name {bad_name!r} but exited {rc}"
                )

    runner.test("mailbox_send rejects invalid agent names (path traversal prevention)", test_send_rejects_invalid_agent_names)

    def test_send_accepts_valid_agent_names():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            for good_name in ["worker-1", "supervisor", "worker_2", "Agent1"]:
                stdout, stderr, rc = run_script(
                    SEND_SCRIPT,
                    args=["--to", good_name, "--from", "supervisor", "--body", "test"],
                    env_overrides={"MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir},
                )
                assert rc == 0, (
                    f"mailbox_send.py should accept valid agent name {good_name!r} but exited {rc}: {stderr}"
                )

    runner.test("mailbox_send accepts valid agent names (alphanumeric, hyphens, underscores)", test_send_accepts_valid_agent_names)

    print("\n[Unit Tests — mailbox_send.py email routing]")

    def test_send_email_routes_to_smtp():
        """When --to contains @, mailbox_send should call SMTP using mail.json."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            mail_config = os.path.join(tmp, "mail.json")
            with open(mail_config, "w") as f:
                json.dump({
                    "address": "sender@example.com",
                    "password": "secret",
                    "smtp_host": "smtp.example.com",
                    "smtp_port": 465,
                }, f)

            module = load_mailbox_send_module()
            module.MAILBOX_DIR = mailbox_dir
            module.MAIL_CONFIG = mail_config
            FakeSMTP.calls = []
            FakeSMTP.fail_login = False
            module.smtplib.SMTP_SSL = FakeSMTP

            module._send_via_email(
                argparse.Namespace(
                    to="user@example.com",
                    from_agent="supervisor",
                    body="Hello via email",
                    subject="Test subject",
                )
            )

            assert len(FakeSMTP.calls) == 1
            smtp = FakeSMTP.calls[0]
            assert smtp.host == "smtp.example.com"
            assert smtp.port == 465
            assert smtp.timeout == 30
            assert smtp.login_args == ("sender@example.com", "secret")
            assert smtp.sent_message["From"] == "sender@example.com"
            assert smtp.sent_message["To"] == "user@example.com"
            assert smtp.sent_message["Subject"] == "Test subject"
            assert smtp.sent_message.get_content() == "Hello via email\n"

    runner.test("mailbox_send routes to SMTP when --to contains @", test_send_email_routes_to_smtp)

    def test_send_email_generates_subject_from_body():
        """When --subject is not provided for email, generate from first line of body."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            mail_config = os.path.join(tmp, "mail.json")
            with open(mail_config, "w") as f:
                json.dump({
                    "address": "sender@example.com",
                    "password": "secret",
                    "smtp_host": "smtp.example.com",
                    "smtp_port": 465,
                }, f)

            module = load_mailbox_send_module()
            module.MAILBOX_DIR = mailbox_dir
            module.MAIL_CONFIG = mail_config
            FakeSMTP.calls = []
            FakeSMTP.fail_login = False
            module.smtplib.SMTP_SSL = FakeSMTP

            module._send_via_email(
                argparse.Namespace(
                    to="user@example.com",
                    from_agent="supervisor",
                    body="Task complete. All tests pass.\n\nDetails follow.",
                    subject=None,
                )
            )

            subject = FakeSMTP.calls[0].sent_message["Subject"]
            assert subject == "Task complete. All tests pass.", (
                f"Expected subject from first line of body, got: {subject!r}"
            )

    runner.test("mailbox_send generates subject from first line of body when --subject omitted for email", test_send_email_generates_subject_from_body)

    def test_send_email_logs_audit_trail():
        """Email sends should still log a mailbox JSON in the sender's outbox."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            mail_config = os.path.join(tmp, "mail.json")
            with open(mail_config, "w") as f:
                json.dump({
                    "address": "sender@example.com",
                    "password": "secret",
                    "smtp_host": "smtp.example.com",
                    "smtp_port": 465,
                }, f)

            module = load_mailbox_send_module()
            module.MAILBOX_DIR = mailbox_dir
            module.MAIL_CONFIG = mail_config
            FakeSMTP.calls = []
            FakeSMTP.fail_login = False
            module.smtplib.SMTP_SSL = FakeSMTP

            module._send_via_email(
                argparse.Namespace(
                    to="user@example.com",
                    from_agent="supervisor",
                    body="Audit this",
                    subject="Audit test",
                )
            )

            # Check audit log in sender's mailbox dir
            sender_dir = os.path.join(mailbox_dir, "supervisor")
            assert os.path.isdir(sender_dir), "Sender outbox directory not created"
            files = [f for f in os.listdir(sender_dir) if f.endswith(".json")]
            assert len(files) == 1, f"Expected 1 audit file, got {len(files)}"

            with open(os.path.join(sender_dir, files[0])) as f:
                msg = json.load(f)
            assert msg["to"] == "user@example.com"
            assert msg["body"] == "Audit this"
            assert msg["from"] == "supervisor"
            assert msg["read"] is True, "Audit messages should be marked read (not for delivery)"

    runner.test("mailbox_send logs audit trail JSON in sender's mailbox for email sends", test_send_email_logs_audit_trail)

    def test_send_email_non_email_unchanged():
        """Non-email --to should still use existing mailbox behavior (not call SMTP)."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")

            stdout, stderr, rc = run_script(
                SEND_SCRIPT,
                args=[
                    "--to", "worker-1",
                    "--from", "supervisor",
                    "--body", "Normal mailbox message",
                ],
                env_overrides={
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                    "MECHA_MAIL_CONFIG": os.path.join(tmp, "missing-mail.json"),
                },
            )
            assert rc == 0, f"Non-email send failed: {stderr}"
            # Should create message in worker-1's mailbox, not call SMTP
            agent_dir = os.path.join(mailbox_dir, "worker-1")
            assert os.path.isdir(agent_dir), "Recipient mailbox not created"
            files = [f for f in os.listdir(agent_dir) if f.endswith(".json")]
            assert len(files) == 1

    runner.test("mailbox_send non-email recipients use existing mailbox behavior (no SMTP call)", test_send_email_non_email_unchanged)

    def test_send_email_reports_smtp_failure():
        """If SMTP send fails, mailbox_send should exit non-zero."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            mail_config = os.path.join(tmp, "mail.json")
            with open(mail_config, "w") as f:
                json.dump({
                    "address": "sender@example.com",
                    "password": "secret",
                    "smtp_host": "smtp.example.com",
                    "smtp_port": 465,
                }, f)

            module = load_mailbox_send_module()
            module.MAILBOX_DIR = mailbox_dir
            module.MAIL_CONFIG = mail_config
            FakeSMTP.calls = []
            FakeSMTP.fail_login = True
            module.smtplib.SMTP_SSL = FakeSMTP

            try:
                module._send_via_email(
                    argparse.Namespace(
                        to="user@example.com",
                        from_agent="supervisor",
                        body="Should fail",
                        subject=None,
                    )
                )
                exited = False
            except SystemExit as e:
                exited = e.code != 0
            finally:
                FakeSMTP.fail_login = False
            assert exited, "Should have failed when SMTP fails"

    runner.test("mailbox_send exits non-zero when SMTP fails", test_send_email_reports_smtp_failure)

    print("\n[Unit Tests — purelymail_poll.py]")

    def test_purelymail_poll_sender_address_mapping():
        module = load_purelymail_poll_module()
        assert module.sender_address("Glue <glue@agents.meyers.life>") == "glue@agents.meyers.life"
        assert module.sender_address("wolfgangmeyers@gmail.com") == "wolfgangmeyers@gmail.com"
        assert module.sender_address("stranger@example.com") == "stranger@example.com"
        assert module.sender_address("") == "unknown"

    runner.test("purelymail_poll preserves raw sender email addresses", test_purelymail_poll_sender_address_mapping)

    def test_purelymail_poll_sender_alias_mapping():
        module = load_purelymail_poll_module()
        assert module.sender_alias("Glue <glue@agents.meyers.life>") == "glue"
        assert module.sender_alias("wolfgangmeyers@gmail.com") == "wolfgang"
        assert module.sender_alias("stranger@example.com") == "unknown"

    runner.test("purelymail_poll keeps sender aliases separate from raw from field", test_purelymail_poll_sender_alias_mapping)

    def test_purelymail_poll_body_prefers_plain_text():
        module = load_purelymail_poll_module()
        msg = EmailMessage()
        msg["From"] = "glue@agents.meyers.life"
        msg["Subject"] = "Plain preferred"
        msg.set_content("plain body")
        msg.add_alternative("<html><body>html body</body></html>", subtype="html")

        mailbox_msg = module.mailbox_message(msg.as_bytes(), "test-id")
        assert mailbox_msg["from"] == "glue@agents.meyers.life"
        assert mailbox_msg["from_alias"] == "glue"
        assert mailbox_msg["subject"] == "Plain preferred"
        assert mailbox_msg["body"] == "plain body"

    runner.test("purelymail_poll prefers text/plain over text/html", test_purelymail_poll_body_prefers_plain_text)

    def test_purelymail_poll_mailbox_message_preserves_wolfgang_gmail():
        module = load_purelymail_poll_module()
        msg = EmailMessage()
        msg["From"] = "Wolfgang Meyers <wolfgangmeyers@gmail.com>"
        msg["Subject"] = "Gmail sender"
        msg.set_content("raw email must survive")

        mailbox_msg = module.mailbox_message(msg.as_bytes(), "test-id")
        assert mailbox_msg["from"] == "wolfgangmeyers@gmail.com"
        assert mailbox_msg["from_alias"] == "wolfgang"
        assert mailbox_msg["subject"] == "Gmail sender"

    runner.test(
        "purelymail_poll mailbox JSON preserves Wolfgang Gmail From address",
        test_purelymail_poll_mailbox_message_preserves_wolfgang_gmail,
    )

    def test_purelymail_poll_html_fallback():
        module = load_purelymail_poll_module()
        msg = EmailMessage()
        msg["From"] = "outsider@example.com"
        msg["Subject"] = "HTML only"
        msg.add_alternative("<html><body><p>Hello</p><p>there</p></body></html>", subtype="html")

        mailbox_msg = module.mailbox_message(msg.as_bytes(), "test-id")
        assert mailbox_msg["from"] == "outsider@example.com"
        assert mailbox_msg["from_alias"] == "unknown"
        assert mailbox_msg["body"] == "Hello\nthere"

    runner.test("purelymail_poll falls back to stripped HTML body", test_purelymail_poll_html_fallback)

    def test_purelymail_poll_delivers_and_marks_seen():
        module = load_purelymail_poll_module()
        with tempfile.TemporaryDirectory() as tmp:
            module.MAILBOX_DIR = tmp
            msg = EmailMessage()
            msg["From"] = "donuts@agents.meyers.life"
            msg["Subject"] = "Inbound test"
            msg.set_content("Delivered through IMAP")
            imap = FakeIMAP({b"7": msg.as_bytes()})

            delivered = module.deliver_unseen(imap, {"address": "hippie@agents.meyers.life"})

            assert delivered == 1
            assert imap.stored == [(b"7", "+FLAGS", "\\Seen")]
            supervisor_dir = os.path.join(tmp, "supervisor")
            files = os.listdir(supervisor_dir)
            assert len(files) == 1
            assert files[0] == "purelymail-hippie-7.json"
            with open(os.path.join(supervisor_dir, files[0])) as f:
                mailbox_msg = json.load(f)
            assert mailbox_msg["from"] == "donuts@agents.meyers.life"
            assert mailbox_msg["from_alias"] == "donuts"
            assert mailbox_msg["to"] == "supervisor"
            assert mailbox_msg["subject"] == "Inbound test"
            assert mailbox_msg["body"] == "Delivered through IMAP"
            assert mailbox_msg["read"] is False

    runner.test("purelymail_poll writes mailbox JSON and marks IMAP message seen", test_purelymail_poll_delivers_and_marks_seen)

    def test_purelymail_poll_does_not_mark_seen_when_write_fails():
        module = load_purelymail_poll_module()
        msg = EmailMessage()
        msg["From"] = "glue@agents.meyers.life"
        msg["Subject"] = "Write fail"
        msg.set_content("do not lose me")
        imap = FakeIMAP({b"9": msg.as_bytes()})

        def fail_write(_message):
            raise OSError("disk full")

        module.write_mailbox_message = fail_write
        delivered = module.deliver_unseen(imap, {"address": "hippie@agents.meyers.life"})
        assert delivered == 0
        assert imap.stored == [], "message must not be marked seen when mailbox write fails"

    runner.test("purelymail_poll does not mark IMAP seen when mailbox write fails", test_purelymail_poll_does_not_mark_seen_when_write_fails)

    def test_purelymail_poll_store_failure_is_idempotent():
        module = load_purelymail_poll_module()
        with tempfile.TemporaryDirectory() as tmp:
            module.MAILBOX_DIR = tmp
            msg = EmailMessage()
            msg["From"] = "glue@agents.meyers.life"
            msg["Subject"] = "Store fail"
            msg.set_content("write once")
            imap = FakeIMAP({b"11": msg.as_bytes()})
            imap.store_fails = True

            delivered = module.deliver_unseen(imap, {"address": "hippie@agents.meyers.life"})

            assert delivered == 0
            supervisor_dir = os.path.join(tmp, "supervisor")
            files = os.listdir(supervisor_dir)
            assert files == ["purelymail-hippie-11.json"]

            delivered = module.deliver_unseen(imap, {"address": "hippie@agents.meyers.life"})

            assert delivered == 0
            files = os.listdir(supervisor_dir)
            assert files == ["purelymail-hippie-11.json"], "retry must not create duplicate mailbox JSON"

    runner.test("purelymail_poll does not duplicate mailbox JSON when mark-seen fails", test_purelymail_poll_store_failure_is_idempotent)

    print("\n[Unit Tests — mailbox-deliver.py hook]")

    def test_hook_ignores_non_agent():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            stdout, stderr, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": None,
                    "MECHA_AGENT_WINDOW": None,
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0, f"Hook exited {rc}"
            assert stdout.strip() == "", (
                f"Hook should produce no output for non-agent session, got: {stdout!r}"
            )

    runner.test("hook produces no output when no agent env var is set", test_hook_ignores_non_agent)

    def test_hook_empty_mailbox():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            stdout, stderr, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            assert stdout.strip() == "", (
                f"Empty mailbox should produce no output, got: {stdout!r}"
            )

    runner.test("hook produces no output for empty mailbox", test_hook_empty_mailbox)

    def test_hook_delivers_unread_message():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(from_agent="supervisor", body="Hello from supervisor")
            seed_mailbox(mailbox_dir, "worker-1", [msg])

            stdout, stderr, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            assert "[MAILBOX]" in stdout, f"Expected [MAILBOX] in output, got: {stdout!r}"
            assert "supervisor" in stdout, "Sender name missing from output"
            assert "Hello from supervisor" in stdout, "Message body missing from output"
            assert "1 new message" in stdout, f"Expected '1 new message', got: {stdout!r}"
            assert OLD_REPLY_TIP_PREFIX not in stdout, "Old reply tip should not appear"

    runner.test("hook delivers unread message with correct [MAILBOX] format", test_hook_delivers_unread_message)

    def test_hook_marks_messages_read():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(body="Mark me read")
            paths = seed_mailbox(mailbox_dir, "worker-1", [msg])
            msg_file = paths[0]

            # First run: delivers
            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "[MAILBOX]" in stdout

            # File should now be marked read
            with open(msg_file, encoding="utf-8") as f:
                updated = json.load(f)
            assert updated["read"] is True, "Message should be marked read=True after delivery"

    runner.test("hook marks delivered messages as read=true in JSON file", test_hook_marks_messages_read)

    def test_hook_idempotent():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(body="Read once only")
            seed_mailbox(mailbox_dir, "worker-1", [msg])

            env = {"MECHA_WORKER_WINDOW": "worker-1", "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir}

            # First run delivers
            stdout1, _, _ = run_script(HOOK_SCRIPT, env_overrides=env, stdin_data=hook_stdin())
            assert "[MAILBOX]" in stdout1

            # Second run: already read, no output
            stdout2, _, rc = run_script(HOOK_SCRIPT, env_overrides=env, stdin_data=hook_stdin())
            assert rc == 0
            assert stdout2.strip() == "", (
                f"Second invocation should produce no output (already read), got: {stdout2!r}"
            )

    runner.test("hook is idempotent — second invocation produces no output (already read)", test_hook_idempotent)

    def test_hook_multiple_messages_format():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msgs = [
                make_message(body="First", age_seconds=10),
                make_message(body="Second", age_seconds=5),
                make_message(body="Third", age_seconds=0),
            ]
            seed_mailbox(mailbox_dir, "worker-1", msgs)

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            assert "3 new messages" in stdout, f"Expected '3 new messages', got: {stdout!r}"
            assert "Message 1 of 3" in stdout
            assert "Message 2 of 3" in stdout
            assert "Message 3 of 3" in stdout
            assert "First" in stdout
            assert "Second" in stdout
            assert "Third" in stdout

    runner.test("hook formats multiple messages with correct count and numbering", test_hook_multiple_messages_format)

    def test_hook_shows_subject():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(body="Body text", subject="Important update")
            seed_mailbox(mailbox_dir, "worker-1", [msg])

            stdout, _, _ = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "Important update" in stdout, (
                f"Subject should appear in output, got: {stdout!r}"
            )

    runner.test("hook shows subject line when present", test_hook_shows_subject)

    def test_hook_uses_agent_window_fallback():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(from_agent="worker-1", to_agent="supervisor", body="Status update")
            seed_mailbox(mailbox_dir, "supervisor", [msg])

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": None,
                    "MECHA_AGENT_WINDOW": "supervisor",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            assert "[MAILBOX]" in stdout, (
                f"Supervisor should receive messages via MECHA_AGENT_WINDOW, got: {stdout!r}"
            )
            assert "Status update" in stdout

    runner.test("hook uses MECHA_AGENT_WINDOW when MECHA_WORKER_WINDOW not set (supervisor support)", test_hook_uses_agent_window_fallback)

    def test_hook_supervisor_state_update_uses_from_alias():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(
                from_agent="wolfgangmeyers@gmail.com",
                from_alias="wolfgang",
                to_agent="supervisor",
                body="Inbound email",
            )
            seed_mailbox(mailbox_dir, "supervisor", [msg])

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": None,
                    "MECHA_AGENT_WINDOW": "supervisor",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            assert "python3 state_update_status.py wolfgang" in stdout
            assert "python3 state_update_status.py wolfgangmeyers@gmail.com" not in stdout
            assert "From: wolfgangmeyers@gmail.com" in stdout

    runner.test("hook supervisor state-update prompt uses from_alias while displaying raw email", test_hook_supervisor_state_update_uses_from_alias)

    def test_hook_mandatory_smtp_block_for_email_sender():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(from_agent="wolfgangmeyers@gmail.com", body="Needs a reply")
            seed_mailbox(mailbox_dir, "worker-1", [msg])

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            context = json.loads(stdout)["hookSpecificOutput"]["additionalContext"]
            assert SMTP_REPLY_BLOCK_HEADER in context
            assert SMTP_REPLY_BLOCK_COMMAND in context
            assert OLD_REPLY_TIP_PREFIX not in context
            assert "Tip: If you're mid-task" in context
            assert context.index("Needs a reply") < context.index(SMTP_REPLY_BLOCK_HEADER)
            assert context.index(SMTP_REPLY_BLOCK_HEADER) < context.index("Tip: If you're mid-task")

    runner.test("hook includes mandatory SMTP reply block for email senders", test_hook_mandatory_smtp_block_for_email_sender)

    def test_hook_mandatory_smtp_block_for_mixed_sender_batch():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msgs = [
                make_message(from_agent="reminders", body="Reminder"),
                make_message(from_agent="glue@agents.meyers.life", body="Email body"),
            ]
            seed_mailbox(mailbox_dir, "worker-1", msgs)

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            context = json.loads(stdout)["hookSpecificOutput"]["additionalContext"]
            assert SMTP_REPLY_BLOCK_HEADER in context
            assert SMTP_REPLY_BLOCK_COMMAND in context
            assert OLD_REPLY_TIP_PREFIX not in context

    runner.test("hook includes mandatory SMTP reply block when any sender is email", test_hook_mandatory_smtp_block_for_mixed_sender_batch)

    def test_hook_omits_mandatory_smtp_block_for_bare_senders():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msgs = [
                make_message(from_agent="reminders", body="Reminder"),
                make_message(from_agent="wolfgang", body="Local message"),
            ]
            seed_mailbox(mailbox_dir, "worker-1", msgs)

            stdout, _, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert rc == 0
            context = json.loads(stdout)["hookSpecificOutput"]["additionalContext"]
            assert SMTP_REPLY_BLOCK_HEADER not in context
            assert OLD_REPLY_TIP_PREFIX not in context
            assert "Tip: If you're mid-task" in context

    runner.test("hook omits mandatory SMTP reply block for bare senders", test_hook_omits_mandatory_smtp_block_for_bare_senders)

    def test_hook_skips_read_messages():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            read_msg = make_message(body="Already read", read=True)
            unread_msg = make_message(body="New message", read=False)
            seed_mailbox(mailbox_dir, "worker-1", [read_msg, unread_msg])

            stdout, _, _ = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "1 new message" in stdout, (
                f"Should show only 1 unread message, got: {stdout!r}"
            )
            assert "New message" in stdout
            assert "Already read" not in stdout

    runner.test("hook skips already-read messages, delivers only unread", test_hook_skips_read_messages)

    def test_hook_handles_malformed_stdin():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            env = {"MECHA_WORKER_WINDOW": "worker-1", "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir}

            # Run with non-JSON stdin
            result = subprocess.run(
                [sys.executable, HOOK_SCRIPT],
                input="not valid json {{{",
                capture_output=True,
                text=True,
                env={**os.environ, **{k: v for k, v in env.items() if v is not None}},
            )
            assert result.returncode == 0, (
                f"Hook should exit 0 even with malformed stdin, got {result.returncode}: {result.stderr}"
            )

    runner.test("hook handles malformed stdin JSON gracefully (no crash)", test_hook_handles_malformed_stdin)

    def test_hook_singular_plural():
        """'1 new message' (singular) vs 'N new messages' (plural)."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            msg = make_message(body="Just one")
            seed_mailbox(mailbox_dir, "worker-1", [msg])

            stdout, _, _ = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "1 new message:" in stdout, (
                f"Singular form should be '1 new message:', got: {stdout!r}"
            )
            assert "1 new messages:" not in stdout, "Should not use plural header for single message"

    runner.test("hook uses singular 'message' for N=1, plural 'messages' for N>1", test_hook_singular_plural)

    print("\n[Unit Tests — mailbox_cron.py internals]")

    def test_message_age_seconds_malformed_timestamp():
        """message_age_seconds must return float('inf') on parse failure, not 0 or raise.

        Returning 0 would cause malformed messages to appear fresh and never be delivered.
        Returning float('inf') ensures they're treated as maximally stale and delivered.
        """
        import importlib.util
        spec = importlib.util.spec_from_file_location("mailbox_cron", CRON_SCRIPT)
        cron = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cron)

        bad_timestamps = [
            "",                    # empty string
            "not-a-timestamp",     # garbage
            "2026-02-22T10:00:00", # naive datetime (no Z / tzinfo) — would raise TypeError on subtraction
            None,                  # None value
        ]

        for ts_value in bad_timestamps:
            msg = {"timestamp": ts_value}
            result = cron.message_age_seconds(msg)
            assert result == float("inf"), (
                f"message_age_seconds should return float('inf') for bad timestamp {ts_value!r}, "
                f"got {result!r} — messages with bad timestamps would be permanently stuck"
            )

    runner.test(
        "message_age_seconds returns float('inf') for malformed/naive timestamps (regression: not 0)",
        test_message_age_seconds_malformed_timestamp,
    )

    def test_cron_supervisor_state_update_uses_from_alias():
        import importlib.util
        spec = importlib.util.spec_from_file_location("mailbox_cron", CRON_SCRIPT)
        cron = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cron)

        msg = make_message(
            from_agent="wolfgangmeyers@gmail.com",
            from_alias="wolfgang",
            to_agent="supervisor",
            body="Inbound email",
        )
        output = cron.format_delivery_text([("/tmp/message.json", msg)], agent_name="supervisor")
        assert "python3 state_update_status.py wolfgang" in output
        assert "python3 state_update_status.py wolfgangmeyers@gmail.com" not in output
        assert "From: wolfgangmeyers@gmail.com" in output
        assert SMTP_REPLY_BLOCK_HEADER in output

    runner.test(
        "mailbox_cron supervisor state-update prompt uses from_alias while displaying raw email",
        test_cron_supervisor_state_update_uses_from_alias,
    )

    def test_cron_mandatory_smtp_block_for_email_sender():
        import importlib.util
        spec = importlib.util.spec_from_file_location("mailbox_cron", CRON_SCRIPT)
        cron = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cron)

        msg = make_message(from_agent="wolfgangmeyers@gmail.com", to_agent="worker-1", body="Needs a reply")
        output = cron.format_delivery_text([("/tmp/message.json", msg)], agent_name="worker-1")
        assert SMTP_REPLY_BLOCK_HEADER in output
        assert SMTP_REPLY_BLOCK_COMMAND in output
        assert OLD_REPLY_TIP_PREFIX not in output
        assert output.index("Needs a reply") < output.index(SMTP_REPLY_BLOCK_HEADER)

    runner.test("mailbox_cron includes mandatory SMTP reply block for email senders", test_cron_mandatory_smtp_block_for_email_sender)

    def test_cron_mandatory_smtp_block_for_mixed_sender_batch():
        import importlib.util
        spec = importlib.util.spec_from_file_location("mailbox_cron", CRON_SCRIPT)
        cron = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cron)

        msgs = [
            ("/tmp/reminder.json", make_message(from_agent="reminders", to_agent="worker-1", body="Reminder")),
            ("/tmp/glue.json", make_message(from_agent="glue@agents.meyers.life", to_agent="worker-1", body="Email body")),
        ]
        output = cron.format_delivery_text(msgs, agent_name="worker-1")
        assert SMTP_REPLY_BLOCK_HEADER in output
        assert SMTP_REPLY_BLOCK_COMMAND in output
        assert OLD_REPLY_TIP_PREFIX not in output

    runner.test("mailbox_cron includes mandatory SMTP reply block when any sender is email", test_cron_mandatory_smtp_block_for_mixed_sender_batch)

    def test_cron_omits_mandatory_smtp_block_for_bare_senders():
        import importlib.util
        spec = importlib.util.spec_from_file_location("mailbox_cron", CRON_SCRIPT)
        cron = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cron)

        msgs = [
            ("/tmp/reminder.json", make_message(from_agent="reminders", to_agent="worker-1", body="Reminder")),
            ("/tmp/wolfgang.json", make_message(from_agent="wolfgang", to_agent="worker-1", body="Local message")),
        ]
        output = cron.format_delivery_text(msgs, agent_name="worker-1")
        assert SMTP_REPLY_BLOCK_HEADER not in output
        assert OLD_REPLY_TIP_PREFIX not in output

    runner.test("mailbox_cron omits mandatory SMTP reply block for bare senders", test_cron_omits_mandatory_smtp_block_for_bare_senders)

    def test_cron_global_lock_prevents_duplicate_concurrent_delivery():
        """Concurrent cron processes must not deliver the same stale unread message twice."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            lock_file = os.path.join(tmp, ".mailbox-cron-delivery.lock")
            log_file = os.path.join(tmp, "mailbox-cron.log")
            deliveries_file = os.path.join(tmp, "deliveries.txt")

            msg = make_message(
                from_agent="supervisor",
                to_agent="worker-1",
                body=f"cron stress payload {uuid.uuid4().hex}",
                age_seconds=120,
            )
            paths = seed_mailbox(mailbox_dir, "worker-1", [msg])

            start_event = multiprocessing.Event()
            processes = [
                multiprocessing.Process(
                    target=cron_stress_worker,
                    args=(start_event, CRON_SCRIPT, mailbox_dir, lock_file, log_file, deliveries_file),
                )
                for _ in range(12)
            ]
            for proc in processes:
                proc.start()

            start_event.set()
            for proc in processes:
                proc.join(10)

            for proc in processes:
                assert proc.exitcode == 0, (
                    f"cron stress worker exited {proc.exitcode}; lock should skip contenders cleanly"
                )

            if os.path.exists(deliveries_file):
                with open(deliveries_file, encoding="utf-8") as f:
                    deliveries = f.read()
            else:
                deliveries = ""

            delivery_count = deliveries.count("---DELIVERY---")
            assert delivery_count == 1, (
                f"Expected exactly one tmux delivery across concurrent cron runs, got {delivery_count}.\n"
                f"Deliveries:\n{deliveries}"
            )

            with open(paths[0], encoding="utf-8") as f:
                persisted = json.load(f)
            assert persisted["read"] is True, "The single delivered message should be marked read"

    runner.test(
        "mailbox_cron global fcntl lock prevents duplicate concurrent stale-message delivery",
        test_cron_global_lock_prevents_duplicate_concurrent_delivery,
    )


# ---------------------------------------------------------------------------
# Live integration test
# ---------------------------------------------------------------------------


def run_live_test(runner):
    print("\n[Live Integration Test]")
    print("  Acceptance gate: proves the hook fires and injects messages,")
    print("  using the same code path that Claude Code takes for PreToolUse hooks.")

    def test_live_hook_fires_and_delivers():
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")
            agent_name = f"test-worker-{uuid.uuid4().hex[:8]}"

            # Seed mailbox with a uniquely identifiable message body
            msg_body = f"Live test message payload {uuid.uuid4().hex}"
            msg = make_message(
                from_agent="supervisor",
                to_agent=agent_name,
                body=msg_body,
                subject="Live test subject",
            )
            paths = seed_mailbox(mailbox_dir, agent_name, [msg])
            msg_file = paths[0]

            # Simulate Claude Code calling the PreToolUse hook.
            # This is exactly what Claude Code does — it spawns the hook script,
            # passes JSON on stdin, and reads stdout to inject into the model context.
            hook_payload = {
                "tool_name": "Bash",
                "tool_input": {"command": "echo live-test"},
                "session_id": "live-integration-test-session",
            }
            stdout, stderr, rc = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": agent_name,
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_payload,
            )

            # --- Assertion 1: Exit 0 (hook never blocks) ---
            assert rc == 0, (
                f"Hook must exit 0 (non-blocking). Got exit {rc}.\nstderr: {stderr}"
            )

            # --- Assertion 2: [MAILBOX] present in stdout (injected into model context) ---
            assert "[MAILBOX]" in stdout, (
                f"[MAILBOX] prefix not found in hook stdout.\n"
                f"stdout: {stdout!r}\n"
                f"stderr: {stderr!r}\n"
                f"This means the hook fired but did not inject the message."
            )

            # --- Assertion 3: Message body present in stdout ---
            assert msg_body in stdout, (
                f"Message body not found in hook stdout.\n"
                f"Expected body: {msg_body!r}\n"
                f"Got stdout: {stdout!r}"
            )

            # --- Assertion 4: Subject present in stdout ---
            assert "Live test subject" in stdout, (
                f"Subject not found in hook stdout: {stdout!r}"
            )

            # --- Assertion 5: Message file marked read=true ---
            with open(msg_file, encoding="utf-8") as f:
                persisted = json.load(f)
            assert persisted["read"] is True, (
                f"Message file should have read=true after delivery, got: {persisted['read']!r}"
            )

            # --- Assertion 6: Idempotency — second call produces no output ---
            stdout2, stderr2, rc2 = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": agent_name,
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_payload,
            )
            assert rc2 == 0
            assert stdout2.strip() == "", (
                f"Second hook invocation should produce no output (message already read). "
                f"Got: {stdout2!r}"
            )

    runner.test(
        "LIVE: hook fires → injects [MAILBOX] to stdout → marks read → idempotent",
        test_live_hook_fires_and_delivers,
    )

    def test_live_multiple_recipients():
        """Two agents each have a message; hook delivers only to the correct recipient."""
        with tempfile.TemporaryDirectory() as tmp:
            mailbox_dir = os.path.join(tmp, "mailboxes")

            msg_for_worker1 = make_message(
                from_agent="supervisor", to_agent="worker-1", body="Message for worker-1"
            )
            msg_for_worker2 = make_message(
                from_agent="supervisor", to_agent="worker-2", body="Message for worker-2"
            )
            seed_mailbox(mailbox_dir, "worker-1", [msg_for_worker1])
            seed_mailbox(mailbox_dir, "worker-2", [msg_for_worker2])

            # Hook running as worker-1 should only see worker-1's message
            stdout1, _, _ = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-1",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "Message for worker-1" in stdout1
            assert "Message for worker-2" not in stdout1, (
                "Worker-1's hook should not see worker-2's message"
            )

            # Hook running as worker-2 should only see worker-2's message
            stdout2, _, _ = run_script(
                HOOK_SCRIPT,
                env_overrides={
                    "MECHA_WORKER_WINDOW": "worker-2",
                    "MECHA_MAILBOX_DIR_OVERRIDE": mailbox_dir,
                },
                stdin_data=hook_stdin(),
            )
            assert "Message for worker-2" in stdout2
            assert "Message for worker-1" not in stdout2

    runner.test(
        "LIVE: each agent only receives its own messages (mailbox isolation)",
        test_live_multiple_recipients,
    )


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main():
    parser = argparse.ArgumentParser(description="Mailbox system test suite.")
    parser.add_argument(
        "--unit-only", action="store_true", help="Run only unit tests"
    )
    parser.add_argument(
        "--live-only", action="store_true", help="Run only live integration test"
    )
    args = parser.parse_args()

    # Verify required scripts exist before running tests
    missing = []
    for path, name in [
        (HOOK_SCRIPT, "hooks/mailbox-deliver.py"),
        (SEND_SCRIPT, "mailbox_send.py"),
        (CRON_SCRIPT, "mailbox_cron.py"),
    ]:
        if not os.path.isfile(path):
            missing.append(f"  Missing: {path} ({name})")

    if missing:
        print("ERROR: Required scripts not found:")
        for m in missing:
            print(m)
        sys.exit(1)

    runner = TestRunner()

    if not args.live_only:
        run_unit_tests(runner)

    if not args.unit_only:
        run_live_test(runner)

    success = runner.summary()
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    import subprocess
    main()
