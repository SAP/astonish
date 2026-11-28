"""Launcher-only Docker-native candidate staging. Never retag a user image."""
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile

MODE = 'docker-native-image-v2'
MODEL_ROOT = '/opt/astonish-models'
MODEL_LABEL = 'astonish.models-sha256'
OWNER_LABEL = 'astonish.chat-first-smoke'
HASH_LABEL = 'astonish.candidate-sha256'
STAGING_LABEL = 'astonish.candidate-staging'


def image_info(image):
    return json.loads(subprocess.check_output(['docker', 'image', 'inspect', image], text=True, timeout=20))[0]


def validate_image(info, state):
    labels = info.get('Config', {}).get('Labels') or {}
    if (info['Id'] != state['candidate_image_id']
            or labels.get(OWNER_LABEL) != state['container']
            or labels.get(HASH_LABEL) != state['binary_sha256']
            or labels.get(STAGING_LABEL) != MODE
            or labels.get('astonish.docker-sha256') != state['docker_sha256']):
        raise RuntimeError('candidate image ownership/hash mismatch')
    if (labels.get(MODEL_LABEL) or '') != state.get('models_sha256', ''):
        raise RuntimeError('candidate model hash mismatch')


def model_payload(root):
    # Import the launcher's pinned allowlist, not a mutable receipt supplied by a caller.
    from local_bot_smoke_launcher import EMBEDDING_HASHES, EMBEDDING_MODEL
    source = Path(root) / 'config/astonish/models' / EMBEDDING_MODEL
    if not source.exists() and not source.is_symlink():
        return [], '', None
    if source.is_symlink() or not source.is_dir():
        raise RuntimeError('native model source must be an owned regular directory')
    assets = []
    for name, expected in EMBEDDING_HASHES.items():
        path = source / name
        if path.is_symlink() or not path.is_file():
            raise RuntimeError('missing or symlink native model asset: ' + name)
        data = path.read_bytes()
        if hashlib.sha256(data).hexdigest() != expected:
            raise RuntimeError('native model asset hash mismatch: ' + name)
        assets.append(('models/' + name, data, 0o444))
    manifest = ''.join(f'{digest}  {name}\n' for name, digest in sorted(EMBEDDING_HASHES.items()))
    return assets + [('models/SHA256SUMS', manifest.encode(), 0o444)], hashlib.sha256(manifest.encode()).hexdigest(), source


def link_models(root, source):
    # Called only after successful image build and label verification. Never touch
    # source user caches, existing links, or paths outside this fresh owned root.
    from local_bot_smoke_launcher import EMBEDDING_MODEL, EMBEDDING_HASHES
    expected = Path(root) / 'config/astonish/models' / EMBEDDING_MODEL
    if source != expected or source.is_symlink() or not source.resolve().is_relative_to(Path(root).resolve()):
        raise RuntimeError('refusing model link outside owned root')
    if set(p.name for p in source.iterdir()) != set(EMBEDDING_HASHES):
        raise RuntimeError('unexpected owned model directory contents')
    for name in EMBEDDING_HASHES:
        (source / name).unlink()
    source.rmdir()
    source.symlink_to(MODEL_ROOT, target_is_directory=True)


def stage(root, owner, base, wrapper_text):
    root = Path(root)
    model_assets, models_digest, model_source = model_payload(root)
    binary = (root / 'astonish').read_bytes()
    cli = (root / 'docker-real').read_bytes()
    digest = hashlib.sha256(binary).hexdigest()
    cli_digest = hashlib.sha256(cli).hexdigest()
    # Resolve the active local engine without copying credentials or invoking
    # unrelated registry credential helpers during this offline build.
    endpoint = os.environ.get('DOCKER_HOST') or subprocess.check_output(
        ['docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}'],
        text=True, timeout=20).strip()
    if not endpoint.startswith('unix://'):
        raise RuntimeError('native staging requires an explicit local Unix Docker endpoint')
    docker_config = root / 'docker-build-config'
    docker_config.mkdir(mode=0o700, exist_ok=True)
    (docker_config / 'config.json').write_text('{}')
    base_id = image_info(base)['Id']
    # Owner is random per fresh launch; content hash distinguishes candidates.
    tag = 'astonish-acceptance-owned:' + owner + '-' + digest
    existing = subprocess.run(['docker', 'image', 'inspect', tag], capture_output=True, timeout=20)
    if existing.returncode == 0:
        raise RuntimeError('refusing to overwrite existing candidate image tag')
    if b'No such image' not in existing.stderr:
        raise RuntimeError('cannot establish candidate image tag absence')
    dockerfile = (f'FROM {base_id}\n'
                  f'LABEL {OWNER_LABEL}="{owner}" {HASH_LABEL}="{digest}" '
                  f'{STAGING_LABEL}="{MODE}" astonish.docker-sha256="{cli_digest}"\n'
                  'COPY candidate /opt/astonish\n'
                  'COPY candidate /usr/local/bin/astonish-host\n'
                  'COPY docker-real /opt/docker/docker\n'
                  'COPY docker-wrapper /opt/astonish-launcher/bin/docker\n')
    if model_assets:
        dockerfile += (f'LABEL {MODEL_LABEL}="{models_digest}"\n'
                       f'COPY models/ {MODEL_ROOT}/\n'
                       f'RUN cd {MODEL_ROOT} && sha256sum -c SHA256SUMS\n')
    # Host Python reads the files; host Docker transports tar bytes over its API.
    # No build context bind, COPY from host mount, or in-container FUSE read.
    with tempfile_context() as context:
        with tarfile.open(fileobj=context, mode='w') as archive:
            for name, data, mode in [('Dockerfile', dockerfile.encode(), 0o644),
                                     ('candidate', binary, 0o755), ('docker-real', cli, 0o755),
                                     ('docker-wrapper', wrapper_text.encode(), 0o755)] + model_assets:
                member = tarfile.TarInfo(name); member.size = len(data); member.mode = mode
                archive.addfile(member, io.BytesIO(data))
        context.seek(0)
        # Classic engine builder supports local immutable image IDs in FROM;
        # BuildKit parses sha256:... as a registry tag instead.
        build_env = dict(os.environ, DOCKER_BUILDKIT='0', DOCKER_CONFIG=str(docker_config), DOCKER_HOST=endpoint)
        build_env.pop('DOCKER_CONTEXT', None)
        subprocess.run(['docker', 'build', '--pull=false', '--network=none', '-t', tag, '-'],
                       stdin=context, env=build_env, check=True, timeout=240)
    info = image_info(tag)
    receipt = dict(candidate_storage=MODE, candidate_image=tag, candidate_image_id=info['Id'],
                   candidate_base_id=base_id, binary_sha256=digest, docker_sha256=cli_digest,
                   container=owner, models_sha256=models_digest)
    validate_image(info, receipt)
    if model_source is not None:
        link_models(root, model_source)
        receipt['model_storage'] = MODEL_ROOT
    return receipt


def tempfile_context():
    import tempfile
    return tempfile.TemporaryFile()


def inspect_native(state, container_info):
    validate_image(image_info(state['candidate_image_id']), state)
    if container_info.get('Image') != state['candidate_image_id']:
        raise RuntimeError('daemon candidate image mismatch')
    protected = ['/opt/astonish', '/usr/local/bin/astonish-host', '/opt/docker/docker',
                 '/opt/astonish-launcher/bin/docker', MODEL_ROOT]
    if state.get('models_sha256'):
        from local_bot_smoke_launcher import EMBEDDING_MODEL
        model = Path(state['root']) / 'config/astonish/models' / EMBEDDING_MODEL
        if not model.is_symlink() or os.readlink(model) != MODEL_ROOT:
            raise RuntimeError('owned model link changed')
    for mount in container_info.get('Mounts', []):
        dest = mount['Destination'].rstrip('/')
        for executable in protected:
            if executable == dest or executable.startswith(dest + '/') or (executable == MODEL_ROOT and dest.startswith(MODEL_ROOT + '/')):
                raise RuntimeError('candidate executable shadowed by mount')
