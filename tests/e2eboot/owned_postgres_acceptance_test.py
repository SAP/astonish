import contextlib
import io
import json
import subprocess
import unittest
from unittest.mock import patch

import owned_postgres_acceptance as pg


class OwnedPostgresTest(unittest.TestCase):
    token = 'a' * 32
    password = 'b' * 64
    cid = 'c' * 64

    def exercise(self, failure=None, child=True):
        calls = []
        info = {'Id': self.cid, 'Name': '/astonish-agent-pg-' + self.token,
                'Config': {'Labels': {pg.OWNER_LABEL: self.token}, 'Image': pg.IMAGE},
                'NetworkSettings': {'Ports': {'5432/tcp': [
                    {'HostIp': '127.0.0.1', 'HostPort': '45678'}]}}}
        if failure == 'foreign':
            info['Config']['Labels'][pg.OWNER_LABEL] = 'someone-else'
        if failure == 'public':
            info['NetworkSettings']['Ports']['5432/tcp'][0]['HostIp'] = '0.0.0.0'
        if failure == 'bad-port':
            info['NetworkSettings']['Ports']['5432/tcp'][0]['HostPort'] = '0'

        def docker(command, **kwargs):
            calls.append(command)
            self.assertNotIn(self.password, ' '.join(command))
            self.assertEqual(self.password, kwargs['env']['POSTGRES_PASSWORD'])
            self.assertEqual(subprocess.PIPE, kwargs['stderr'])
            action = command[1]
            if action == failure or (failure == 'create-lost-response' and action == 'create'):
                raise subprocess.CalledProcessError(1, command, stderr=self.password)
            if failure == 'no-docker':
                raise FileNotFoundError(self.password)
            if action == 'inspect':
                return subprocess.CompletedProcess(command, 0, json.dumps([info]), '')
            if action == 'create':
                return subprocess.CompletedProcess(command, 0, self.cid + '\n', '')
            return subprocess.CompletedProcess(command, 1 if failure == 'timeout' and action == 'exec' else 0, '', '')

        output = io.StringIO()
        with patch.object(pg.secrets, 'token_hex', side_effect=[self.token, self.password]), patch.object(
                pg.subprocess, 'run', side_effect=docker), patch.object(pg.time, 'sleep'), patch.object(
                pg, 'run', side_effect=RuntimeError(self.password) if failure == 'child-error' else None,
                return_value=child) as run, contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            result = pg.run_owned_postgres(['strict-child'], {'PATH': '/bin'})
        self.assertNotIn(self.password, output.getvalue())
        return result, calls, run

    def test_absent_and_explicit_modes_never_adopt_partial_configuration(self):
        self.assertEqual('owned', pg.postgres_mode({}))
        self.assertEqual('external', pg.postgres_mode({pg.DSN: 'explicit', pg.ISOLATED: '1'}))
        for source in ({pg.DSN: ''}, {pg.ISOLATED: ''}, {pg.DSN: 'secret'},
                       {pg.ISOLATED: '1'}, {pg.DSN: 'secret', pg.ISOLATED: 'true'},
                       {pg.DSN: ' ', pg.ISOLATED: '1'}):
            with self.subTest(source=source), self.assertRaises(ValueError) as error:
                pg.postgres_mode(source)
            self.assertNotIn('secret', str(error.exception))

    def test_success_owned_loopback_random_credential_and_strict_child(self):
        result, calls, run = self.exercise()
        self.assertEqual(0, result)
        self.assertEqual(['docker', 'rm', '-f', '-v', self.cid], calls[-1])
        create = calls[0]
        self.assertEqual('127.0.0.1::5432', create[create.index('-p') + 1])
        self.assertIn(pg.OWNER_LABEL + '=' + self.token, create)
        self.assertEqual(pg.IMAGE, create[-1])
        command, env = run.call_args.args
        self.assertEqual(['strict-child'], command)
        self.assertEqual('1', env[pg.ISOLATED])
        self.assertEqual('1', env['ASTONISH_REQUIRE_SKILL_POSTGRES'])
        self.assertEqual(f'postgres://postgres:{self.password}@127.0.0.1:45678/postgres?sslmode=disable', env[pg.DSN])
        self.assertNotIn('POSTGRES_PASSWORD', env)
        self.assertEqual(['create', 'inspect', 'start', 'inspect', 'exec', 'inspect', 'rm'],
                         [call[1] for call in calls])

    def test_child_failure_is_not_a_skip_and_still_removes_volume(self):
        result, calls, run = self.exercise(child=False)
        self.assertEqual(1, result)
        run.assert_called_once()
        self.assertEqual('rm', calls[-1][1])

    def test_start_readiness_binding_and_child_exceptions_cleanup(self):
        for failure in ('start', 'timeout', 'public', 'bad-port', 'child-error', 'create-lost-response'):
            with self.subTest(failure=failure):
                result, calls, run = self.exercise(failure)
                self.assertEqual(2, result)
                self.assertEqual(['docker', 'rm', '-f', '-v', self.cid], calls[-1])
                if failure != 'child-error':
                    run.assert_not_called()

    def test_foreign_container_is_never_started_or_removed(self):
        result, calls, run = self.exercise('foreign')
        self.assertEqual(2, result)
        self.assertFalse(any(call[1] in ('rm', 'start', 'exec') for call in calls))
        run.assert_not_called()

    def test_missing_docker_and_inspection_failure_block_without_child_or_removal(self):
        for failure in ('no-docker', 'inspect'):
            with self.subTest(failure=failure):
                result, calls, run = self.exercise(failure)
                self.assertEqual(2, result)
                self.assertFalse(any(call[1] == 'rm' for call in calls))
                run.assert_not_called()

    def test_cleanup_failure_cannot_report_success(self):
        result, calls, run = self.exercise('rm')
        self.assertEqual(2, result)
        run.assert_called_once()


if __name__ == '__main__':
    unittest.main()
