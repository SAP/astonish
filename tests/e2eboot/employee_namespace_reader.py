"""Owned-container SQL transport; the launcher retains all evidence projections."""
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading


class Row(dict):
    def __getitem__(self, key):
        return list(self.values())[key] if isinstance(key, int) else super().__getitem__(key)


class Reader:
    def __init__(self, repo, root, info):
        self.process = None
        self.root = root
        source = repo / 'tests/e2eboot/readhelper/main.go'
        digest = hashlib.sha256(source.read_bytes() + (repo / 'go.mod').read_bytes() + (repo / 'go.sum').read_bytes()).hexdigest()
        binary = root / ('employee-readhelper-' + digest)
        if not binary.exists():
            fd, pending = tempfile.mkstemp(prefix='.readhelper-', dir=root)
            os.close(fd)
            try:
                env = dict(os.environ, GOOS='linux', GOARCH='arm64', CGO_ENABLED='0')
                subprocess.run(['go', 'build', '-o', pending, './tests/e2eboot/readhelper'], cwd=repo, env=env, check=True, timeout=120, stdout=subprocess.DEVNULL)
                Path(pending).chmod(0o700)
                Path(pending).replace(binary)
            finally:
                Path(pending).unlink(missing_ok=True)
        expected = hashlib.sha256(binary.read_bytes()).hexdigest()
        target = '/tmp/employee-readhelper-' + expected
        command = ['docker', 'exec', info['Id'], 'sha256sum', target]
        check = subprocess.run(command, text=True, capture_output=True, timeout=15)
        if check.returncode:
            subprocess.run(['docker', 'cp', str(binary), info['Id'] + ':' + target], check=True, timeout=20, stdout=subprocess.DEVNULL)
            check = subprocess.run(command, text=True, capture_output=True, check=True, timeout=15)
        if check.stdout.split()[0] != expected:
            raise RuntimeError('owned SQLite helper hash mismatch')
        self.process = subprocess.Popen(['docker', 'exec', '-i', info['Id'], target, str(root)],
                                        text=True, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        # Bound blocked protocol reads without imposing a host SQL fallback.
        self.timer = threading.Timer(25, self.process.kill)
        self.timer.daemon = True
        self.timer.start()

    def request(self, **value):
        self.process.stdin.write(json.dumps(value) + '\n')
        self.process.stdin.flush()
        line = self.process.stdout.readline()
        if not line:
            raise RuntimeError('owned SQLite helper exited or timed out')
        result = json.loads(line)
        if result.get('error'):
            raise RuntimeError('owned SQLite helper: ' + result['error'])
        return result

    def paths(self):
        return [Path(p) for p in self.request(op='paths').get('paths', [])]

    def connect(self, database, **kwargs):
        from urllib.parse import unquote, urlsplit
        self.request(op='open', path=unquote(urlsplit(database).path))
        return self

    def execute(self, sql, args=()):
        response = self.request(op='query', sql=sql, args=args)
        def decode(value):
            if isinstance(value, dict) and set(value) == {'sqlite_blob_base64'}:
                return base64.b64decode(value['sqlite_blob_base64'], validate=True)
            return value
        return [Row(zip(response.get('columns', []), map(decode, row))) for row in response.get('rows', [])]

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.request(op='close')

    def close(self):
        if self.process is not None:
            self.timer.cancel()
            self.process.stdin.close()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill(); self.process.wait(timeout=3)
            self.process.stdout.close()
