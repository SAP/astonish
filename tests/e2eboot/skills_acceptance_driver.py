"""Exact current-invocation skills gates. No historic receipts or skipped PG proof."""
import argparse
import json
import os
from pathlib import Path
import sys
from collections import Counter
from bots_acceptance_driver import ROOT, clean_env, go_command, run

MODULE = 'github.com/SAP/astonish/'
RECOVERY = {'pkg/api': ['TestBotSkillRecoveryHTTPAcceptance']}
POSTGRES = {'pkg/store/entstore': ['TestBotSkillPostgresPreSkillArchive', 'TestBotSkillPostgresRecovery']}
CONTROLLED = {'pkg/api': ['TestBotSkillStorageHTTPAcceptance', 'TestBotSkillRuntimeHTTPAcceptance'],
              'pkg/daemon': ['TestBotSkillScheduleHTTPAcceptance']}


class ExactGoEvidence:
    def __init__(self, package, names):
        self.package = MODULE + package
        self.required = set(names)
        self.allowed = set(self.required)
        for name in names:
            parts = name.split('/')
            self.allowed.update('/'.join(parts[:n]) for n in range(1, len(parts)))
        self.events = Counter()
        self.failures = []
        self.started = set()
        self.finished = set()

    def feed(self, line):
        try:
            event = json.loads(line)
        except ValueError:
            self.failures.append('non-JSON test output')
            return
        if not isinstance(event, dict) or event.get('Package') != self.package:
            self.failures.append('foreign package evidence')
            return
        action, name = event.get('Action'), event.get('Test', '')
        if action in ('skip', 'fail'):
            self.failures.append('skipped/failed: '+name)
        if name and name not in self.allowed:
            self.failures.append('foreign test: '+name)
        if action == 'run':
            if name in self.started: self.failures.append('duplicate/replayed run: '+name)
            self.started.add(name)
        if action == 'pass':
            if name and name not in self.started: self.failures.append('pass without run: '+name)
            if name in self.finished: self.failures.append('duplicate/replayed pass: '+name)
            self.finished.add(name)
        if action in ('run', 'pass'):
            self.events[action, name] += 1

    def errors(self):
        errors = list(self.failures)
        for name in self.required:
            if self.events['run', name] != 1 or self.events['pass', name] != 1:
                errors.append('missing exact run/pass: '+name)
        if self.events['pass', ''] != 1: errors.append('missing package pass')
        if self.started - self.finished: errors.append('unfinished tests')
        return errors


def required_cases(names):
    from employee_http_acceptance import SKILLS_TESTS
    required = set(names)
    for _, test, cases in SKILLS_TESTS.values():
        if test in names:
            required.update(test+'/'+case for case in cases)
    for test in ('TestBotSkillRecoveryHTTPAcceptance', 'TestBotSkillPostgresRecovery'):
        if test in names:
            required.update(test+'/'+scope for scope in ('personal', 'team'))
    return required


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--skills-stage', required=True, choices=('all', 'recovery', 'postgres'))
    parser.add_argument('--config', type=Path, help='explicit isolated live launcher JSON; required for all')
    args = parser.parse_args(argv)
    if (args.skills_stage == 'all') != bool(args.config):
        parser.error('--config is required only with --skills-stage all')
    env = {key: value for key, value in clean_env(os.environ).items()
           if not key.endswith('API_KEY') and not key.startswith('ASTONISH_BOTS_LIVE_')
           and key not in ('KUBECONFIG', 'ASTONISH_BOT_BROWSER_PATH')}
    if args.skills_stage == 'all':
        # Validate explicit operator inputs before any resources; never discover
        # normal daemon/provider credentials or infer browser paths.
        from local_bot_smoke_launcher import config_environment
        from quality_live_contract import strict_json
        try:
            configured = config_environment(strict_json(args.config.read_text()))
        except (OSError, ValueError) as error:
            print('BLOCKED: explicit skills config preflight failed: '+str(error), file=sys.stderr)
            return 2
        env['ASTONISH_BOT_BROWSER_PATH'] = configured['ASTONISH_BOT_BROWSER_PATH']
    if args.skills_stage in ('all', 'postgres'):
        if (not env.get('ASTONISH_TEST_DSN', '').strip()
                or env.get('ASTONISH_BOTS_TEST_DB_ISOLATED') != '1'):
            print('BLOCKED: explicit disposable ASTONISH_TEST_DSN and ASTONISH_BOTS_TEST_DB_ISOLATED=1 required', file=sys.stderr)
            return 2
        env['ASTONISH_REQUIRE_SKILL_POSTGRES'] = '1'
    groups = []
    if args.skills_stage == 'all': groups.append((CONTROLLED, None))
    if args.skills_stage in ('all', 'recovery'): groups.append((RECOVERY, None))
    if args.skills_stage in ('all', 'postgres'): groups.append((POSTGRES, 'integration'))
    for group, tags in groups:
        for package, names in group.items():
            child = dict(env)
            if tags is None:
                child.pop('ASTONISH_TEST_DSN', None)
                child.pop('ASTONISH_REQUIRE_SKILL_POSTGRES', None)
            if not run(go_command('./'+package, names, tags), child, ExactGoEvidence(package, required_cases(names))):
                return 1
    if args.skills_stage == 'all':
        if not run([sys.executable, str(ROOT/'tests/e2eboot/bot_skill_surface_acceptance.py')], env):
            return 1
        # Launcher creates fresh root, candidate daemon, browser and nonce, validates
        # 17 employee + 6 quality + 7 skills receipts in this very invocation.
        if not run([sys.executable, str(ROOT/'tests/e2eboot/local_bot_smoke_launcher.py'),
                    '--verify-bot-skills', '--config', str(args.config.resolve())], env, timeout=4000):
            return 1
    print(json.dumps({'skills_stage': args.skills_stage, 'status': 'passed',
                      'full_skills_acceptance': args.skills_stage == 'all'}))
    return 0

if __name__ == '__main__':
    sys.exit(main())
