"""Native worker boundary evidence, not authenticated upstream wire provenance."""
from agent_live_contract import bound, identity, require, source, stamp


def validate_adapter_records(records, runs, start, end, bots):
    require(records and runs, 'missing adapter calls/runs')
    adapters, calls, ordinals, observed = {}, {}, {}, set()
    identity_keys = ('observation_boundary', 'wire_verified', 'pid', 'worker_id', 'adapter_id',
                     'bot_id', 'run_id', 'session_id', 'owner_id', 'scope', 'provider', 'model',
                     'adapter_type', 'adapter_name')
    allowed = set(identity_keys) | {'at', 'phase', 'call_id', 'ordinal', 'stream', 'request_model', 'responses', 'outcome'}
    for r in records:
        require(set(r) <= allowed and all(k in r for k in identity_keys), 'unknown/missing adapter fields')
        for key in ('adapter_id', 'bot_id', 'run_id', 'session_id', 'owner_id'):
            identity(r[key])
        run = runs[r['run_id']]
        bot = bots[run['bot_id']]
        require(r['bot_id'] == bot['id'] and r['owner_id'] == identity(bot['owner_id'])
                and r['scope'] == bot['scope'], 'foreign Bot owner observation')
        worker = (run.get('lease') or {}).get('worker_id')
        if worker:
            require(r['worker_id'] == worker, 'foreign retained lease worker observation')
        # PID is producer-reported namespace metadata only. Without a separate
        # process observer it is not host/container identity attestation.
        require(r['bot_id'] == run['bot_id'] and r['session_id'] == run['session_id']
                and r['scope'] == 'personal' and isinstance(r['worker_id'], str) and r['worker_id']
                and type(r['pid']) is int and r['pid'] > 0, 'foreign worker/run observation')
        require(start <= stamp(run['created_at']) <= stamp(run['started_at']) <= stamp(r['at'])
                <= stamp(run['finished_at']) <= end, 'stale adapter/run interval')
        require(r['observation_boundary'] == 'native-worker-adapter' and r['wire_verified'] is False
                and r['provider'] == run['provider'] == 'New AI Core Provider'
                and r['model'] == run['model'] == r['adapter_name'] == 'anthropic--claude-4.6-sonnet'
                and r['adapter_type'] == '*sap.Provider', 'actual adapter identity mismatch (not resolver parity)')
        aid = r['adapter_id']
        if r['phase'] == 'resolved':
            require(aid not in adapters and not r.get('ordinal') and not r.get('outcome')
                    and r.get('call_id', '00000000-0000-0000-0000-000000000000') == '00000000-0000-0000-0000-000000000000',
                    'duplicate/invalid adapter resolution')
            adapters[aid] = r
            ordinals[aid] = set()
            continue
        require(aid in adapters and all(r[k] == adapters[aid][k] for k in identity_keys)
                and stamp(r['at']) >= stamp(adapters[aid]['at']), 'changed/unresolved adapter instance')
        call = identity(r['call_id'])
        require(type(r['ordinal']) is int and r['ordinal'] > 0 and type(r['stream']) is bool
                and r['request_model'] in ('', r['adapter_name']), 'substituted request model/call')
        if r['phase'] == 'generation-start':
            require(call not in calls and r['ordinal'] not in ordinals[aid] and r['responses'] == 0
                    and not r.get('outcome'), 'duplicate/invalid call start')
            calls[call] = [r, None]
            ordinals[aid].add(r['ordinal'])
        elif r['phase'] == 'generation-end':
            require(call in calls and calls[call][1] is None, 'unpaired/duplicate call end')
            before = calls[call][0]
            require(all(r[k] == before[k] for k in identity_keys + ('ordinal', 'stream', 'request_model'))
                    and stamp(r['at']) >= stamp(before['at']) and type(r['responses']) is int and r['responses'] >= 0
                    and r['outcome'] in ('complete', 'error', 'cancelled', 'consumer-stopped'), 'changed/invalid call end')
            calls[call][1] = r
            observed.add(r['run_id'])
        else:
            raise RuntimeError('unknown adapter phase')
    require(calls and all(pair[1] is not None for pair in calls.values()), 'missing adapter call end')
    require(all(values and values == set(range(1, len(values)+1)) for values in ordinals.values()),
            'factory-only resolution or missing call ordinals')
    require(observed == set(runs), 'required run has no observed adapter invocation')


def adapter_provenance(state, actual):
    require('adapter_manifest' in actual, 'unsupported proof interface: missing native adapter_manifest; resolver metadata is insufficient')
    ref = actual['adapter_manifest']
    require(ref['Source'] == 'agent-native-adapters.json', 'unknown adapter manifest')
    doc = source(state, ref['Source'], ref['SourceHash'])
    bound(state, doc)
    require(doc['contract'] == 'bot-native-adapter-v1' and doc['log_path'] == 'logs/daemon.log', 'adapter boundary contract')
    runs, bots = {}, {}
    def retain_bot(bot):
        bid = identity(bot["id"])
        require(bid not in bots or all(bots[bid][k] == bot[k] for k in ("id", "owner_id", "scope")),
                "changed retained Bot ownership")
        bots[bid] = bot
    def retain(run):
        require(run['id'] not in runs or runs[run['id']] == run, 'changed retained run')
        runs[run['id']] = run
    samples = actual['samples']
    require(len(samples) == 3 and {s['sample'] for s in samples} == {'stock-1','stock-2','stock-3'}, 'three adapter title samples required')
    for sample in samples:
        key, files = sample['sample'], sample['sources']
        load = lambda name: source(state, 'agent-'+key+'-'+name, files['agent-'+key+'-'+name])
        retain_bot(load('bot.json'))
        rows = load('runs.json')['runs']
        require(len(rows) == 1 and rows[0]['id'] == sample['run_id'] and rows[0]['bot_id'] == sample['bot_id'], 'foreign title run')
        model = load('model-before.json')
        require(model['effectiveProvider'] == rows[0]['provider'] and model['effectiveModel'] == rows[0]['model']
                and model['credentialsAvailable'] is True, 'Home/adapter mismatch')
        retain(rows[0])
    general_ref = actual['general_manifest']
    general = source(state, general_ref['Source'], general_ref['SourceHash']); bound(state, general)
    files = general['sources']
    retain_bot(source(state, 'agent-general-bot.json', files['agent-general-bot.json']))
    for run in source(state, 'agent-general-runs.json', files['agent-general-runs.json'])['runs']:
        retain(run)
    require(len(general['journeys']) >= 4, 'missing general adapter journeys')
    # Include every retained learning run, including assignment and post-restart ticks.
    captures = {}
    for receipt in actual['learning']['receipts'].values():
        learning = source(state, receipt['Source'], receipt['SourceHash']); bound(state, learning)
        captures.update(learning['run_captures'])
    require(len(captures) >= 7, 'missing learning adapter runs')
    for rid, sha in captures.items():
        record = source(state, 'agent-skills-run-'+rid+'.json', sha); bound(state, record)
        require(record['run']['id'] == rid, 'foreign learning adapter run')
        retain_bot(record['bot'])
        retain(record['run'])
    validate_adapter_records(doc['records'], runs, stamp(doc['invocation_started_at']), stamp(doc['captured_at']), bots)
