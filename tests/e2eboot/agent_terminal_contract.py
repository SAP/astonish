"""Offline PTY/Studio proof contract. Record consistency is not live provenance.

Required additions to the producer (absence is UNVERIFIED, never inferred):
* terminal frames: {encoding: 'base64', data: raw PTY bytes, rows: int, cols: int}
* studio_manifest -> agent-studio.json: invocation binding, bot_id, url, two
  passes with the same exchanges plus rendered {invocation_id, text, visible,
  run_states, skills}, captured from the Studio DOM/rendered component dataset.
* state.agent_cli_sha256: independently retained native candidate build digest.

Studio exchanges are actual snapshot/skills/SSE responses, not reserialized
terminal records. rendered.text is the visible canonical final bubble's DOM text.
Only a conservative terminal escape subset is interpreted; unknown controls fail
closed rather than treating hidden output as a visible answer.
"""
import base64
import re
from urllib.parse import urlsplit, parse_qs
from quality_live_contract import strict_json

TERMINAL_CONTRACT = 'bot-agent-terminal-v1'
STUDIO_CONTRACT = 'bot-agent-studio-v1'


def check(ok, reason):
    if not ok:
        raise RuntimeError('terminal parity: ' + reason)


def positive(value):
    return type(value) is int and value > 0


def records(exchanges, bot):
    """Decode actual API payloads; preserve transcript order and reject conflicts."""
    from agent_live_contract import identity
    check(isinstance(exchanges, list) and exchanges, 'missing exchanges')
    endpoints, events = {}, []
    base = '/api/studio/bots/' + bot
    for row in exchanges:
        check(type(row['status']) is int and row['status'] == 200, 'failed endpoint read')
        url = urlsplit(row['path'])
        query = parse_qs(url.query, keep_blank_values=True)
        check(not url.scheme and not url.netloc and not url.fragment
              and (url.path == base or url.path.startswith(base + '/'))
              and query.get('scope', ['personal']) == ['personal']
              and all(k in ('scope', 'after', 'limit') for k in query), 'foreign endpoint')
        if url.path.endswith('/events'):
            fields = {}
            for line in row['body'].replace('\r\n', '\n').splitlines():
                if not line or line.startswith(':'):
                    continue
                name, sep, value = line.partition(':')
                check(sep and name in ('id', 'event', 'data') and name not in fields,
                      'ambiguous SSE frame')
                fields[name] = value.lstrip(' ')
            kind = fields.get('event')
            check(kind in ('transcript', 'replay_complete'), 'unsupported event')
            if kind == 'transcript':
                sid, colon, cursor = fields['id'].rpartition(':')
                identity(sid)
                check(colon and cursor.isascii() and cursor.isdigit() and int(cursor) > 0,
                      'invalid transcript cursor')
                check(url.path == base + '/sessions/' + sid + '/events', 'foreign transcript session')
                check(not events or (sid == events[-1][0] and int(cursor) > events[-1][1]),
                      'stale/duplicate transcript sequence')
                events.append((sid, int(cursor), strict_json(fields['data'])))
            continue
        value = strict_json(row['body'])
        check(url.path not in endpoints or endpoints[url.path] == value, 'changed endpoint response')
        endpoints[url.path] = value
    check(events, 'missing ordered transcript')
    snapshot = endpoints[base + '/snapshot']
    home = identity(snapshot['bot']['home_session_id'])
    check(snapshot['bot']['id'] == bot and all(sid == home for sid, _, _ in events), 'foreign Bot/Home')
    check(positive(snapshot['latest_sequence']) and positive(snapshot['history_cursor'])
          and events[-1][1] == snapshot['history_cursor'], 'stale snapshot/history cursor')
    states = {}
    last_sequence = 0
    for _, _, event in events:
        state = (event.get('actions') or {}).get('stateDelta', {}).get('bot_run_state')
        if state is None:
            continue
        rid = identity(state['run_id'])
        identity(state['session_id'])
        check(state['schema_version'] == 1 and state['bot_id'] == bot and state['home_session_id'] == home
              and positive(state['state_sequence']) and last_sequence < state['state_sequence'] <= snapshot['latest_sequence'],
              'foreign/stale run-state sequence')
        work = state.get('work')
        if work is not None:
            check(work['schema_version'] == 1 and work['protocol'] == 'agent_led_v1'
                  and work['bot_id'] == bot and work['run_id'] == rid and positive(work['work_revision']),
                  'foreign/unknown work projection')
            prior = (states.get(rid) or {}).get('work')
            check(prior is None or work['work_revision'] > prior['work_revision'] or work == prior,
                  'regressing/conflicting work revision')
        last_sequence = state['state_sequence']
        states[rid] = state
    check(states, 'missing run-state projection')
    snapshot_states = snapshot['run_states']
    check(len({s['run_id'] for s in snapshot_states}) == len(snapshot_states)
          and {s['run_id']: s for s in snapshot_states} == states, 'snapshot/transcript projection differs')
    return endpoints, events, snapshot


def final_result(snapshot, events, bot):
    from agent_live_contract import run_result
    run = run_result(snapshot['latest_outcome'], bot)
    state = next((s for s in snapshot['run_states'] if s['run_id'] == run['id']), None)
    check(state is not None and state['session_id'] == run['session_id']
          and state['status'] == run['status'] and state['work'] == run['work'], 'final projection differs')
    message = run['report']['employee_message']
    check(message['origin'] == 'agent' and isinstance(message['text'], str) and message['text'].strip(),
          'missing canonical final answer')
    invocation = 'bot-report:' + run['id']
    reports = [e for _, _, e in events if e.get('invocationId') == invocation]
    check(len(reports) == 1, 'missing/duplicate final invocation')
    parts = (reports[0].get('content') or {}).get('parts', [])
    check(reports[0]['content'].get('role') == 'model' and parts
          and all(set(p) == {'text'} for p in parts)
          and ''.join(p['text'] for p in parts).strip() == message['text'].strip(),
          'final invocation content differs/private thought')
    return run, invocation, message['text']


def skills(endpoints, bot, run):
    from agent_live_contract import digest, identity
    heads = endpoints['/api/studio/bots/' + bot + '/skills']['skills']
    check(isinstance(heads, list) and len({h['id'] for h in heads}) == len(heads), 'duplicate skill heads')
    for head in heads:
        identity(head['id'])
        check(head['bot_id'] == bot and positive(head['head_revision']) and digest(head['head_digest'])
              and isinstance(head['title'], str) and head['title'].strip(), 'foreign/invalid skill head')
    receipt = run['report'].get('skill')
    if receipt:
        check(receipt['bot_id'] == bot and receipt['run_id'] == run['id'] and receipt['schema_version'] == 1,
              'foreign skill receipt')
        revision = receipt.get('committed_revision') or receipt['used_revision']
        matches = [h for h in heads if h['id'] == receipt['skill_id']]
        check(len(matches) == 1 and matches[0]['head_revision'] == revision and matches[0]['head_digest'] == receipt['digest'],
              'final skill reference differs')


def live_terminal_delivery(exchanges, run):
    """Replayed history is not live delivery, even if its screen looks correct.

    Require the latest run's state and canonical publication on the first CLI
    connection AFTER its actual SSE replay_complete barrier. The observer must
    already be connected when the journey starts work; receipt flags cannot
    substitute for these wire records.
    """
    replay_done, live_state, live_report = False, False, False
    for row in exchanges:
        if not urlsplit(row['path']).path.endswith('/events'):
            continue
        fields = {}
        for line in row['body'].splitlines():
            name, sep, value = line.partition(':')
            if sep:
                fields[name] = value.lstrip(' ')
        if fields.get('event') == 'replay_complete':
            check(not replay_done, 'multiple replay barriers on live CLI connection')
            replay_done = True
        elif replay_done and fields.get('event') == 'transcript':
            event = strict_json(fields['data'])
            projection = (event.get('actions') or {}).get('stateDelta', {}).get('bot_run_state')
            if projection and projection.get('run_id') == run['id']:
                live_state = True
            if event.get('invocationId') == 'bot-report:' + run['id']:
                live_report = True
    check(replay_done and live_state and live_report,
          'first CLI connection lacks live run-state and publication after replay_complete; replay-only capture')


def terminal_parity(state, actual):
    try:
        _terminal_parity(state, actual)
    except (KeyError, TypeError, ValueError, IndexError, AttributeError, OverflowError, RecursionError) as exc:
        raise RuntimeError('terminal parity: malformed/incomplete capture: ' + str(exc)) from exc


def _terminal_parity(state, actual):
    from agent_live_contract import bound, digest, identity, source
    documents = []
    for key, name, contract in (('terminal_manifest', 'agent-terminal.json', TERMINAL_CONTRACT),
                                 ('studio_manifest', 'agent-studio.json', STUDIO_CONTRACT)):
        check(key in actual, 'missing ' + key)
        ref = actual[key]
        check(ref['Source'] == name, 'unknown manifest')
        doc = source(state, ref['Source'], ref['SourceHash'])
        bound(state, doc)
        check(doc['contract'] == contract, 'unknown capture contract')
        documents.append(doc)
    terminal, studio = documents
    bot = identity(state['bot_id'])
    url = urlsplit(state['url'])
    check(url.scheme == 'http' and url.hostname == '127.0.0.1' and url.port not in (None, 9393)
          and not url.username and not url.password and not url.path and not url.query and not url.fragment,
          'not an isolated candidate URL')
    check(all(d['bot_id'] == bot and d['url'] == state['url'] for d in documents), 'foreign capture Bot/URL')
    check(digest(state.get('agent_cli_sha256')) and terminal['cli_sha256'] == state['agent_cli_sha256'],
          'missing/foreign independently bound CLI hash')
    check(type(terminal['blocked_mutations']) is int and terminal['blocked_mutations'] == 0
          and terminal['comparison_error'] == '', 'mutation or producer comparison failure')
    check(all(isinstance(d['passes'], list) and len(d['passes']) == 2 for d in documents), 'two independent captures required')
    canonical = None
    for pass_index, (terminal_pass, studio_pass) in enumerate(zip(terminal['passes'], studio['passes'])):
        observed = records(terminal_pass['exchanges'], bot)
        browser = records(studio_pass['exchanges'], bot)
        check(observed == browser, 'Studio/terminal records differ')
        check(canonical is None or canonical == observed, 'reconnect changed persisted records')
        canonical = observed
        endpoints, events, snapshot = observed
        run, invocation, text = final_result(snapshot, events, bot)
        if pass_index == 0:
            live_terminal_delivery(terminal_pass['exchanges'], run)
        skills(endpoints, bot, run)
        rendered = studio_pass['rendered']
        check(rendered['visible'] is True and rendered['invocation_id'] == invocation
              and isinstance(rendered['text'], str) and rendered['text'].strip(), 'missing/foreign Studio final bubble')
        check(rendered['run_states'] == snapshot['run_states']
              and rendered['skills'] == endpoints['/api/studio/bots/' + bot + '/skills']['skills'],
              'Studio rendered work/skill dataset differs')
        # Deliberately conservative: no guessed Markdown stripping or fuzzy matching.
        check(normalize(rendered['text']) == normalize(text), 'Studio final text differs from publication')
        frames = terminal_pass['frames']
        check(isinstance(frames, dict) and all(k in frames for k in ('home', 'status', 'skills', 'bots')),
              'missing terminal frame')
        screens = {name: screen(frame) for name, frame in frames.items()}
        check(normalize(rendered['text']) in normalize(screens['home']), 'final answer absent from visible terminal Home')
        check('Next run:' in screens['status'] and 'skills' in screens['skills'].lower() and 'Bots' in screens['bots'],
              'missing rendered inspectors')
        work = run['work']
        label = {'answered': 'Answer provided', 'complete': 'Completed by agent'}[work['disposition']]
        check(label in screens['status'], 'work disposition not rendered')
        for item in work.get('items') or []:
            label = 'Completed by agent' if item['status'] == 'completed' else item['status'].replace('_', ' ')
            check(normalize(label + ': ' + item['summary']) in normalize(screens['status']), 'work item not rendered')
            if item.get('reason'):
                check(normalize(item['reason']) in normalize(screens['status']), 'work reason not rendered')
        for feedback in work.get('checks') or []:
            label = ('Mechanical observation' if feedback['kind'] == 'mechanical_observation' else 'Feedback received') if feedback['status'] == 'returned' else 'Check unavailable'
            check(normalize(label + ': ' + feedback['summary']) in normalize(screens['status']), 'check feedback not rendered')
        for head in endpoints['/api/studio/bots/' + bot + '/skills']['skills']:
            check(normalize(head['title']) in normalize(screens['skills'])
                  and 'revision ' + str(head['head_revision']) in screens['skills']
                  and head['head_digest'] in screens['skills'], 'skill reference not rendered')


def normalize(value):
    return ' '.join(value.split())


def screen(frame):
    """Reconstruct the final visible screen, not concatenated terminal writes.

    Unknown controls, concealed text and wide/combining glyphs are unverified;
    they must not be silently stripped into a false positive. This intentionally
    small decoder is not a general-purpose VT emulator.
    """
    import unicodedata
    check(isinstance(frame, dict), 'raw encoded terminal frame required')
    check(frame.get('encoding') == 'base64' and isinstance(frame.get('data'), str), 'invalid frame encoding')
    rows, cols = frame['rows'], frame['cols']
    check(positive(rows) and positive(cols) and rows <= 240 and cols <= 500, 'invalid frame dimensions')
    check(0 < len(frame['data']) <= 8 * 1024 * 1024, 'invalid frame size')
    raw = base64.b64decode(frame['data'], validate=True)
    check(base64.b64encode(raw).decode() == frame['data'], 'noncanonical frame encoding')
    text = raw.decode('utf-8')
    grid = [[' '] * cols for _ in range(rows)]
    y = x = i = 0
    top, bottom = 0, rows - 1

    def line_feed(row):
        if row == bottom:
            grid.pop(top)
            grid.insert(bottom, [' '] * cols)
            return row
        return min(rows - 1, row + 1)

    while i < len(text):
        char = text[i]
        if char == '\x1b':
            # Exact non-rendering requests answered by agentTerminalRespond. OSC
            # titles, hyperlinks, clipboard operations and arbitrary queries are
            # not stripped: those remain unsupported evidence.
            query = re.match(r'\x1b\](?:10|11);\?(?:\x07|\x1b\\)|\x1b\[(?:6n|0?c|\?202[67]\$p|>4(?:;2)?m|>1u|\?u|\?5W)', text[i:])
            if query:
                i += len(query.group())
                continue
            match = re.match(r'\x1b\[([?0-9;]*)([A-Za-z])', text[i:])
            check(match is not None, 'unsupported terminal escape')
            args, command = match.groups()
            i += len(match.group())
            if args.startswith('?'):
                check(args in ('?25', '?1049', '?2004', '?1004', '?1002', '?1006', '?2026') and command in ('h', 'l'), 'unsupported terminal mode')
                if args == '?1049':
                    check(command == 'h', 'alternate screen already closed')
                    grid = [[' '] * cols for _ in range(rows)]
                    y = x = 0
                continue
            values = [int(n) if n else 0 for n in args.split(';')]
            n = values[0] or 1
            check(len(values) <= 2 or command == 'm', 'invalid terminal parameters')
            if command == 'm':
                # NO_COLOR candidate capture only. Conceal and explicit colors
                # can hide text against the background; never strip those into
                # an apparent visible answer. Inverse is used by the real input
                # cursor and preserves the default foreground/background pair.
                check(all(v in (0, 1, 3, 4, 7, 9, 22, 23, 24, 27, 29, 39, 49)
                          for v in values), 'unsupported/concealed terminal style')
            elif command in ('H', 'f'):
                y, x = n - 1, (values[1] or 1) - 1 if len(values) == 2 else 0
            elif command == 'A':
                y = max(0, y - n)
            elif command == 'B':
                y = min(rows - 1, y + n)
            elif command == 'C':
                x = min(cols - 1, x + n)
            elif command == 'D':
                x = max(0, x - n)
            elif command == 'G':
                x = n - 1
            elif command == 'd':
                y = n - 1
            elif command == 'r':
                top = n - 1
                bottom = (values[1] or rows) - 1 if len(values) == 2 else rows - 1
                check(0 <= top < bottom < rows, 'invalid terminal scroll region')
                y = x = 0  # DECOM remains off; setting margins homes the cursor.
            elif command in ('S', 'T'):
                check(len(values) == 1, 'invalid scroll parameters')
                count = min(n, bottom - top + 1)
                blank = [[' '] * cols for _ in range(count)]
                if command == 'S':
                    grid[top:bottom+1] = grid[top+count:bottom+1] + blank
                else:
                    grid[top:bottom+1] = blank + grid[top:bottom+1-count]
            elif command in ('L', 'M'):
                check(len(values) == 1, 'invalid line-edit parameters')
                if top <= y <= bottom:
                    count = min(n, bottom - y + 1)
                    if command == 'L':
                        grid[y:bottom+1] = [[' '] * cols for _ in range(count)] + grid[y:bottom+1-count]
                    else:
                        grid[y:bottom+1] = grid[y+count:bottom+1] + [[' '] * cols for _ in range(count)]
                x = 0
            elif command == 'X':
                check(len(values) == 1, 'invalid erase-character parameters')
                for col in range(min(x, cols - 1), min(cols, x + n)):
                    grid[y][col] = ' '
            elif command in ('J', 'K'):
                mode = values[0]
                check(mode in (0, 1, 2) and len(values) == 1, 'unsupported erase')
                for row in range(rows):
                    for col in range(cols):
                        selected = command == 'J' or row == y
                        position, cursor = (row * cols + col, y * cols + x) if command == 'J' else (col, x)
                        if selected and (mode == 2 or mode == 0 and position >= cursor or mode == 1 and position <= cursor):
                            grid[row][col] = ' '
            else:
                check(False, 'unsupported terminal control ' + command)
            check(0 <= y < rows and 0 <= x <= cols, 'terminal cursor outside capture')
            continue
        i += 1
        if char == '\r':
            x = 0
        elif char == '\n':
            y = line_feed(y)
        elif char == '\b':
            x = max(0, x - 1)
        elif char == '\t':
            x = min(cols - 1, (x // 8 + 1) * 8)
        else:
            check(char.isprintable() and not unicodedata.combining(char)
                  and unicodedata.east_asian_width(char) not in ('W', 'F'), 'unsupported terminal glyph/control')
            if x == cols:
                x = 0
                y = line_feed(y)
            grid[y][x] = char
            x += 1
    return '\n'.join(''.join(row).rstrip() for row in grid).rstrip()
