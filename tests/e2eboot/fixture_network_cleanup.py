"""Exact recorded fixture network cleanup; no prefix pruning or production hooks."""
import json
import re
import subprocess
from pathlib import Path


def recorded_org(state, repo):
    root = Path(state['root'])
    if (not root.is_absolute() or root.is_symlink()
            or root.resolve().parent != (Path(repo) / '.tmp').resolve()
            or not root.name.startswith('astonish-chat-first-')):
        raise RuntimeError('not a recorded fixture root')
    saved = json.loads((root / 'state.json').read_text())
    org = state['container']
    if saved['container'] != org or not re.fullmatch(r'astonish-chat-first-[0-9a-f]{8}', org):
        raise RuntimeError('recorded fixture identity mismatch')
    # create() writes this exact organization slug before starting the daemon.
    config = (root / 'config/astonish/config.yaml').read_text()
    if not re.search(r'^    default_org_slug: ' + re.escape(org) + r'\s*$', config, re.M):
        raise RuntimeError('fixture organization configuration mismatch')
    return org


def cleanup_recorded_network(state, repo):
    """Only retired fixtures: no daemon or org compute, exact label, no attachments.

    Keep all Home/data/image evidence. This function cannot delete containers,
    volumes, foreign networks or an active quality fixture.
    """
    org = recorded_org(state, repo)
    for selector in ('name=^/' + org + '$', 'label=astonish.org=' + org):
        result = subprocess.check_output(['docker', 'ps', '-aq', '--filter', selector], text=True, timeout=20)
        if result.strip():
            raise RuntimeError('fixture still has daemon or compute; retained')
    name = 'astonish-org-' + org
    ids = subprocess.check_output(['docker', 'network', 'ls', '--filter', 'name=^' + name + '$', '--format', '{{.ID}}'], text=True, timeout=20).split()
    if not ids:
        return None
    if len(ids) != 1:
        raise RuntimeError('ambiguous fixture network; retained')
    info = json.loads(subprocess.check_output(['docker', 'network', 'inspect', ids[0]], text=True, timeout=20))[0]
    if (info['Name'] != name or info['Labels'] != {'astonish.org': org}
            or info['Driver'] != 'bridge' or info['Scope'] != 'local' or info['Containers']):
        raise RuntimeError('nonowned or attached fixture network; retained')
    subprocess.run(['docker', 'network', 'rm', info['Id']], check=True, capture_output=True, timeout=20)
    remaining = subprocess.check_output(['docker', 'network', 'ls', '--filter', 'name=^' + name + '$', '--format', '{{.ID}}'], text=True, timeout=20)
    if remaining.strip():
        raise RuntimeError('fixture network remains after cleanup')
    return {'name': name, 'id': info['Id'], 'state': str(Path(state['root']) / 'state.json'),
            'labels': info['Labels'], 'attachments': 0}
