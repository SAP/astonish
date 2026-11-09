"""Provider staging boundary, not live acceptance evidence."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import yaml

spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class ProviderStartupTests(unittest.TestCase):
    def test_explicit_provider_staged_before_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            binary = repo / 'candidate'
            binary.write_bytes(b'candidate')
            env = {'LOCAL_BOT_BINARY': str(binary), 'LOCAL_BOT_DOCKER': str(binary),
                   'LOCAL_BOT_LAYERS': str(repo / 'layers'),
                   'LOCAL_BOT_PROVIDER_URL': 'http://authorized.example:4000/v1',
                   'LOCAL_BOT_PROVIDER_API_KEY': 'quoted " key\\with\nnewline',
                   'LOCAL_BOT_MODEL': 'explicit:model "\\\nname',
                   'LOCAL_BOT_ROUTINE_POLICY_JSON': json.dumps({'require_approval': ['memory_forget']})}
            def start(args, **kwargs):
                config_path = next((repo / '.tmp').glob('*/config/astonish/config.yaml'))
                config = yaml.safe_load(config_path.read_text())
                self.assertEqual(config_path.stat().st_mode & 0o777, 0o600)
                self.assertEqual(config['general'], {'default_provider': 'local-litellm', 'default_model': env['LOCAL_BOT_MODEL']})
                self.assertEqual(config['providers']['local-litellm'], {'type': 'litellm', 'base_url': env['LOCAL_BOT_PROVIDER_URL'], 'api_key': env['LOCAL_BOT_PROVIDER_API_KEY']})
                self.assertEqual(config['bot_routine_policy']['require_approval'], ['memory_forget'])
                receipt = config_path.parents[2] / 'provider-startup-receipt.json'
                self.assertTrue(json.loads(receipt.read_text())['staged_before_startup'])
                self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)
                self.assertNotIn(env['LOCAL_BOT_PROVIDER_API_KEY'], receipt.read_text())
                self.assertEqual(args[-3:], ['sha256:native', 'daemon', 'run'])
                self.assertFalse(any('target=/opt/astonish,' in arg or 'target=/opt/docker/docker,' in arg for arg in args))
                daemon_env = (config_path.parents[2] / 'daemon.env').read_text()
                self.assertIn('ASTONISH_SANDBOX_IMAGE=sha256:native\n', daemon_env)
                self.assertIn('PATH=/opt/astonish-launcher/bin:', daemon_env)
                return 'owned-container\n'
            with patch('native_candidate_staging.stage', return_value={'candidate_image_id': 'sha256:native'}), patch.object(launcher, 'REPO', repo), patch.object(launcher.subprocess, 'check_output', side_effect=start) as docker:
                state = launcher.create(env)
            self.assertEqual(docker.call_count, 1)
            self.assertNotIn(env['LOCAL_BOT_PROVIDER_API_KEY'], (Path(state['root']) / 'state.json').read_text())

    def test_partial_explicit_provider_fails_closed(self):
        with self.assertRaisesRegex(ValueError, 'explicit provider'):
            launcher.provider_settings({'LOCAL_BOT_PROVIDER_URL': 'http://authorized.example'})


if __name__ == '__main__':
    unittest.main()
