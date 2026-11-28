import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('refresh_launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class BrowserRefreshTests(unittest.TestCase):
    def test_refresh_preserves_candidate_and_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root/'bin').mkdir()
            (root/'bin/chat-first-browser').write_bytes(b'old')
            cp = dict(Candidate='candidate', BotID='bot', IDs={'write':'run'}, Consents={'approval':'digest'}, Sent={'artifact-load':True}, Before={'artifact-load':{'proposal':True}})
            original=json.dumps(cp).encode(); (root/'coherent-checkpoint.json').write_bytes(original)
            state=dict(root=str(root), binary_sha256='candidate', bot_id='bot', container_id='container', browser_sha256=hashlib.sha256(b'old').hexdigest(), status='blocked')
            old=copy.deepcopy(state)
            def build(*args, **kwargs): (root/'bin/chat-first-browser.pending').write_bytes(b'new')
            with patch.object(launcher,'inspect_owned'), patch.object(launcher,'assert_browser_idle'), patch.object(launcher.subprocess,'run',side_effect=build):
                launcher.refresh_browser(state)
            self.assertEqual((root/'coherent-checkpoint.json').read_bytes(), original)
            self.assertEqual({k:v for k,v in state.items() if k!='browser_sha256'}, {k:v for k,v in old.items() if k!='browser_sha256'})
            receipt=json.loads(next(root.glob('browser-refresh-*.json')).read_text())
            self.assertEqual(receipt['old_hash'],old['browser_sha256']); self.assertEqual(receipt['new_hash'],state['browser_sha256'])
    def test_active_browser_blocks(self):
        with patch.object(launcher.subprocess,'check_output',return_value='/owned/bin/chat-first-browser\n'):
            with self.assertRaises(RuntimeError): launcher.assert_browser_idle(Path('/owned'))
    def test_active_profile_blocks(self):
        with patch.object(launcher.subprocess,'check_output',return_value='chrome --user-data-dir=/owned/chrome-profile\n'):
            with self.assertRaises(RuntimeError): launcher.assert_browser_idle(Path('/owned'))
