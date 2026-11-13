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


def agent_config():
    config = {k: '/explicit/path' for k in launcher.PATH_CONFIG_KEYS}
    config.update(schema_version=1, provider_instance=launcher.AGENT_TARGET['provider'],
                  model=launcher.AGENT_TARGET['model'], intended=dict(launcher.AGENT_TARGET),
                  provider_config={'type': 'sap_ai_core', 'base_url': 'https://api.example.test',
                                   'auth_url': 'https://auth.example.test', 'client_id': 'test-client',
                                   'client_secret': 'SECRET_SENTINEL', 'resource_group': 'test-group'})
    return config


class ProviderStartupTests(unittest.TestCase):
    def test_versioned_provider_and_legacy_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / 'binary'
            executable.write_bytes(b'\x7fELF' + bytes(14) + b'\xb7\x00')
            config = agent_config()
            config.update(binary=str(executable), docker=str(executable), embedding_cache=str(root))
            with patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'EMBEDDING_HASHES', {}), patch.dict(launcher.os.environ, {launcher.CANDIDATE_PROVIDER_ENV: 'stale'}):
                env = launcher.config_environment(config)
                settings = launcher.provider_settings(env)
                self.assertEqual(settings['default_provider'], launcher.AGENT_TARGET['provider'])
                self.assertEqual(settings['providers'][settings['default_provider']], config['provider_config'])
                self.assertEqual(env['LOCAL_BOT_PROVIDER_API_KEY'], '')
                legacy = {k: '/explicit/path' for k in launcher.CONFIG_KEYS}
                legacy.update(binary=str(executable), docker=str(executable), embedding_cache=str(root),
                              provider_url='http://localhost:4000', provider_api_key='legacy-key', model='legacy-model')
                settings = launcher.provider_settings(launcher.config_environment(legacy))
                self.assertEqual(settings['default_provider'], 'local-litellm')
                self.assertEqual(settings['default_model'], 'legacy-model')
        self.assertEqual(launcher.provider_settings({})['default_provider'], 'local-litellm')

    def test_versioned_config_rejects_unknown_missing_and_alias_parity(self):
        from copy import deepcopy
        valid = agent_config()
        launcher.require_agent_config(valid)
        cases = []
        for key in valid:
            config = deepcopy(valid); del config[key]; cases.append(config)
        for version in (True, 2, '1'):
            config = deepcopy(valid); config['schema_version'] = version; cases.append(config)
        config = deepcopy(valid); config['provider_config']['client_secret'] = ''; cases.append(config)
        config = deepcopy(valid); config['provider_config']['unknown'] = 'SECRET_SENTINEL'; cases.append(config)
        config = deepcopy(valid); config['provider_instance'] = 'alias'; cases.append(config)
        config = deepcopy(valid); config['provider_config']['base_url'] = 'https://user:SECRET_SENTINEL@example.test'; cases.append(config)
        for config in cases:
            with self.assertRaises(ValueError) as error:
                launcher.candidate_provider(config)
            self.assertNotIn('SECRET_SENTINEL', str(error.exception))
        config = deepcopy(valid)
        config['provider_instance'] = config['intended']['provider'] = 'alias'
        launcher.candidate_provider(config)  # Named instances work, but are not target parity.
        with self.assertRaisesRegex(ValueError, 'aliases are not parity'):
            launcher.require_agent_config(config)

    def test_ai_core_staging_is_private_not_runtime_parity(self):
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory); binary = repo / 'candidate'; binary.write_bytes(b'candidate')
            config = agent_config()
            env = {'LOCAL_BOT_BINARY': str(binary), 'LOCAL_BOT_DOCKER': str(binary),
                   'LOCAL_BOT_LAYERS': str(repo / 'layers'),
                   launcher.CANDIDATE_PROVIDER_ENV: json.dumps(launcher.candidate_provider(config))}
            with patch('native_candidate_staging.stage', return_value={'candidate_image_id': 'sha256:native'}), patch.object(launcher, 'REPO', repo), patch.object(launcher.subprocess, 'check_output', return_value='owned-container\n'):
                state = launcher.create(env)
            root = Path(state['root'])
            path = root / 'config/astonish/config.yaml'
            staged = yaml.safe_load(path.read_text())
            self.assertEqual(staged['providers'][config['provider_instance']], config['provider_config'])
            self.assertEqual(staged['general']['default_model'], config['model'])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            receipt = json.loads((root / 'provider-startup-receipt.json').read_text())
            self.assertEqual(receipt['provider_type'], 'sap_ai_core')
            self.assertFalse(receipt['runtime_parity_verified'])
            for name in ('provider-startup-receipt.json', 'state.json', 'daemon.env'):
                self.assertNotIn('SECRET_SENTINEL', (root / name).read_text())

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
