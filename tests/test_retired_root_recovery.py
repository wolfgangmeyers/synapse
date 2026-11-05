import hashlib
import json
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

RUNNER = Path(__file__).resolve().parents[1] / 'tools/managed_root_control.py'


class RetiredRootRecoveryTest(unittest.TestCase):
    def test_fingerprint_match_and_mismatches(self):
        with tempfile.TemporaryDirectory() as home:
            directory = Path(home) / 'root-control'
            directory.mkdir()
            saved = {'status': 'error', 'content': 'old failure', 'details': {},
                     'error_code': 'OLD_ERROR', 'error_message': 'old failure'}
            original = {'operation_id': 'ops.worker', 'arguments': {'action': 'send'},
                        'fence': {'agent_id': 'agent', 'session_epoch': 1}}
            fingerprint = hashlib.sha256(json.dumps(original, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()
            (directory / 'requests.json').write_text(json.dumps({
                'version': 1, 'settings': {}, 'requests': {
                    'saved': {'fingerprint': fingerprint, 'recorded_at': time.time(), 'result': saved}}}))
            cases = [(original, saved),
                     ({**original, 'operation_id': 'ops.state'}, None),
                     ({**original, 'arguments': {}}, None),
                     ({**original, 'fence': {'agent_id': 'agent', 'session_epoch': 2}}, None)]
            for request, expected in cases:
                with self.subTest(request=request):
                    completed = subprocess.run([sys.executable, str(RUNNER), 'reconcile', '--home', home],
                                               input=json.dumps({'request_id': 'saved', **request}),
                                               text=True, capture_output=True, timeout=10)
                    self.assertEqual(completed.returncode, 0, completed.stderr)
                    actual = json.loads(completed.stdout)
                    self.assertEqual(actual, expected) if expected else self.assertEqual(actual['error_code'], 'ROOT_OPERATION_UNKNOWN')


if __name__ == '__main__':
    unittest.main()
