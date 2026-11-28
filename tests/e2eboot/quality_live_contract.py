"""Strict quality-live receipt contract; no resource operations."""
import re

QUALITY_CONTRACT = 'bot-quality-live-v1'
QUALITY_REQUIRED = ('persistent-requirements', 'supplied-draft-corrected',
                    'three-offline-assessed-results', 'live-reload-final-only',
                    'cadence-preserves-requirements', 'owner-update-next-run')


def strict_json(raw):
    import json
    def pairs(items):
        result = {}
        seen = set()
        for key, value in items:
            if key.lower() in seen:
                raise ValueError('duplicate or case-aliased evidence member')
            seen.add(key.lower()); result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def validate_quality_evidence(state, actual, validate_employee):
    if (state.get('workflow') != QUALITY_CONTRACT or not state.get('quality_nonce')
            or state.get('diagnostic_resume') or state.get('browser_refreshes')
            or actual.get('contract') != QUALITY_CONTRACT
            or actual.get('nonce') != state['quality_nonce']
            or actual.get('status') != 'verified'
            or any(actual.get(k) for k in ('unverified', 'unimplemented', 'skipped', 'diagnostic_resume'))):
        raise RuntimeError('UNVERIFIED quality fresh contract')
    employee = actual.get('employee')
    if not isinstance(employee, dict) or any(employee.get(k) for k in ('skipped', 'diagnostic_resume')):
        raise RuntimeError('UNVERIFIED quality employee journey missing')
    validate_employee(state, employee)
    receipts = actual.get('receipts', {})
    if (set(actual.get('passed', [])) != set(QUALITY_REQUIRED)
            or len(actual.get('passed', [])) != len(QUALITY_REQUIRED)
            or set(receipts) != set(QUALITY_REQUIRED)):
        raise RuntimeError('UNVERIFIED quality gates missing/duplicate/unknown')
    for name in QUALITY_REQUIRED:
        r = receipts[name]
        if (not isinstance(r, dict) or r.get('SchemaVersion') != 1
                or r.get('Candidate') != state['binary_sha256']
                or r.get('BrowserCandidate') != state['browser_sha256']
                or r.get('Nonce') != state['quality_nonce']
                or r.get('BotID') != state['bot_id']
                or r.get('Home') != state['url']+'/#/chat/bots/personal/'+state['bot_id']
                or not r.get('Source') or not r.get('Detail')
                or not re.fullmatch('[0-9a-f]{64}', str(r.get('SourceHash', '')))
                or r.get('Status') != 'passed'):
            raise RuntimeError('UNVERIFIED quality receipt: '+name)
        import hashlib
        from pathlib import Path
        root = Path(state['root']).resolve()
        source = root / r['Source']
        if source.parent != root or source.is_symlink() or not source.is_file() or hashlib.sha256(source.read_bytes()).hexdigest() != r['SourceHash']:
            raise RuntimeError('UNVERIFIED quality source bytes: '+name)
    runs = receipts['three-offline-assessed-results'].get('RunIDs', [])
    if len(runs) != 3 or len(set(runs)) != 3 or any(not x for x in runs):
        raise RuntimeError('UNVERIFIED three independent scheduled results')
