#!/usr/bin/env python3
"""Strict isolated built Studio/native-browser and remote-command PTY gate."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

from skill_surface_acceptance import main as run_surface_gate

AGENT_CASES = {
    'TestBotAgentWorkBrowserAcceptance': ('browser/answer', 'browser/work-states', 'browser/restore-composer'),
    'TestBotAgentWorkPTYAcceptance': ('pty/work-states', 'pty/restore-readonly'),
}


def validate_agent(text):
    passed, evidence = set(), set()
    for line in text.splitlines():
        event = json.loads(line)
        test = event.get('Test', '').split('/')[0]
        if test not in AGENT_CASES:
            continue
        if event.get('Action') in ('fail', 'skip'):
            raise ValueError(f'{test}: {event["Action"]}')
        if event.get('Action') == 'pass' and event.get('Test') == test:
            passed.add(test)
        for case in AGENT_CASES[test]:
            if f'AGENT_SURFACE {case} PASS' in event.get('Output', ''):
                evidence.add(case)
    missing = set(AGENT_CASES) - passed
    missing.update(case for cases in AGENT_CASES.values() for case in cases if case not in evidence)
    if missing:
        raise ValueError('Missing agent surface evidence: ' + ', '.join(sorted(missing)))
    return sorted(evidence)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--browser', action='store_true', help='Built Studio (always enabled).')
    parser.add_argument('--terminal', action='store_true', help='Remote-command PTY (always enabled).')
    parser.add_argument('--agent', action='store_true', help='Also require agent work surface evidence.')
    args = parser.parse_args(argv)
    result = run_surface_gate()
    if result or not args.agent:
        return result
    root = Path(__file__).resolve().parents[2]
    result = subprocess.run(['go', 'test', './pkg/api', './cmd/astonish', '-run', '^TestBotAgentWork(Browser|PTY)Acceptance$', '-count=1', '-json', '-timeout=5m'], cwd=root, env=dict(os.environ, ASTONISH_BOT_BROWSER_ACCEPTANCE='1'), text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode:
        return result.returncode
    try:
        cases = validate_agent(result.stdout)
    except (ValueError, json.JSONDecodeError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    print(json.dumps({'agent_surface_cases': cases, 'status': 'passed'}))
    return 0


if __name__ == '__main__':
    sys.exit(main())
