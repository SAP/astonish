"""Fresh skill evidence layered on (not substituted for) employee + quality proof."""
import hashlib
import re
from pathlib import Path
from quality_live_contract import QUALITY_CONTRACT, strict_json

SKILLS_CONTRACT = 'bot-skills-live-v1'
SKILLS_REQUIRED = ('assignment-without-schedule', 'reuse-pinned-skill', 'owner-feedback-revision',
                   'owned-restart-three-scheduled', 'manual-and-scheduled-outcomes',
                   'schedule-removal-manual', 'skill-inspection-readonly')


def validate_skills_evidence(state, actual, validate_quality):
    if (state.get('workflow') != SKILLS_CONTRACT or not state.get('skills_nonce')
            or state.get('diagnostic_resume') or state.get('browser_refreshes')
            or actual.get('contract') != SKILLS_CONTRACT
            or actual.get('nonce') != state['skills_nonce'] or actual.get('status') != 'verified'
            or any(actual.get(k) for k in ('unverified', 'unimplemented', 'skipped', 'diagnostic_resume'))):
        raise RuntimeError('UNVERIFIED fresh skills contract')
    quality = actual.get('quality')
    if not isinstance(quality, dict):
        raise RuntimeError('UNVERIFIED preserved quality journey')
    validate_quality(dict(state, workflow=QUALITY_CONTRACT), quality)
    receipts = actual.get('receipts', {})
    if (not isinstance(receipts, dict) or set(receipts) != set(SKILLS_REQUIRED)
            or sorted(actual.get('passed', [])) != sorted(SKILLS_REQUIRED)):
        raise RuntimeError('UNVERIFIED missing/duplicate/unknown skill gates')
    seen_sources = set()
    for name in SKILLS_REQUIRED:
        r = receipts[name]
        if (not isinstance(r, dict) or r.get('SchemaVersion') != 1
                or r.get('Candidate') != state['binary_sha256']
                or r.get('BrowserCandidate') != state['browser_sha256']
                or r.get('Nonce') != state['skills_nonce'] or r.get('BotID') != state['bot_id']
                or r.get('Home') != state['url']+'/#/chat/bots/personal/'+state['bot_id']
                or r.get('Status') != 'passed' or not r.get('Detail')
                or r.get('Source') != 'skills-'+name+'.json'
                or not re.fullmatch('[0-9a-f]{64}', str(r.get('SourceHash', '')))):
            raise RuntimeError('UNVERIFIED skill receipt: '+name)
        source = Path(state['root']).resolve() / r['Source']
        if source.is_symlink() or not source.is_file():
            raise RuntimeError('UNVERIFIED skill source: '+name)
        raw = source.read_bytes()
        if hashlib.sha256(raw).hexdigest() != r['SourceHash'] or r['SourceHash'] in seen_sources:
            raise RuntimeError('UNVERIFIED altered/replayed skill source: '+name)
        seen_sources.add(r['SourceHash'])
        document = strict_json(raw)
        identities = {key: r[key] for key in ('RunIDs', 'ManualRunID') if key in r}
        if document.get('identities', {}) != identities:
            raise RuntimeError('UNVERIFIED skill run identities differ from hashed source: '+name)
        if (document.get('gate') != name or document.get('nonce') != state['skills_nonce']
                or document.get('candidate') != state['binary_sha256']
                or document.get('browser_candidate') != state['browser_sha256']
                or document.get('bot_id') != state['bot_id'] or not document.get('observation')):
            raise RuntimeError('UNVERIFIED foreign/replayed skill document: '+name)
    manual = receipts['manual-and-scheduled-outcomes'].get('ManualRunID')
    scheduled = receipts['owned-restart-three-scheduled'].get('RunIDs', [])
    removed = receipts['schedule-removal-manual'].get('ManualRunID')
    if (not manual or not removed or len(scheduled) != 3 or len(set(scheduled)) != 3
            or any(not x for x in scheduled) or len(set(scheduled+[manual, removed])) != 5
            or receipts['manual-and-scheduled-outcomes'].get('RunIDs') != scheduled):
        raise RuntimeError('UNVERIFIED distinct manual and scheduled skill runs')
