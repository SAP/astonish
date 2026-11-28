#!/usr/bin/env python3
"""Offline exact Task obligations; never a runtime/model reviewer.

Read {state,evidence} from stdin and emit only the Go taskRequired result map.
Missing decisive evidence is unverified, contradiction/corruption is failed.
The current Go collector observes three title starts, not a real Task lifecycle
or accountable all-claim adequacy assessment. Its booleans/fixture passes are
not substitutes for those independent obligations. No network or paid calls.
"""
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

from agent_live_contract import (digest, identity, persisted_content_parts,
                                 published_answer_matches, run_result, source, stamp)
from quality_live_contract import strict_json

CONTRACT = 'bot-task-live-v1'
PROVIDER = 'sap_ai_core_sci'
MODEL = 'anthropic--claude-4.6-sonnet'
STOCKS = ('stock-1', 'stock-2', 'stock-3')
REQUIRED = (*STOCKS, 'compatibility', 'workspace', 'configured-search-first',
            'adapter-provenance', 'source-grounding', 'execution', 'continuity',
            'owner-correction', 'python-helper-sqlite-state', 'task-three-scheduler-occurrences',
            'schedule-delete-manual-reuse', 'restart', 'postgres-backup-restore',
            'isolation-permissions-cancellation-no-replay', 'studio-terminal-parity')
MISSING = {
    'compatibility': 'Fresh-candidate compatibility journey absent; controlled fixtures are independent.',
    'workspace': 'Independent physical Home bytes, registry heads and committed run pins not decisively validated.',
    'configured-search-first': 'Three bound retained successful configured searches before decisive detail retrieval required.',
    'adapter-provenance': 'Retained adapter records need exact fresh run/session/provider binding; resolver booleans are insufficient.',
    'source-grounding': 'Accountable original-assignment adequacy and all-answer-claim assessment against decisive retained sources absent.',
    'execution': 'Same Task manual invocation with committed revision and executable asset pins absent.',
    'continuity': 'Real fresh-candidate Task lifecycle continuity evidence absent.',
    'owner-correction': 'Scoped owner correction and subsequent same-Task revision reuse absent.',
    'python-helper-sqlite-state': 'Retained validated Python helper execution and persistent SQLite state reuse absent.',
    'task-three-scheduler-occurrences': 'Three actual fresh-data scheduler occurrences of the same retained Task absent.',
    'schedule-delete-manual-reuse': 'Actual schedule removal followed by manual reuse of the same retained Task absent.',
    'restart': 'Owned candidate process restart and same physical Home/Task/state reuse absent.',
    'postgres-backup-restore': 'Owned PostgreSQL quiescent full-Home backup/restore journey absent.',
    'isolation-permissions-cancellation-no-replay': 'Fresh-candidate scoped isolation, lease, revocation, cancellation and uncertain-effect no-replay journeys absent.',
    'studio-terminal-parity': 'Built Studio states and independent real PTY live/reconnect ordered refs/results parity absent.',
}


class MissingEvidence(RuntimeError):
    """An obligation is not yet exercised, rather than contradicted."""


def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)


def invocation(state, evidence):
    require(isinstance(state, dict) and isinstance(evidence, dict), 'invalid state/evidence objects')
    require(state.get('workflow') == evidence.get('contract') == CONTRACT
            and state.get('task_config_verified') is True and state.get('task_attempted') is True,
            'fresh verified Task workflow binding unavailable')
    require(all(digest(state.get(k)) for k in ('binary_sha256', 'browser_sha256', 'task_nonce'))
            and evidence.get('nonce') == state['task_nonce']
            and evidence.get('candidate') == state['binary_sha256']
            and evidence.get('browser_candidate') == state['browser_sha256'], 'foreign/replayed candidate or nonce')
    require(not any(state.get(k) or evidence.get(k) for k in
                    ('diagnostic_resume', 'browser_refreshes', 'skipped', 'unimplemented')),
            'resumed/refreshed/skipped invocation cannot certify Tasks')
    require(state.get('task_intended') == dict(provider=PROVIDER, provider_type='sap_ai_core', model=MODEL),
            'Task intended provider/model mismatch')
    route = state.get('task_search')
    require(isinstance(route, dict) and set(route) == {'engine', 'provider', 'model', 'tool', 'fingerprint'}
            and route.get('engine') in ('perplexity', 'tavily')
            and isinstance(route.get('tool'), str) and bool(route['tool'].strip())
            and isinstance(route.get('fingerprint'), str) and bool(route['fingerprint'].strip())
            and evidence.get('configured_search') == route, 'configured search route binding mismatch')
    require(route['engine'] != 'perplexity' or bool(route['provider'] and route['model']),
            'configured Perplexity identity absent')
    require(stamp(evidence.get('invocation_started_at')) <= datetime.now(timezone.utc),
            'future invocation timestamp')
    root = Path(state['root'])
    require(root.is_absolute() and root.is_dir() and not root.is_symlink(), 'owned evidence root absent/symlink')


def search_first(stream, route):
    calls, results, searched, details, complete = {}, set(), False, 0, False
    for frame in stream.replace('\r\n', '\n').split('\n\n'):
        data = '\n'.join(line[5:].strip() for line in frame.splitlines() if line.startswith('data:'))
        if not data:
            continue
        event = strict_json(data)
        complete |= event.get('persisted') is True
        for part in persisted_content_parts(event):
            call = part.get('functionCall')
            if call:
                cid, tool = call.get('id'), call.get('name')
                require(isinstance(cid, str) and cid and cid not in calls, 'missing/duplicate tool call identity')
                calls[cid] = tool
                if tool in ('web_fetch', 'http_request', 'browser_navigate'):
                    require(searched, 'detail fetch preceded successful configured search')
            result = part.get('functionResponse')
            if not result:
                continue
            cid, tool = result.get('id'), result.get('name')
            require(cid in calls and cid not in results and calls[cid] == tool, 'orphan/mismatched/duplicate tool result')
            results.add(cid)
            value = result.get('response')
            if tool == 'perplexity_web_search':
                require(isinstance(value, dict) and not value.get('error') and value.get('success') is not False
                        and isinstance(value.get('query'), str) and value['query'].strip()
                        and bool(value.get('citations') or value.get('search_results')), 'configured search failed or empty')
                expected = 'tavily' if route['engine'] == 'tavily' else route['provider']
                require(value.get('provider') == expected
                        and (route['engine'] != 'perplexity' or value.get('model') == route['model']),
                        'configured search provider/model mismatch')
                searched = True
            if tool in ('web_fetch', 'http_request', 'browser_navigate') and isinstance(value, dict):
                status = value.get('status')
                if (not value.get('error') and value.get('success') is not False
                        and (not isinstance(status, (int, float)) or 200 <= status < 400)
                        and any(isinstance(value.get(k), str) and value[k].strip() for k in ('content', 'text', 'markdown'))):
                    details += 1
    require(complete and searched and details, 'successful configured search then retained detail body absent')


def sample_capture(state, evidence, sample):
    key = sample['sample']
    require(sample.get('prompt') == 'Stock Market News', 'changed original assignment')
    require(sample.get('status') != 'failed', 'sample collector failed: ' + str(sample.get('blocker', 'unknown failure')))
    files = sample.get('sources')
    if not isinstance(files, dict) or not files:
        raise MissingEvidence('retained sample file manifest absent')
    # Verify every retained file even if it is not consumed below. Booleans and
    # self-reported source_payloads never override raw hashed bytes.
    for name, sha in files.items():
        require(name.startswith('task-' + key + '-'), 'foreign sample file reference')
        source(state, name, sha, False)
    def load(label, parse=True):
        name = 'task-' + key + '-' + label
        if name not in files:
            raise MissingEvidence('required retained file absent: ' + label)
        return source(state, name, files[name], parse)
    identity(sample['bot_id']); identity(sample['run_id'])
    bot = load('bot.json')
    run = run_result(load('run.json'), sample['bot_id'])
    require(bot['id'] == run['bot_id'] == sample['bot_id'] and run['id'] == sample['run_id'], 'foreign sample run/Bot')
    require(load('runs.json')['runs'] == [run], 'missing/duplicate/changed title run')
    require(stamp(run['created_at']) >= stamp(evidence['invocation_started_at']), 'replayed title run predates invocation')
    home = load('model-before.json')
    require(home.get('effectiveProvider') == run['provider'] == PROVIDER
            and home.get('effectiveModel') == run['model'] == MODEL
            and home.get('credentialsAvailable') is True, 'Home/worker provider identity mismatch')
    answer = run['report']['employee_message']['text']
    require(isinstance(answer, str) and answer.strip() and run['report']['employee_message']['origin'] == 'agent', 'substantive agent answer absent')
    before, after = load('history-before.json'), load('history-after.json')
    require(before['messages'] == after['messages'], 'restored ordered messages changed')
    for history in (before, after):
        reports = [m for m in history['messages'] if m.get('invocationId') == 'bot-report:' + run['id']]
        require(history['id'] == bot['home_session_id'] and len(reports) == 1
                and reports[0].get('type') == 'agent'
                and published_answer_matches(reports[0].get('content'), answer), 'missing/duplicate/drifted published answer')
    search_first(load('worker-events.txt', False), evidence['configured_search'])


def evaluate(state, evidence):
    result = {name: dict(status='unverified', reason=MISSING.get(name,
                  'Retained title result requires accountable assignment adequacy and all-claim grounding assessment.'))
              for name in REQUIRED}
    try:
        invocation(state, evidence)
    except (RuntimeError, ValueError, KeyError, TypeError, OSError) as error:
        return {name: dict(status='failed', reason='Invalid invocation: ' + str(error)) for name in REQUIRED}
    samples = evidence.get('samples', [])
    if not isinstance(samples, list) or any(not isinstance(s, dict) or s.get('sample') not in STOCKS for s in samples):
        for key in STOCKS:
            result[key] = dict(status='failed', reason='Unknown/malformed title sample inventory')
        return result
    keys = [s['sample'] for s in samples]
    if len(keys) != len(set(keys)):
        for key in STOCKS:
            result[key] = dict(status='failed', reason='Duplicate title sample identity')
        return result
    captured = []
    for sample in samples:
        key = sample['sample']
        try:
            sample_capture(state, evidence, sample)
            captured.append(sample)
        except MissingEvidence as error:
            result[key] = dict(status='unverified', reason=str(error))
        except (RuntimeError, ValueError, KeyError, TypeError, OSError) as error:
            result[key] = dict(status='failed', reason=str(error))
    if len(captured) == 3:
        if len({s['run_id'] for s in captured}) != 3 or len({s['bot_id'] for s in captured}) != 3:
            for key in STOCKS:
                result[key] = dict(status='failed', reason='Reused title Bot/run identity')
        else:
            result['configured-search-first'] = dict(status='passed', reason='Three distinct bound raw transcripts contain selected configured search before successful detail-body retrieval.')
    # No current collector emits decisive lifecycle or accountable semantic
    # assessment documents. Do not guess a PASS from fixture receipts, flags,
    # adapter file presence, retained snapshots alone or a completed model call.
    return result


def main(stdin=None):
    try:
        document = strict_json((stdin or sys.stdin).read())
        require(isinstance(document, dict) and set(document) == {'state', 'evidence'}, 'exact state/evidence envelope required')
        result = evaluate(document['state'], document['evidence'])
        print(json.dumps(result, separators=(',', ':'), allow_nan=False))
        return 0  # valid protocol, not acceptance; Go inspects every exact status
    except (RuntimeError, ValueError, KeyError, TypeError, OSError):
        print('Invalid Task evidence input; no acceptance produced', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
