#!/usr/bin/env python3
"""Shared reminder storage tests."""

import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch

import _reminders


class ReminderStorageTest(unittest.TestCase):
    def test_save_uses_a_uuid4_atomic_temporary_filename(self):
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "reminders.json"
            replaced = []
            with patch.object(_reminders, "reminders_file", return_value=destination), patch.object(
                _reminders.os, "replace", side_effect=lambda source, target: replaced.append((source, target)),
            ):
                _reminders.save_reminders([])

            temporary, target = replaced[0]
            self.assertEqual(target, destination)
            self.assertTrue(temporary.name.startswith(f".{destination.name}.{_reminders.os.getpid()}."))
            self.assertEqual(uuid.UUID(temporary.name.split(".")[-2]).version, 4)


if __name__ == "__main__":
    unittest.main()
