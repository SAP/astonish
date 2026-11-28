"""Opt-in controlled WAL drill; no daemon/API/provider/existing database."""
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
import uuid

import local_bot_smoke_launcher as launcher


@unittest.skipUnless(os.environ.get('RUN_EMPLOYEE_NAMESPACE_DRILL') == '1', 'explicit owned Docker drill opt-in')
class EmployeeNamespaceDrill(unittest.TestCase):
    def test_live_writer_commits_are_observed_in_owned_namespace(self):
        root = Path(tempfile.mkdtemp(prefix='astonish-chat-first-namespace-', dir=launcher.REPO / '.tmp'))
        name = 'astonish-chat-first-namespace-' + uuid.uuid4().hex[:12]
        bot, peer = str(uuid.uuid4()), str(uuid.uuid4())
        subprocess.run(['go', 'build', '-o', str(root / 'astonish'), './tests/e2eboot/readhelper/testdata/writer.go'],
                       cwd=launcher.REPO, env=dict(os.environ, GOOS='linux', GOARCH='arm64', CGO_ENABLED='0'), check=True, timeout=120)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); port = sock.getsockname()[1]
        image = subprocess.check_output(['docker', 'image', 'inspect', launcher.IMAGE, '--format', '{{.Id}}'], text=True).strip()
        cid = subprocess.check_output(['docker', 'create', '--name', name,
            '--label', 'astonish.chat-first-smoke=' + name,
            '--label', 'astonish.namespace-drill=' + name,
            '--network', 'none', '-p', f'127.0.0.1:{port}:9393',
            '--mount', f'type=bind,source={root},target={root}',
            '--entrypoint', '/tmp/namespace-writer', image, str(root), bot, peer], text=True).strip()
        state = dict(root=str(root), bot_id=bot, container=name, container_id=cid,
                     binary_sha256=hashlib.sha256((root / 'astonish').read_bytes()).hexdigest(), port=port)
        records = []
        def execute(*args):
            launcher.inspect_owned(state)
            return subprocess.check_output(['docker', 'exec', cid, *args], text=True, timeout=15)
        def hashes():
            return execute('sha256sum', str(root / 'data/fixture.db'), str(root / 'data/fixture.db-wal'))
        try:
            subprocess.run(['docker', 'cp', str(root / 'astonish'), cid + ':/tmp/namespace-writer'], check=True, timeout=20, stdout=subprocess.DEVNULL)
            subprocess.run(['docker', 'start', cid], check=True, timeout=20, stdout=subprocess.DEVNULL)
            for version in (1, 2, 3, 4):
                if version > 1:
                    # Only the fresh fixture's control file, never SQL against product data.
                    execute('sh', '-c', 'printf %s "$1" > /tmp/namespace-command', 'control', str(version))
                deadline = time.monotonic() + 15
                while True:
                    ack = execute('sh', '-c', 'if test -f /tmp/namespace-ack; then read value < /tmp/namespace-ack; printf %s "$value"; fi').strip()
                    if ack == str(version): break
                    self.assertLess(time.monotonic(), deadline, 'controlled writer did not commit')
                    time.sleep(.05)
                before = hashes()
                canonical = launcher.employee_readonly(state)
                self.assertTrue(canonical['observation_complete'])
                self.assertEqual(canonical['bots'][0]['version'], version)
                self.assertEqual([j['id'] for j in canonical['jobs']], ['owned-job'])
                self.assertEqual(canonical['jobs'][0]['status'], str(version))
                self.assertNotIn('SECRET', json.dumps(canonical))
                self.assertEqual(before, hashes(), 'observer changed DB or WAL bytes')
                peer_read = launcher.employee_readonly(dict(state, bot_id=peer))
                self.assertEqual([j['id'] for j in peer_read['jobs']], ['peer-job'])
                self.assertEqual(peer_read['bots'][0]['version'], 99)
                try:
                    host = launcher.employee_readonly_local(state)
                    host_value = dict(version=host['bots'][0]['version'], job_status=host['jobs'][0]['status'])
                except Exception as error:
                    host_value = dict(error=type(error).__name__)
                records.append(dict(writer_committed=version, canonical_version=canonical['bots'][0]['version'],
                                    canonical_job_status=canonical['jobs'][0]['status'], host=host_value,
                                    source=canonical['observation_source'], db_wal_unchanged=True))
            # Force a commit between two reads of the SAME container transaction.
            from employee_namespace_reader import Reader
            reader = Reader(launcher.REPO, root, launcher.inspect_owned(state))
            try:
                with reader.connect((root / 'data/fixture.db').as_uri() + '?mode=ro', uri=True) as db:
                    db.execute('BEGIN')
                    snapshot_version = db.execute('SELECT version FROM bots WHERE id=?', (bot,))[0][0]
                    execute('sh', '-c', 'printf 5 > /tmp/namespace-command')
                    deadline = time.monotonic() + 15
                    while execute('sh', '-c', 'read value < /tmp/namespace-ack; printf %s "$value"').strip() != '5':
                        self.assertLess(time.monotonic(), deadline)
                        time.sleep(.05)
                    snapshot_job = db.execute('SELECT status FROM scheduled_jobs WHERE id=?', ('owned-job',))[0][0]
                    self.assertEqual((snapshot_version, snapshot_job), (4, '4'))
            finally:
                reader.close()
            fresh = launcher.employee_readonly(state)
            self.assertEqual((fresh['bots'][0]['version'], fresh['jobs'][0]['status']), (5, '5'))
            receipt = dict(root=str(root), container_id=cid, image_id=image, records=records,
                           concurrent_commit_snapshot=dict(before=4, writer_committed=5, same_transaction_job=snapshot_job, fresh_transaction=5),
                           host_staleness_reproduced=any('version' in r['host'] and r['host']['version'] != r['writer_committed'] for r in records))
            launcher.private_json(root / 'namespace-drill.json', receipt)
            print('NAMESPACE_DRILL=' + json.dumps(receipt), flush=True)
        finally:
            info = json.loads(subprocess.check_output(['docker', 'inspect', cid], text=True))[0]
            if info['Id'] != cid or info['Config']['Labels'].get('astonish.namespace-drill') != name:
                raise RuntimeError('refusing cleanup of unowned namespace fixture')
            subprocess.run(['docker', 'rm', '-f', cid], check=True, stdout=subprocess.DEVNULL, timeout=20)
