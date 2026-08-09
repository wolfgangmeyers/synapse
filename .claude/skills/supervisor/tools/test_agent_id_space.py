"""The dotted-id boundary between self-registered sessions and mailbox agents.

Self-registered Claude sessions exist to show presence in the router UI. They are not mailbox
addresses. They use dotted agent ids so they cannot collide with a managed worker's id, which is
its tmux window name and can never contain a dot.

The protocol itself allows dots (protocol/v1/schema.json, $defs.agentId). This validator is
deliberately narrower. Widening it to accept dots merges the two id spaces, and an ad hoc session
could then register under a live worker's name. These tests are the tripwire for that.
"""

import unittest

from _tmux import _AGENT_ID


class AgentIdSpaceTest(unittest.TestCase):
    def test_dotted_ids_are_rejected(self):
        for candidate in (
            "session.claude-activity-indicators.4821",
            "session.supervisor.1",
            "a.b",
        ):
            with self.subTest(candidate=candidate):
                self.assertIsNone(_AGENT_ID.fullmatch(candidate))

    def test_managed_worker_names_are_accepted(self):
        for candidate in ("supervisor", "claude-activity-indicators", "van-770-eval-gate", "a"):
            with self.subTest(candidate=candidate):
                self.assertIsNotNone(_AGENT_ID.fullmatch(candidate))


if __name__ == "__main__":
    unittest.main()
