"""Offline, fail-closed validation of current-invocation agent observations.

Hashes bind retained bytes, not factual truth or authenticated adapter provenance.
The unsupported gates below need new producer interfaces, not a status flag.
"""
import hashlib
import json
import re
import sys
from datetime import datetime
from pathlib import Path
from uuid import UUID
from quality_live_contract import strict_json
from skills_live_contract import SKILLS_REQUIRED
from agent_terminal_contract import terminal_parity

AGENT_CONTRACT = 'bot-agent-live-v1'
LEARNING_CONTRACT = 'bot-agent-skills-live-v1'
AGENT_REQUIRED = (
    'stock-1', 'stock-2', 'stock-3', 'adapter-provenance', 'source-grounding',
    'document-revision', 'conflicting-source-comparison', 'feedback-restart',
    'skill-three-scheduler-occurrences', 'schedule-delete-manual-reuse',
    'studio-terminal-parity',
)
LEARNING_GATES = ('feedback-restart', 'skill-three-scheduler-occurrences', 'schedule-delete-manual-reuse')


def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)


def digest(value):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{64}', value) is not None


def identity(value):
    require(isinstance(value, str) and str(UUID(value)) == value and UUID(value).int != 0,
            'missing/canonical UUID identity')
    return value


def stamp(value):
    require(isinstance(value, str), 'missing timestamp')
    result = datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(result.tzinfo is not None and result.year > 1, 'invalid timestamp')
    return result


def invocation(state, actual):
    require(all(digest(state.get(k)) for k in ('binary_sha256', 'browser_sha256', 'agent_nonce'))
            and actual.get('contract') == AGENT_CONTRACT
            and actual.get('candidate') == state['binary_sha256']
            and actual.get('browser_candidate') == state['browser_sha256']
            and actual.get('nonce') == state['agent_nonce']
            and not state.get('diagnostic_resume') and not state.get('browser_refreshes'),
            'invocation identity')


def bound(state, document):
    require(document['nonce'] == state['agent_nonce']
            and document['candidate'] == state['binary_sha256']
            and document['browser_candidate'] == state['browser_sha256'], 'foreign/replayed document')


def source(state, name, sha, parse=True):
    require(isinstance(name, str) and Path(name).name == name and name not in ('.', '..')
            and digest(sha), 'invalid evidence reference')
    root = Path(state['root']).resolve()
    path = root / name
    require(not path.is_symlink() and path.is_file(), 'missing/symlink evidence file')
    raw = path.read_bytes()
    require(hashlib.sha256(raw).hexdigest() == sha, 'altered evidence digest')
    return strict_json(raw) if parse else raw.decode('utf-8')


def run_result(run, bot):
    identity(bot)
    identity(run['id']); identity(run['session_id'])
    w = run['work']
    require(run['bot_id'] == bot and run['status'] == 'succeeded'
            and stamp(run['finished_at']) >= stamp(run['started_at']) >= stamp(run['created_at'])
            and isinstance(run['report']['summary'], str) and run['report']['summary'].strip()
            and w['schema_version'] == 1 and w['protocol'] == 'agent_led_v1'
            and w['bot_id'] == bot and w['run_id'] == run['id'] and w['work_revision'] >= 1
            and w['phase'] == 'finished' and w['disposition'] in ('answered', 'complete')
            and not w.get('remaining') and digest(w['candidate_digest'])
            and not run['report'].get('quality'), 'incomplete/foreign/legacy run')
    return run


def published_answer_matches(content, answer):
    """Preserve exact agent text; permit only the known renderer's one extra LF."""
    return (isinstance(content, str) and isinstance(answer, str)
            and content in (answer, answer + '\n'))


def general(state, actual, key):
    manifest_ref = actual['general_manifest']
    require(manifest_ref['Source'] == 'agent-general-manifest.json', 'unknown general manifest')
    manifest = source(state, manifest_ref['Source'], manifest_ref['SourceHash'])
    bound(state, manifest)
    require(manifest['contract'] == AGENT_CONTRACT and manifest['sources'] == actual['general_sources']
            and manifest['journeys'] == actual['journeys'], 'foreign/replayed general evidence')
    files = manifest['sources']
    load = lambda name, parse=True: source(state, 'agent-' + name, files['agent-' + name], parse)
    bot = load('general-bot.json')
    identity(bot['id']); identity(bot['home_session_id'])
    rows = load('general-runs.json')['runs']
    require(len({r['id'] for r in rows}) == len(rows), 'duplicate general run')
    run = actual['journeys'][key]['run']
    require(sum(r == run for r in rows) == 1, 'journey run absent from retained endpoint')
    run_result(run, bot['id'])
    model = load('general-model.json')
    require(model['effectiveProvider'] == run['provider'] == 'New AI Core Provider'
            and model['effectiveModel'] == run['model'] == 'anthropic--claude-4.6-sonnet'
            and model['credentialsAvailable'] is True, 'resolver parity')
    histories = [load(key + '-' + label + '-history.json') for label in ('published', 'restored')]
    for history in histories:
        require(history['id'] == bot['home_session_id'], 'foreign publication Home')
        reports = [m for m in history['messages'] if m.get('invocationId') == 'bot-report:' + run['id']]
        message = run['report']['employee_message']
        require(message['origin'] == 'agent' and message['text'].strip()
                and len(reports) == 1 and reports[0]['type'] == 'agent'
                and published_answer_matches(reports[0]['content'], message['text']), 'missing/duplicate/changed canonical publication')
    require(histories[0]['messages'] == histories[1]['messages'], 'changed restored publication')
    return load, run


def artifact(run, path, body):
    matches = [a for a in run['report']['artifacts'] if a['relative_path'] == path]
    require(len(matches) == 1, 'missing/duplicate retained artifact')
    a = matches[0]
    identity(a['id']); identity(a['home_id'])
    require(a['sha256'] == hashlib.sha256(body.encode()).hexdigest(), 'artifact digest mismatch')
    return a


def document_revision(state, actual):
    load, seed = general(state, actual, 'document-seed')
    _, revised = general(state, actual, 'document-revision')
    nonce = state['agent_nonce']
    before, after = (load('document-' + part + '-artifact.txt', False) for part in ('before', 'after'))
    initial = '# Operations brief\n\nRecord: ' + nonce + '\n\nReview window: 14 days.\n\nBudget: unchanged.\n'
    require(before == initial and after == before.replace('Review window: 14 days.', 'Review window: 21 days.'),
            'revision differs from exact current-invocation edit')
    path = 'reports/agent-' + nonce[:12] + '-brief.md'
    prior, next_ = artifact(seed, path, before), artifact(revised, path, after)
    require(seed['id'] != revised['id'] and seed['session_id'] != revised['session_id']
            and prior['home_id'] == next_['home_id'], 'revision reused run or lost Home')


def comparison(state, actual):
    load, seed = general(state, actual, 'comparison-seed')
    _, run = general(state, actual, 'conflicting-source-comparison')
    stem = 'agent-' + state['agent_nonce'][:12]
    n = ord(state['agent_nonce'][0]) + 100
    expected = [dict(id=stem + '-' + label, as_of=date, units=n + delta) for label, date, delta in (
        ('archive', '2024-01-10T00:00:00Z', 0), ('current', '2025-02-20T00:00:00Z', 17),
        ('interim', '2024-12-01T00:00:00Z', 3))]
    raw = load('comparison-sources-artifact.txt', False)
    require(strict_json(raw) == expected and load('comparison-sources-after-artifact.txt', False) == raw,
            'changed/foreign comparison dossier')
    answer = load('comparison-answer-artifact.txt', False)
    a = strict_json(answer)
    require(set(a) == {'selected_source', 'as_of', 'units', 'conflict', 'stale_sources', 'citations'}
            and type(a['units']) is int and a['units'] == n + 17 and a['conflict'] is True
            and a['selected_source'] == expected[1]['id'] and a['as_of'] == expected[1]['as_of']
            and sorted(a['stale_sources']) == sorted([expected[0]['id'], expected[2]['id']])
            and sorted(a['citations']) == sorted(s['id'] for s in expected), 'unsupported comparison claim')
    artifact(seed, 'reports/' + stem + '-sources.json', raw)
    artifact(run, 'reports/' + stem + '-comparison.json', answer)
    require(seed['id'] != run['id'] and seed['session_id'] != run['session_id'], 'comparison reused seed run')


def persisted_content_parts(event):
    """Decode only the known ADK envelope spellings, without normalizing payloads.

    strict_json rejects duplicate/case-aliased members before this helper. Keep
    the same rule for direct callers: even equal aliases are ambiguous evidence.
    Go exports Content while the alternate JSON envelope uses content; nested
    genai parts retain their explicit lower-camel JSON tags in both forms.
    """
    require(isinstance(event, dict), 'invalid persisted event')
    keys = [key for key in event if key.lower() == 'content']
    require(len(keys) <= 1 and all(key in ('Content', 'content') for key in keys),
            'ambiguous persisted content alias')
    content = event[keys[0]] if keys else None
    if content is None:
        return []
    require(isinstance(content, dict), 'invalid persisted content')
    parts = content.get('parts', [])
    require(isinstance(parts, list) and all(isinstance(part, dict) for part in parts),
            'invalid persisted content parts')
    return parts


def retrieval(stream):
    # Parse finite persisted ADK SSE, not a URL or a success flag in answer text.
    messages = []
    complete = False
    for frame in stream.replace('\r\n', '\n').split('\n\n'):
        data = '\n'.join(line[5:].strip() for line in frame.splitlines() if line.startswith('data:'))
        if not data:
            continue
        event = strict_json(data)
        complete |= event.get('persisted') is True
        messages.extend(persisted_content_parts(event))
    calls, results = {}, set()
    for part in messages:
        call = part.get('functionCall')
        if call and call.get('name') in ('web_fetch', 'browser_navigate', 'web_search'):
            require(call.get('id') and call['id'] not in calls, 'duplicate retrieval call')
            calls[call['id']] = call['name']
        result = part.get('functionResponse')
        if not result or result.get('id') not in calls:
            continue
        require(result['id'] not in results and result.get('name') == calls[result['id']], 'ambiguous retrieval result')
        value = result.get('response')
        if (isinstance(value, dict) and not value.get('error') and value.get('success') is not False
                and (not isinstance(value.get('status'), (int, float)) or 200 <= value['status'] < 400)
                and len(json.dumps(value)) > 50):
            results.add(result['id'])
    require(complete and results, 'no paired successful persisted retrieval')


def disk_runs(disk):
    found = {}
    for row in disk['Runs']:
        data = strict_json(row['data'])
        if 'bot_document' in data:
            require(set(data) == {'bot_document'}, 'ambiguous disk wrapper')
            data = strict_json(data['bot_document'])
        data = {key.lower(): value for key, value in data.items()}
        require(isinstance(data.get('run'), dict) and 'id' not in data and 'bot_id' not in data,
                'missing/ambiguous private run envelope')
        run = data['run']
        require(run['id'] == row['id'] and run['id'] not in found
                and row.get('bot_id', run['bot_id']) == run['bot_id'], 'foreign/duplicate disk row')
        found[run['id']] = (run, data['work'])
    return found


def pinned(work, run, skill, minimum=1):
    pin = work['contract']['skill']
    receipt = run['report']['skill']
    snapshot = pin['snapshot']
    require(work['protocol'] == 'agent_led_v1' and work['phase'] == 'finished'
            and work['contract']['run_id'] == run['id'] and work['contract']['bot_id'] == run['bot_id']
            and not work.get('review') and work['agent_work']['disposition'] in ('answered', 'complete')
            and not work['agent_work'].get('remaining')
            and pin['skill_id'] == skill == snapshot['skill_id'] == receipt['skill_id']
            and pin['revision'] == snapshot['revision'] == receipt['used_revision'] >= minimum
            and pin['digest'] == snapshot['digest'] and digest(pin['digest'])
            and snapshot['bot_id'] == receipt['bot_id'] == run['bot_id']
            and receipt['run_id'] == run['id'] and receipt['schema_version'] == 1
            and receipt == work['skill_state']['receipt']
            and receipt['kind'] in ('created', 'updated', 'unchanged', 'selected'), 'invalid immutable skill pin')
    require(run['work']['work_revision'] == work['revision']
            and run['work']['candidate_digest'] == work['candidate']['digest'], 'private/public work mismatch')
    committed = receipt.get('committed_revision', 0)
    require((committed > pin['revision'] and work['skill_state']['stage'] == 'learning_committed' and digest(receipt['digest']))
            or (committed == 0 and receipt['digest'] == pin['digest']), 'unresolved learning receipt')


def learning(state, actual):
    evidence = actual['learning']
    require(evidence['contract'] == LEARNING_CONTRACT and evidence['nonce'] == state['agent_nonce']
            and evidence['status'] == 'verified' and sorted(evidence['passed']) == sorted(SKILLS_REQUIRED)
            and set(evidence['receipts']) == set(SKILLS_REQUIRED), 'incomplete/unknown learning gates')
    bot = identity(state['agent_learning_bot_id'])
    documents, captures = {}, {}
    for name in SKILLS_REQUIRED:
        r = evidence['receipts'][name]
        require(r['SchemaVersion'] == 1 and r['Candidate'] == state['binary_sha256']
                and r['BrowserCandidate'] == state['browser_sha256'] and r['Nonce'] == state['agent_nonce']
                and r['BotID'] == bot and r['Home'] == state['url'] + '/#/chat/bots/personal/' + bot
                and r['Status'] == 'passed' and r['Source'] == 'agent-skills-' + name + '.json', 'foreign learning receipt')
        doc = source(state, r['Source'], r['SourceHash'])
        bound(state, doc)
        require(doc['contract'] == LEARNING_CONTRACT and doc['gate'] == name and doc['bot_id'] == bot
                and doc.get('identities', {}) == {k: r[k] for k in ('RunIDs', 'ManualRunID') if k in r},
                'learning document identity')
        started, captured = stamp(doc['invocation_started_at']), stamp(doc['captured_at'])
        require(started <= captured, 'learning observation time')
        for rid, sha in doc['run_captures'].items():
            identity(rid)
            record = source(state, 'agent-skills-run-' + rid + '.json', sha)
            bound(state, record)
            run = run_result(record['run'], bot)
            require(record['contract'] == LEARNING_CONTRACT and run['id'] == rid
                    and started <= stamp(run['created_at']) <= stamp(run['finished_at']) <= stamp(record['captured_at']) <= captured,
                    'stale/foreign run capture')
            retrieval(record['stream'])
            require(rid not in captures or captures[rid] == run, 'changed captured run')
            captures[rid] = run
        documents[name] = doc
    def observed(name):
        return documents[name]['observation']
    def captured_run(run):
        require(captures.get(run['id']) == run, 'run absent from current transcript captures')
        return run
    assign = observed('assignment-without-schedule')
    baseline = assign['head']
    skill = identity(baseline['skill_id'])
    require(baseline['bot_id'] == bot and not assign['disk']['Jobs'], 'assignment created schedule')
    assignment_id = assign['work']['contract']['run_id']
    assignment_run, assignment_work = disk_runs(assign['disk'])[assignment_id]
    captured_run(assignment_run)
    require(assignment_work == assign['work'], 'assignment private work differs from disk')
    pinned(assignment_work, assignment_run, skill)
    require(all(r['trigger'] != 'cron' for r, _ in disk_runs(assign['disk']).values()),
            'assignment scheduled work without consent')
    reuse = observed('reuse-pinned-skill')
    manual = captured_run(reuse['run'])
    pinned(reuse['work'], manual, skill, baseline['revision'])
    feedback = observed('owner-feedback-revision')
    before, after, feedback_run = feedback['before'], feedback['after'], captured_run(feedback['source_run'])
    require(before == feedback['old_readback'] and before['skill_id'] == after['skill_id'] == skill
            and before['bot_id'] == after['bot_id'] == bot and after['revision'] > before['revision']
            and digest(after['digest']) and before['digest'] != after['digest'], 'feedback revision/history mismatch')
    feedback_receipt = feedback_run['report']['skill']
    require(feedback_receipt['skill_id'] == skill and feedback_receipt.get('committed_revision') == after['revision']
            and feedback_receipt['digest'] == after['digest'] and feedback_receipt['kind'] == 'updated',
            'owner feedback revision is not committed by source run')
    require(all(any(e['id'] == old['id'] and e['text'] == old['text'] for e in after['expectations'])
                for old in before['expectations']), 'feedback erased owner requirement')
    require(any('humidity' in e['text'].lower() and e['provenance']['level'] == 'owner_specified'
                and e['provenance']['run_id'] == feedback_run['id']
                and e['provenance']['event_id'] == feedback_run['user_event_id']
                and feedback_run['user_event_id'] for e in after['expectations']), 'feedback lacks exact owner source')
    scheduled = documents['owned-restart-three-scheduled']
    ids = scheduled['identities']['RunIDs']
    require(len(ids) == len(set(ids)) == 3, 'three distinct scheduler runs required')
    offline = scheduled['observation']
    restart = stamp(offline['restart'])
    require(stamp(feedback_run['finished_at']) < restart, 'restart preceded owner feedback')
    trigger = identity(offline['trigger'])
    rows = disk_runs(offline['disk'])
    ticks = []
    for rid in ids:
        run, work = rows[rid]
        captured_run(run)
        require(run['trigger'] == 'cron' and run['trigger_id'] == trigger
                and stamp(run['created_at']) > restart and stamp(run['started_at']) > restart,
                'not a post-restart occurrence')
        pinned(work, run, skill, after['revision'])
        ticks.append(run)
    require(len({r['session_id'] for r in ticks}) == 3
            and len({r['idempotency_key'] for r in ticks}) == 3
            and all(r['idempotency_key'] for r in ticks), 'replayed scheduler occurrence')
    restored = documents['manual-and-scheduled-outcomes']
    require(restored['identities'] == {'ManualRunID': manual['id'], 'RunIDs': ids}
            and len(restored['observation']) == 4, 'restored skill identities differ')
    for work, run in zip(restored['observation'], [manual] + ticks):
        pinned(work, run, skill)
    removed = observed('schedule-removal-manual')
    last = captured_run(removed['run'])
    require(not removed['disk']['Jobs'] and last['trigger'] != 'cron'
            and len({manual['id'], last['id'], *ids}) == 5
            and stamp(last['started_at']) > max(stamp(r['finished_at']) for r in ticks), 'schedule removal/manual reuse')
    pinned(removed['work'], last, skill, after['revision'])
    require(disk_runs(removed['disk'])[last['id']][0] == last, 'manual reuse missing from disk')
    require(removed['head']['skill_id'] == skill and removed['head']['bot_id'] == bot
            and removed['head']['revision'] >= after['revision'], 'removed schedule lost updated head')
    require(documents['schedule-removal-manual']['identities'] == {'ManualRunID': last['id']},
            'manual removal receipt identity mismatch')
    inspection = observed('skill-inspection-readonly')
    require(inspection['head'] == removed['head'] and inspection['head'] in inspection['history']['revisions']
            and last in inspection['runs']['runs'], 'missing restored skill history')
    require(len(captures) >= 7, 'incomplete learning run captures')


def agent_evidence_results(state, actual):
    """Evaluate every gate independently; never trust producer outcome flags."""
    invocation(state, actual)
    from agent_adapter_contract import adapter_provenance
    from agent_grounding_contract import source_grounding, stock_result
    validators = {'adapter-provenance': adapter_provenance, 'source-grounding': source_grounding,
                  'document-revision': document_revision, 'conflicting-source-comparison': comparison,
                  'studio-terminal-parity': terminal_parity}
    learning_error = None
    try:
        learning(state, actual)
    except (RuntimeError, KeyError, TypeError, ValueError, OSError) as exc:
        learning_error = str(exc)
    results = {}
    for gate in AGENT_REQUIRED:
        try:
            if gate.startswith('stock-'):
                stock_result(state, actual, gate)
                adapter_provenance(state, actual)
            elif gate in validators:
                validators[gate](state, actual)
            elif gate in LEARNING_GATES:
                require(learning_error is None, 'learning records: ' + str(learning_error))
            else:
                raise RuntimeError('unknown semantic gate: ' + gate)
            results[gate] = {'status': 'passed'}
        except (RuntimeError, KeyError, TypeError, ValueError, OSError) as exc:
            results[gate] = {'status': 'unverified', 'reason': str(exc)}
    return results


def validate_agent_evidence(state, actual):
    try:
        invocation(state, actual)
        require(actual.get('status') == 'verified' and not any(actual.get(k) for k in
                ('unverified', 'unimplemented', 'skipped', 'diagnostic_resume')), 'remaining obligations')
        require(sorted(actual.get('passed', [])) == sorted(AGENT_REQUIRED)
                and set(actual.get('receipts', {})) == set(AGENT_REQUIRED), 'required gates missing/duplicate/unknown')
        results = agent_evidence_results(state, actual)
        failed = [name + ': ' + r['reason'] for name, r in results.items() if r['status'] != 'passed']
        require(not failed, 'semantic validators: ' + '; '.join(failed))
        require(actual['receipts'] == results, 'receipts differ from semantic results')
    except (KeyError, TypeError, ValueError, OSError, RuntimeError) as exc:
        raise RuntimeError('UNVERIFIED agent ' + str(exc)) from exc


if __name__ == '__main__':
    payload = strict_json(sys.stdin.read())
    print(json.dumps(agent_evidence_results(payload['state'], payload['evidence']), sort_keys=True))
