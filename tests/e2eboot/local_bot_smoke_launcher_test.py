#!/usr/bin/env python3
"""Unit safety checks; these are not live acceptance evidence."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class LauncherTests(unittest.TestCase):
    def test_agent_mode_launches_fresh_journey_but_diagnostic_is_not_acceptance(self):
        import io
        import json
        import hashlib
        from provider_startup_launcher_test import agent_config
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'config.json'
            path.write_text(json.dumps(agent_config())); path.chmod(0o600)
            (root / 'bin').mkdir()
            (root / 'bin/chat-first-browser').write_bytes(b'candidate-browser')
            state = {'root': str(root), 'binary_sha256': 'a' * 64,
                     'browser_sha256': hashlib.sha256(b'candidate-browser').hexdigest()}
            output = io.StringIO()
            def run(command, **kwargs):
                self.assertEqual(command, [str(root / 'bin/chat-first-browser')])
                self.assertEqual(kwargs['env']['BOT_AGENT_ACCEPTANCE'], '1')
                self.assertNotIn('BOT_QUALITY_ACCEPTANCE', kwargs['env'])
                staged = json.loads((root / 'state.json').read_text())
                self.assertEqual(staged['agent_intended'], launcher.AGENT_TARGET)
                self.assertEqual(len(staged['agent_nonce']), 64)
                launcher.private_json(root / 'browser-evidence.json', {
                    'contract': 'bot-agent-live-v1', 'status': 'blocked',
                    'stage': 'agent-proof-obligations', 'unverified': ['adapter-provenance'],
                    'blocker': 'candidate samples retained; full proof unavailable'})
            with patch.object(launcher, 'config_environment', return_value={}), patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'create', return_value=state) as create, patch.object(launcher, 'setup') as setup, patch.object(launcher.subprocess, 'run', side_effect=run) as browser, patch.dict(launcher.os.environ, {'BOT_QUALITY_ACCEPTANCE': '1'}), patch('sys.stderr', output):
                self.assertEqual(launcher.main(['--verify-bot-agent', '--config', str(path)]), 1)
            create.assert_called_once(); setup.assert_called_once(); browser.assert_called_once()
            self.assertEqual(json.loads((root / 'state.json').read_text())['status'], 'blocked')
            self.assertIn('full proof unavailable', output.getvalue())
            self.assertNotIn('SECRET_SENTINEL', output.getvalue())

    def test_agent_rejects_resume_legacy_config_and_public_secrets(self):
        import io
        from provider_startup_launcher_test import agent_config
        import json
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            for text, mode, extra in [('{}', 0o600, []),
                                       (json.dumps(agent_config()), 0o644, []),
                                       (json.dumps(agent_config()), 0o600, ['--state', '/not-owned']),
                                       ('{"schema_version":1,"schema_version":"SECRET_SENTINEL"}', 0o600, [])]:
                path.write_text(text); path.chmod(mode)
                output = io.StringIO()
                with patch.object(launcher, 'create') as create, patch('sys.stderr', output), self.assertRaises(SystemExit) as error:
                    launcher.main(['--verify-bot-agent', '--config', str(path)] + extra)
                self.assertEqual(error.exception.code, 2)
                create.assert_not_called()
                self.assertNotIn('SECRET_SENTINEL', output.getvalue())

    def test_task_run_projection_does_not_promote_private_outer_task(self):
        self.assertIn('task', launcher.EMPLOYEE_RUN_FIELDS)
        errors = []
        # botStoredRun.task is a private revision, not the compact Run.task ref.
        row = dict(id='run', bot_id='bot', data={'Run': {'id':'run', 'bot_id':'bot', 'session_id':'session'},
                                               'task': {'session_id': 'private-session'}})
        self.assertEqual(launcher.employee_run_session(row, errors), 'session')
        self.assertEqual(errors, [])
        del row['data']['Run']['session_id']
        self.assertIsNone(launcher.employee_run_session(row, errors))
        self.assertEqual(errors, [])
        row['data']['session_id'] = 'private-session'
        self.assertIsNone(launcher.employee_run_session(row, errors))
        self.assertTrue(errors)

    def test_task_mode_fresh_exclusive_and_blocked(self):
        import io, json, hashlib
        from provider_startup_launcher_test import task_config
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); path=root/'config.json'
            path.write_text(json.dumps(task_config())); path.chmod(0o600)
            (root/'bin').mkdir(); (root/'bin/chat-first-browser').write_bytes(b'browser')
            state=dict(root=str(root), binary_sha256='a'*64, browser_sha256=hashlib.sha256(b'browser').hexdigest())
            def run(command, **kwargs):
                env=kwargs['env']; self.assertEqual(env['BOT_TASK_ACCEPTANCE'], '1')
                self.assertNotIn('BOT_AGENT_ACCEPTANCE', env)
                self.assertEqual(env['LOCAL_BOT_TASK_CONFIG'], str(path))
                staged=json.loads((root/'state.json').read_text())
                self.assertEqual(staged['task_intended'], launcher.TASK_TARGET)
                self.assertTrue(staged['task_config_verified']); self.assertEqual(len(staged['task_nonce']),64)
                launcher.private_json(root/'browser-evidence.json', dict(contract='bot-task-live-v1', status='blocked',
                    stage='task-proof-obligations', unverified=['workspace'], blocker='lifecycle gates outstanding'))
            output=io.StringIO()
            with patch.object(launcher,'task_config_environment',return_value={}), patch.object(launcher,'prerequisites',return_value=[]), patch.object(launcher,'create',return_value=state) as create, patch.object(launcher,'setup'), patch.object(launcher.subprocess,'run',side_effect=run), patch.dict(launcher.os.environ,{'BOT_AGENT_ACCEPTANCE':'1'}), patch('sys.stderr',output):
                self.assertEqual(launcher.main(['--verify-bot-tasks','--config',str(path)]),1)
            create.assert_called_once(); self.assertIn('gates outstanding',output.getvalue())
            self.assertEqual(json.loads((root/'state.json').read_text())['status'],'blocked')
            for extra in (['--state','/not-owned'],['--verify-bot-agent'],['--verify-bot-quality'],['--verify-bot-skills'],['--verify-chat-first'],['--refresh-browser']):
                with patch.object(launcher,'create') as create, patch('sys.stderr',io.StringIO()), self.assertRaises(SystemExit):
                    launcher.main(['--verify-bot-tasks','--config',str(path)]+extra)
                create.assert_not_called()
            for text,mode in [('{}',0o600),(json.dumps(task_config()),0o644),('{"schema_version":1,"schema_version":"SECRET_SENTINEL"}',0o600)]:
                path.write_text(text);path.chmod(mode);output=io.StringIO()
                with patch.object(launcher,'create') as create, patch('sys.stderr',output),self.assertRaises(SystemExit):
                    launcher.main(['--verify-bot-tasks','--config',str(path)])
                create.assert_not_called();self.assertNotIn('SECRET_SENTINEL',output.getvalue())

    def test_task_physical_reader_real_bytes_links_and_traversal(self):
        import json, subprocess, sys, uuid, base64
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);task=str(uuid.uuid4());publication=str(uuid.uuid4())
            folder=root/'tasks'/task;files=folder/'.revisions'/publication;files.mkdir(parents=True)
            (root/'.workspace-lock').write_bytes(b'');(files/'TASK.md').write_bytes(b'PHYSICAL BYTES')
            (folder/'.head').write_text(json.dumps(dict(task_path='tasks/'+task,publication_id=publication,binding_key='binding',entries=[dict(path='TASK.md')])))
            request=dict(binding='binding',tasks=[dict(id=task,publication_id=publication,assets=['TASK.md'])])
            program=launcher.TASK_PHYSICAL_READER.replace("'/mnt/astonish-bot-home'",repr(str(root)))
            def read():return subprocess.run([sys.executable,'-I','-c',program],input=json.dumps(request),text=True,capture_output=True)
            result=read();self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(base64.b64decode(json.loads(result.stdout)[0]['bytes']),b'PHYSICAL BYTES')
            (files/'TASK.md').unlink();(files/'TASK.md').symlink_to(root/'.workspace-lock')
            self.assertNotEqual(read().returncode,0)
            request['tasks'][0]['assets']=['../escape'];self.assertNotEqual(read().returncode,0)

    def test_task_registry_observer_uses_physical_transport_not_archival_bundle(self):
        import json
        from employee_namespace_reader import Row
        from unittest.mock import MagicMock
        bot='00000000-0000-0000-0000-000000000001';task='00000000-0000-0000-0000-000000000002'
        head=dict(task_id=task,revision=1,digest='d'*64)
        revision=dict(id='publication',bot_id=bot,ref=head,manifest=dict(assets=[dict(path='TASK.md')]),bundle={'TASK.md':'ARCHIVAL'})
        reader=MagicMock();reader.connect.return_value.__enter__.return_value=reader
        reader.execute.side_effect=[[],[],[Row(id=task,bot_id=bot,key='key',title='title',status='ready',revision=1,
            home_path='tasks/'+task,head_revision=1,head_digest='d'*64,created_at='date',updated_at='date')],
            [Row(id='publication',data=json.dumps({'bot_document':json.dumps({'Publication':{'candidate':revision}})}))]]
        state=dict(root='/owned',workflow='bot-task-live-v1',task_config_verified=True,bot_id=bot)
        observed=dict(observation_complete=True,database='/owned/data/one.db',bots=[dict(home_binding=json.dumps({'scope':{'bot_id':bot}}))])
        physical=[dict(task_id=task,path='TASK.md',bytes='PHYSICAL')]
        with patch.object(launcher,'inspect_owned',return_value={'State':{'StartedAt':'now'}}),patch.object(launcher,'employee_readonly',return_value=observed),patch('employee_namespace_reader.Reader',return_value=reader),patch.object(launcher,'task_physical_files',return_value=physical) as transport:
            result=launcher.task_readonly(state)
        self.assertEqual(result['physical_files'],physical)
        self.assertEqual(result['revisions'][0]['bundle']['TASK.md'],'ARCHIVAL')
        self.assertEqual(transport.call_args.args[2],[dict(id=task,publication_id='publication',assets=['TASK.md'])])
        reader.close.assert_called_once()

    def test_task_physical_volume_no_adoption_or_host_fallback(self):
        import json,hashlib
        scope=dict(user_id='owner',bot_id='bot');key=hashlib.sha256(json.dumps(scope,separators=(',',':')).encode()).hexdigest()
        binding=dict(scope=scope,backend='docker',ref='astonish-bot-home-'+key)
        with patch.object(launcher.subprocess,'check_output',return_value=json.dumps([dict(Name='foreign',Driver='local',Labels={},Options={})])),patch.object(launcher.subprocess,'run') as run:
            with self.assertRaisesRegex(RuntimeError,'ownership mismatch'):launcher.task_physical_files(dict(bot_id='bot'),binding,[])
            run.assert_not_called()
        with self.assertRaises(RuntimeError):launcher.task_readonly(dict(workflow='legacy'))

    def test_missing_prerequisites_fail_closed(self):
        errors = launcher.prerequisites({})
        for key in ('LOCAL_BOT_BINARY', 'LOCAL_BOT_DOCKER', 'ASTONISH_BOT_BROWSER_PATH', 'LOCAL_BOT_LAYERS'):
            self.assertTrue(any(key in e for e in errors))

    def test_private_checkpoint(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'state.json'
            launcher.private_json(path, {'status': 'blocked'})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_unowned_root_rejected_before_docker(self):
        with patch.object(launcher.subprocess, 'check_output') as command:
            with self.assertRaisesRegex(RuntimeError, 'owned smoke root'):
                launcher.inspect_owned({'root': '/tmp/not-owned'})
            command.assert_not_called()

    def test_whole_journey_required(self):
        self.assertEqual(len(launcher.REQUIRED_EVIDENCE), 9)
        self.assertIn('paused-restart-bytes', launcher.REQUIRED_EVIDENCE)
        self.assertIn('exact-approval-continued', launcher.REQUIRED_EVIDENCE)
        self.assertIn('scheduled-tick-return', launcher.REQUIRED_EVIDENCE)

    def test_preserves_named_browser_blocker(self):
        import json
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            state = root / 'state.json'
            launcher.private_json(state, {'root': str(root), 'bot_id': 'existing'})
            launcher.private_json(root / 'browser-evidence.json', {'stage': 'tool-task-confirm', 'blocker': 'task confirmation rejected'})
            with patch.object(launcher, 'prerequisites', return_value=[]), patch.object(launcher, 'setup'), patch.object(launcher.subprocess, 'run', side_effect=RuntimeError('generic process failed')):
                self.assertEqual(launcher.main(['--verify-chat-first', '--state', str(state)]), 1)
            saved = json.loads(state.read_text())
            self.assertEqual(saved['blocked_stage'], 'tool-task-confirm')
            self.assertEqual(saved['blocker'], 'task confirmation rejected')
            self.assertEqual(saved['bot_id'], 'existing')

    def test_wrapper_filters_own_org_and_preserves_spaces(self):
        script = launcher.wrapper('astonish-chat-first-unit', '/tmp/own alias', '/tmp/layers spaces')
        self.assertIn('label=astonish.org=astonish-chat-first-unit', script)
        self.assertIn("alias='/tmp/own alias'", script)
        self.assertIn("layers='/tmp/layers spaces'", script)
        self.assertNotIn('docker stop', script)
        self.assertNotIn('docker rm', script)

if __name__ == '__main__': unittest.main()
