import copy
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import native_candidate_staging as native
import local_bot_smoke_launcher as launcher


class NativeCandidateTests(unittest.TestCase):
    def setUp(self):
        env = patch.dict(os.environ, {'DOCKER_HOST':'unix:///test/docker.sock'})
        env.start(); self.addCleanup(env.stop)

    def fixture(self):
        state = dict(candidate_image_id='sha256:owned', container='owned', binary_sha256='a'*64, docker_sha256='b'*64)
        info = dict(Id='sha256:owned', Config={'Labels': {
            native.OWNER_LABEL: 'owned', native.HASH_LABEL: 'a'*64,
            native.STAGING_LABEL: native.MODE, 'astonish.docker-sha256': 'b'*64}})
        return state, info

    def test_ownership_hash_identity_and_mount_shadow_rejected(self):
        state, info = self.fixture()
        native.validate_image(info, state)
        for key in ('candidate_image_id', 'container', 'binary_sha256', 'docker_sha256'):
            bad = dict(state); bad[key] = 'other'
            with self.assertRaises(RuntimeError): native.validate_image(info, bad)
        with patch.object(native, 'image_info', return_value=info):
            native.inspect_native(state, {'Image': info['Id'], 'Mounts': []})
            for dest in ('/opt', '/opt/astonish', '/usr/local/bin/astonish-host', '/opt/docker', '/opt/astonish-launcher'):
                with self.assertRaises(RuntimeError):
                    native.inspect_native(state, {'Image': info['Id'], 'Mounts': [{'Destination': dest}]})

    def test_stdin_tar_contains_both_native_paths_and_cli_and_no_secrets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'astonish').write_bytes(b'candidate'); (root/'docker-real').write_bytes(b'cli')
            state, info = self.fixture()
            info['Config']['Labels'][native.HASH_LABEL] = hashlib.sha256(b'candidate').hexdigest()
            info['Config']['Labels']['astonish.docker-sha256'] = hashlib.sha256(b'cli').hexdigest()
            def run(args, **kwargs):
                if args[1] == 'image': return subprocess.CompletedProcess(args, 1, b'', b'No such image')
                self.assertEqual(args[-1], '-')
                self.assertIn('--pull=false', args)
                self.assertEqual(kwargs['env']['DOCKER_BUILDKIT'], '0')
                self.assertNotIn('DOCKER_CONTEXT', kwargs['env'])
                self.assertEqual(Path(kwargs['env']['DOCKER_CONFIG'], 'config.json').read_text(), '{}')
                with tarfile.open(fileobj=kwargs['stdin']) as archive:
                    self.assertEqual(set(archive.getnames()), {'Dockerfile', 'candidate', 'docker-real', 'docker-wrapper'})
                    recipe = archive.extractfile('Dockerfile').read().decode()
                    self.assertIn('COPY candidate /opt/astonish', recipe)
                    self.assertIn('COPY candidate /usr/local/bin/astonish-host', recipe)
                    self.assertIn('COPY docker-real /opt/docker/docker', recipe)
                    self.assertEqual(archive.extractfile('candidate').read(), b'candidate')
                    self.assertEqual(archive.getmember('candidate').mode, 0o755)
                return subprocess.CompletedProcess(args, 0)
            with patch.object(native, 'image_info', side_effect=[{'Id':'sha256:base'}, info]), patch.object(native.subprocess, 'run', side_effect=run):
                result = native.stage(root, 'owned', 'base', '#!/bin/bash')
            self.assertEqual(result['candidate_image_id'], 'sha256:owned')

    def test_existing_tag_or_engine_error_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('astonish', 'docker-real'): (root/name).write_bytes(b'candidate')
            for code, error in ((0, b''), (1, b'engine unavailable')):
                with patch.object(native, 'image_info', return_value={'Id':'base'}), patch.object(native.subprocess, 'run', return_value=subprocess.CompletedProcess([], code, b'', error)) as run:
                    with self.assertRaises(RuntimeError): native.stage(root, 'owned', 'base', '')
                    self.assertEqual(run.call_count, 1)

    def test_model_build_tar_and_checksum_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('astonish', 'docker-real'): (root/name).write_bytes(b'candidate')
            state, info = self.fixture()
            digest = hashlib.sha256(b'candidate').hexdigest()
            info['Config']['Labels'].update({native.HASH_LABEL:digest, 'astonish.docker-sha256':digest, native.MODEL_LABEL:'manifest'})
            assets = [('models/' + name, name.encode(), 0o444) for name in launcher.EMBEDDING_HASHES]
            assets.append(('models/SHA256SUMS', b'manifest', 0o444))
            def run(args, **kwargs):
                if args[1] == 'image': return subprocess.CompletedProcess(args, 1, b'', b'No such image')
                with tarfile.open(fileobj=kwargs['stdin']) as archive:
                    for name, data, mode in assets:
                        self.assertEqual(archive.extractfile(name).read(), data)
                        self.assertEqual(archive.getmember(name).mode, mode)
                    recipe = archive.extractfile('Dockerfile').read().decode()
                    self.assertIn('COPY models/ /opt/astonish-models/', recipe)
                    self.assertIn('RUN cd /opt/astonish-models && sha256sum -c SHA256SUMS', recipe)
                return subprocess.CompletedProcess(args, 0)
            with patch.object(native, 'model_payload', return_value=(assets,'manifest',root/'model')), patch.object(native, 'link_models') as link, patch.object(native, 'image_info', side_effect=[{'Id':'base'},info]), patch.object(native.subprocess, 'run', side_effect=run):
                receipt = native.stage(root, 'owned', 'base', '#!/bin/sh')
                link.assert_called_once_with(root, root/'model')
                self.assertEqual(receipt['model_storage'], native.MODEL_ROOT)

    def test_six_model_assets_verified_and_native_link(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root/'config/astonish/models'/launcher.EMBEDDING_MODEL
            source.mkdir(parents=True)
            hashes = {}
            for name in launcher.EMBEDDING_HASHES:
                data = name.encode(); (source/name).write_bytes(data)
                hashes[name] = hashlib.sha256(data).hexdigest()
            with patch.object(launcher, 'EMBEDDING_HASHES', hashes):
                assets, digest, path = native.model_payload(root)
                self.assertEqual(len(assets), 7)
                self.assertEqual(path, source)
                for name, data, mode in assets:
                    self.assertEqual(mode, 0o444)
                    if name != 'models/SHA256SUMS':
                        self.assertEqual(hashlib.sha256(data).hexdigest(), hashes[Path(name).name])
                for name in hashes:
                    original = (source/name).read_bytes()
                    (source/name).write_bytes(b'bad')
                    with self.assertRaisesRegex(RuntimeError, 'hash mismatch'): native.model_payload(root)
                    (source/name).write_bytes(original)
                native.link_models(root, source)
                self.assertEqual(os.readlink(source), native.MODEL_ROOT)
                with self.assertRaisesRegex(RuntimeError, 'regular directory'): native.model_payload(root)
                state, info = self.fixture()
                state.update(root=str(root), models_sha256=digest)
                info['Config']['Labels'][native.MODEL_LABEL] = digest
                with patch.object(native, 'image_info', return_value=info):
                    native.inspect_native(state, {'Image':info['Id'], 'Mounts':[{'Destination':str(root)}]})
                    with self.assertRaisesRegex(RuntimeError, 'shadowed'):
                        native.inspect_native(state, {'Image':info['Id'], 'Mounts':[{'Destination':native.MODEL_ROOT}]})
                state['models_sha256'] = 'wrong'
                with self.assertRaisesRegex(RuntimeError, 'model hash'): native.validate_image(info, state)

    def test_native_wrapper_pin_owner_and_no_executable_mount(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake = root/'docker'
            fake.write_text('#!/bin/bash\nprintf "%s\\n" "$@"\n'); fake.chmod(0o755)
            script = root/'wrapper'
            script.write_text(launcher.wrapper('owned', '/alias', '/layers', native=True).replace('/opt/docker/docker', str(fake)))
            env = dict(os.environ, ASTONISH_SANDBOX_IMAGE='sha256:owned')
            for owner, image, success in [('owned','sha256:owned',True), ('other','sha256:owned',False), ('owned','base',False)]:
                result = subprocess.run(['bash',str(script),'container','run','--label=astonish.org='+owner,image], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0 if success else 64)
                self.assertNotIn('--mount', result.stdout)


if __name__ == '__main__': unittest.main()
