"""Owned candidate-node deployment checks; no Bot tasks or approvals."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('node_launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class CandidateNodeTests(unittest.TestCase):
    def invoke(self, args):
        with tempfile.TemporaryDirectory(prefix='candidate node ') as tmp:
            root = Path(tmp)
            fake = root / 'docker-fake'
            fake.write_text('#!/usr/bin/env python3\nimport json,sys\nprint(json.dumps(sys.argv[1:]))\n')
            fake.chmod(0o755)
            wrapper = root / 'wrapper'
            import shlex
            wrapper.write_text(launcher.wrapper('owned-org', '/owned alias', '/layers spaces', root / 'astonish').replace('/opt/docker/docker', shlex.quote(str(fake))))
            result = subprocess.run(['bash', str(wrapper), *args], text=True, capture_output=True)
            return result, str(root / 'astonish')

    def test_candidate_mounted_readonly_for_owned_launch(self):
        result, candidate = self.invoke(['container', 'run', '--label=astonish.org=owned-org', '--volume=/owned alias:/mnt/astonish-layers:ro', 'image'])
        self.assertEqual(result.returncode, 0, result.stderr)
        args = json.loads(result.stdout)
        self.assertEqual(args[:3], ['container', 'run', '--mount=type=bind,source=' + candidate + ',target=/usr/local/bin/astonish-host,readonly'])
        self.assertIn('--volume=/layers spaces:/mnt/astonish-layers:ro', args)
        self.assertEqual(args[-1], 'image')

    def test_other_org_launch_rejected(self):
        result, _ = self.invoke(['container', 'run', '--label=astonish.org=other', 'image'])
        self.assertEqual(result.returncode, 64)
        self.assertFalse(result.stdout)

    def test_exec_unmodified(self):
        args = ['exec', 'session', '/usr/local/bin/astonish-shell', 'astonish', 'node']
        result, _ = self.invoke(args)
        self.assertEqual(json.loads(result.stdout), args)

    def test_list_keeps_owner_filter(self):
        result, _ = self.invoke(['ps', '--all'])
        self.assertEqual(json.loads(result.stdout), ['ps', '--filter', 'label=astonish.org=owned-org', '--all'])
