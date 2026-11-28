"""Offline evaluator evidence, NOT a runtime reviewer or a mechanical truth oracle.

An accountable evaluator must inspect all answer claims and retained source bodies,
and judge whether the answer is a useful full result for the original task. This
module checks bindings, coverage, quotations and recorded judgments, not the
evaluator's honesty, semantic entailment or task adequacy itself. No assessment
is generated here and no model is called.
"""
import hashlib
import json
import stat
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlparse

from quality_live_contract import strict_json

CONTRACT = 'bot-agent-grounding-v2'
ASSESSMENT_FILE = 'agent-grounding-assessment.json'
STOCKS = ('stock-1', 'stock-2', 'stock-3')
SUPPORTED = 'supported'
UNSUPPORTED = 'unsupported'
UNVERIFIED = 'unverified'


def require(ok, reason):
    if not ok:
        raise RuntimeError('grounding: ' + reason)


def fields(value, names):
    require(isinstance(value, dict) and set(value) == set(names.split()), 'unknown/missing assessment fields')


def text(value):
    require(isinstance(value, str) and bool(value.strip()), 'missing substantive text')
    return value


def sha(value):
    return hashlib.sha256(value.encode('utf-8')).hexdigest()


def payload_sha(value):
    """Canonical JSON binding independent of Go JSON escaping/whitespace."""
    return sha(json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False))


def fetched_sources(stream):
    """Re-read paired fetches from retained persisted SSE, never answer citations."""
    from agent_live_contract import persisted_content_parts
    calls, results, found, complete = {}, set(), {}, False
    for frame in stream.replace('\r\n', '\n').split('\n\n'):
        data = '\n'.join(line[5:].strip() for line in frame.splitlines() if line.startswith('data:'))
        if not data:
            continue
        event = strict_json(data)
        complete |= event.get('persisted') is True
        for part in persisted_content_parts(event):
            call = part.get('functionCall')
            if call and call.get('name') in ('web_fetch', 'browser_navigate'):
                cid = text(call.get('id'))
                require(cid not in calls, 'duplicate fetch call')
                calls[cid] = call['name']
            result = part.get('functionResponse')
            if not result or result.get('id') not in calls:
                continue
            cid = result['id']
            require(cid not in results and result.get('name') == calls[cid], 'ambiguous fetch result')
            results.add(cid)
            value = result.get('response')
            if not isinstance(value, dict) or value.get('error') or value.get('success') is False:
                continue
            status = value.get('status')
            if isinstance(status, (int, float)) and not 200 <= status < 400:
                continue
            url = value.get('url', '')
            parsed = urlparse(url)
            bodies = {k: v for k, v in value.items() if k in ('content', 'text', 'markdown')
                      and isinstance(v, str) and v.strip()}
            if parsed.scheme not in ('http', 'https') or not parsed.netloc or not bodies:
                continue
            found[cid] = dict(tool=calls[cid], url=url, payload_sha256=payload_sha(value), bodies=bodies)
    require(complete and found, 'no paired retained fetched source body (search snippets are insufficient)')
    return found


def stock_capture(state, actual, key):
    from agent_live_contract import source, run_result, published_answer_matches
    samples = actual.get('samples', [])
    require(len(samples) == 3 and {s['sample'] for s in samples} == set(STOCKS), 'three distinct title samples required')
    require(len({s['run_id'] for s in samples}) == 3 and len({s['bot_id'] for s in samples}) == 3,
            'reused stock run/Bot')
    sample = next(s for s in samples if s['sample'] == key)
    require(sample['prompt'] == 'Stock Market News', 'changed title prompt')
    files = sample['sources']
    load = lambda name, parse=True: source(state, 'agent-' + key + '-' + name,
                                          files['agent-' + key + '-' + name], parse)
    bot = load('bot.json')
    rows = load('runs.json')['runs']
    require(len(rows) == 1 and rows[0]['id'] == sample['run_id'] and bot['id'] == sample['bot_id'], 'foreign title run')
    run = run_result(rows[0], bot['id'])
    require(load('runs-after.json')['runs'] == rows, 'changed restored run')
    for label in ('before', 'after'):
        model = load('model-' + label + '.json')
        require(model['effectiveProvider'] == run['provider'] == 'New AI Core Provider'
                and model['effectiveModel'] == run['model'] == 'anthropic--claude-4.6-sonnet'
                and model['credentialsAvailable'] is True, 'stock resolver mismatch')
    answer = text(run['report']['employee_message']['text'])
    require(run['report']['employee_message']['origin'] == 'agent', 'not agent answer')
    histories = [load('history-' + label + '.json') for label in ('before', 'after')]
    for h in histories:
        reports = [m for m in h['messages'] if m.get('invocationId') == 'bot-report:' + run['id']]
        require(h['id'] == bot['home_session_id'] and len(reports) == 1 and reports[0]['type'] == 'agent'
                and published_answer_matches(reports[0]['content'], answer), 'missing/duplicate/changed full published answer')
    require(histories[0]['messages'] == histories[1]['messages'], 'changed restored history')
    stream = load('worker-events.txt', False)
    return sample, run, answer, fetched_sources(stream)


def assessment(state):
    from agent_live_contract import bound, stamp
    path = Path(state['root']).resolve() / ASSESSMENT_FILE
    require(path.is_file() and not path.is_symlink(), 'missing private ' + ASSESSMENT_FILE)
    require(stat.S_IMODE(path.stat().st_mode) == 0o600, 'assessment must have mode 0600')
    doc = strict_json(path.read_bytes())
    fields(doc, 'contract nonce candidate browser_candidate evaluator inspection_started_at assessed_at coverage_policy samples')
    bound(state, doc)
    require(doc['contract'] == CONTRACT and doc['coverage_policy'] == 'all-answer-content-v1', 'unknown assessment contract/coverage policy')
    fields(doc['evaluator'], 'identity kind method')
    text(doc['evaluator']['identity']); text(doc['evaluator']['method'])
    require(doc['evaluator']['kind'] in ('human', 'agent'), 'unknown evaluator kind')
    require(stamp(doc['inspection_started_at']) <= stamp(doc['assessed_at']) <= datetime.now(timezone.utc), 'invalid/future assessment interval')
    require(isinstance(doc['samples'], list) and len(doc['samples']) == 3
            and {s['sample'] for s in doc['samples']} == set(STOCKS), 'assessment must cover all three samples')
    return doc


def validate_sample(state, actual, doc, key):
    from agent_live_contract import stamp
    sample, run, answer, sources = stock_capture(state, actual, key)
    row = next(s for s in doc['samples'] if s['sample'] == key)
    require(isinstance(row, dict) and 'task_adequacy' in row, key + ': missing task-adequacy judgment')
    fields(row, 'sample bot_id run_id session_id answer_sha256 sources claims nonclaims coverage_statement task_adequacy')
    adequacy = row['task_adequacy']
    fields(adequacy, 'task useful_full_result rationale limitations')
    require(adequacy['task'] == sample['prompt'], key + ': task-adequacy judgment is for a different task')
    text(adequacy['rationale']); text(adequacy['limitations'])
    # The named evaluator owns this semantic judgment. Supported peripheral facts
    # do not establish a useful full answer; no keyword/topic count can prove it.
    require(adequacy['useful_full_result'] is True,
            key + ': not a useful full result: ' + adequacy['rationale'])
    require(row['bot_id'] == run['bot_id'] and row['run_id'] == run['id'] and row['session_id'] == run['session_id'], 'foreign assessment run')
    require(stamp(run['finished_at']) <= stamp(doc['inspection_started_at']), 'assessment predates finished answer')
    require(row['answer_sha256'] == sha(answer) and row['sources'] == sample['sources'], 'changed answer/source digest binding')
    text(row['coverage_statement'])
    claims, nonclaims = row['claims'], row['nonclaims']
    require(isinstance(claims, list) and claims and isinstance(nonclaims, list), 'no claim inventory')
    covered, ids = set(), set()
    def span(item):
        start, end = item['start'], item['end']
        require(type(start) is int and type(end) is int and 0 <= start < end <= len(answer), 'invalid answer span')
        require(item['quote'] == answer[start:end] and item['quote'].strip(), 'answer quote/span mismatch')
        return set(range(start, end))
    for item in nonclaims:
        fields(item, 'start end quote reason')
        text(item['reason']); covered.update(span(item))
    for claim in claims:
        fields(claim, 'id start end quote verdict rationale evidence temporal')
        cid = text(claim['id'])
        require(cid not in ids, 'duplicate claim identity'); ids.add(cid)
        covered.update(span(claim)); text(claim['rationale'])
        require(claim['verdict'] in (SUPPORTED, UNSUPPORTED, UNVERIFIED), 'unknown claim verdict')
        require(claim['verdict'] == SUPPORTED, key + '/' + cid + ': ' + claim['verdict'] + ': ' + claim['rationale'])
        require(isinstance(claim['evidence'], list) and claim['evidence'], 'supported claim lacks decisive evidence')
        for ev in claim['evidence']:
            fields(ev, 'call_id url payload_sha256 field quote rationale')
            require(ev['call_id'] in sources, 'quotation references unfetched source')
            retained = sources[ev['call_id']]
            require(ev['url'] == retained['url'] and ev['payload_sha256'] == retained['payload_sha256'], 'changed cited source digest/URL')
            require(ev['field'] in retained['bodies'] and text(ev['quote']) in retained['bodies'][ev['field']], 'decisive quote absent from retained body')
            text(ev['rationale'])
        temporal = claim['temporal']
        fields(temporal, 'kind verdict as_of date_quote rationale')
        require(temporal['kind'] in ('current', 'historical', 'not-time-sensitive') and temporal['verdict'] == 'aligned', 'stale/unknown temporal support')
        text(temporal['rationale'])
        if temporal['kind'] == 'not-time-sensitive':
            require(temporal['as_of'] is None and temporal['date_quote'] is None, 'timeless claim includes date assertion')
        else:
            day = date.fromisoformat(temporal['as_of'])
            quotation = text(temporal['date_quote'])
            require(any(quotation in sources[e['call_id']]['bodies'][e['field']] for e in claim['evidence']), 'source date quote absent')
            run_day = stamp(run['started_at']).date()
            require(day <= run_day, 'future-dated source claim')
            if temporal['kind'] == 'current':
                require(run_day - day <= timedelta(days=4), 'stale source presented as current')
            else:
                require(temporal['as_of'] in claim['quote'], 'historical claim omits explicit ISO as-of date')
    require(all(i in covered for i, char in enumerate(answer) if not char.isspace()), 'incomplete answer coverage; summary-only assessment')
    return row


def source_grounding(state, actual):
    from agent_live_contract import invocation
    invocation(state, actual)
    doc = assessment(state)
    for key in STOCKS:
        validate_sample(state, actual, doc, key)


def stock_result(state, actual, key):
    from agent_live_contract import invocation
    invocation(state, actual)
    require(key in STOCKS, 'unknown stock sample')
    validate_sample(state, actual, assessment(state), key)
