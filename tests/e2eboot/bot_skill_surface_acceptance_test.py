import io
import json
import subprocess
import unittest
from unittest.mock import patch

import bot_skill_surface_acceptance as entrypoint


def complete_evidence():
    rows = []
    for test, cases in entrypoint.AGENT_CASES.items():
        for case in cases:
            rows.append({'Test': test, 'Action': 'output', 'Output': f'AGENT_SURFACE {case} PASS'})
        rows.append({'Test': test, 'Action': 'pass'})
    return rows


class BotSkillSurfaceEntrypointTest(unittest.TestCase):
    def test_flags_delegate_to_complete_gate(self):
        for flags in ([], ['--browser'], ['--terminal'], ['--browser', '--terminal']):
            with self.subTest(flags=flags), patch.object(entrypoint, 'run_surface_gate', return_value=0) as gate:
                self.assertEqual(entrypoint.main(flags), 0)
                gate.assert_called_once_with()

    def test_failure_propagates(self):
        with patch.object(entrypoint, 'run_surface_gate', return_value=1):
            self.assertEqual(entrypoint.main(['--browser', '--terminal']), 1)

    def test_unknown_flags_rejected(self):
        with patch.object(entrypoint, 'run_surface_gate') as gate, self.assertRaises(SystemExit) as raised:
            entrypoint.main(['--skip-terminal'])
        self.assertEqual(raised.exception.code, 2)
        gate.assert_not_called()

    def test_agent_requires_all_cases_and_both_tests(self):
        rows = complete_evidence()
        encode = lambda data: '\n'.join(json.dumps(row) for row in data)
        self.assertEqual(len(entrypoint.validate_agent(encode(rows))), sum(map(len, entrypoint.AGENT_CASES.values())))
        for i in range(len(rows)):
            with self.subTest(missing=i), self.assertRaises(ValueError):
                entrypoint.validate_agent(encode(rows[:i] + rows[i+1:]))
        for action in ('fail', 'skip'):
            for test in entrypoint.AGENT_CASES:
                for suffix in ('', '/active'):
                    with self.subTest(action=action, test=test+suffix), self.assertRaises(ValueError):
                        entrypoint.validate_agent(encode(rows + [{'Test': test+suffix, 'Action': action}]))
        with self.assertRaises(ValueError):
            entrypoint.validate_agent('not JSON')

    def test_agent_runs_strict_browser_and_terminal(self):
        text = '\n'.join(json.dumps(row) for row in complete_evidence())
        process = subprocess.CompletedProcess([], 0, text, '')
        with patch.object(entrypoint, 'run_surface_gate', return_value=0) as gate, patch.object(entrypoint.subprocess, 'run', return_value=process) as run, patch('sys.stdout', new_callable=io.StringIO):
            self.assertEqual(entrypoint.main(['--agent']), 0)
        gate.assert_called_once_with()
        command = run.call_args.args[0]
        self.assertIn('./pkg/api', command)
        self.assertIn('./cmd/astonish', command)
        self.assertIn('^TestBotAgentWork(Browser|PTY)Acceptance$', command)
        self.assertEqual(run.call_args.kwargs['env']['ASTONISH_BOT_BROWSER_ACCEPTANCE'], '1')

    def test_agent_never_passes_missing_or_failed_evidence(self):
        for code, output in ((1, ''), (0, ''), (0, 'not JSON')):
            with self.subTest(code=code, output=output), patch.object(entrypoint, 'run_surface_gate', return_value=0), patch.object(entrypoint.subprocess, 'run', return_value=subprocess.CompletedProcess([], code, output, '')), patch('sys.stdout', new_callable=io.StringIO), patch('sys.stderr', new_callable=io.StringIO):
                self.assertNotEqual(entrypoint.main(['--agent']), 0)
        with patch.object(entrypoint, 'run_surface_gate', return_value=1), patch.object(entrypoint.subprocess, 'run') as run:
            self.assertEqual(entrypoint.main(['--agent']), 1)
            run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
