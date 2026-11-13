#!/usr/bin/env python3
"""Strict isolated built-SPA/native-browser + remote-command PTY gate.

This proves controlled surface behavior, not real-provider or daemon work.
All case markers must originate in their passing Go test's JSON output.
"""
import json
import os
from pathlib import Path
import subprocess
import sys

CASES = {
    'TestBotSkillsBrowserAcceptance': ('browser/empty', 'browser/import-cancel', 'browser/import-success', 'browser/history', 'browser/schema', 'browser/stale-retry', 'browser/legacy-schedule', 'browser/home-readonly'),
    'TestBotSkillsPTYAcceptance': ('pty/empty', 'pty/history', 'pty/schema', 'pty/receipts', 'pty/stale-retry', 'pty/escape-readonly'),
}

def validate(text):
    passed, evidence = set(), set()
    for line in text.splitlines():
        event = json.loads(line)
        test = event.get('Test')
        if test not in CASES:
            continue
        if event.get('Action') in ('fail', 'skip'):
            raise ValueError(f'{test}: {event["Action"]}')
        if event.get('Action') == 'pass':
            passed.add(test)
        output = event.get('Output', '')
        for case in CASES[test]:
            if f'SKILL_SURFACE {case} PASS' in output:
                evidence.add(case)
    missing = set(CASES) - passed
    missing.update(case for cases in CASES.values() for case in cases if case not in evidence)
    if missing:
        raise ValueError('Missing surface evidence: ' + ', '.join(sorted(missing)))
    return sorted(evidence)

def main():
    root = Path(__file__).resolve().parents[2]
    env = dict(os.environ, ASTONISH_BOT_BROWSER_ACCEPTANCE='1')
    command = ['go', 'test', './pkg/api', './cmd/astonish', '-run', '^TestBotSkills(Browser|PTY)Acceptance$', '-count=1', '-json', '-timeout=5m']
    result = subprocess.run(command, cwd=root, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode:
        return result.returncode
    try:
        cases = validate(result.stdout)
    except (ValueError, json.JSONDecodeError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(json.dumps({'controlled_surface_cases': cases, 'status': 'passed'}))
    return 0

if __name__ == '__main__':
    sys.exit(main())
